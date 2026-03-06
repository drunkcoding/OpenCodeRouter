package cache

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

var ErrCacheClosed = errors.New("scrollback cache is closed")

const (
	jsonlExtension  = ".jsonl"
	storageDirPerm  = 0o755
	sessionFilePerm = 0o600
)

type sessionWriter struct {
	file   *os.File
	writer *bufio.Writer
}

type JSONLCache struct {
	config      CacheConfig
	mu          sync.Mutex
	closed      bool
	writers     map[string]*sessionWriter
	entryCounts map[string]int
	lru         *sessionLRU
	logger      *slog.Logger
}

// JSONL schema contract:
//   - File path layout: {storagePath}/{sessionID}.jsonl
//   - Each line is one JSON object encoded from Entry.
//   - Entry.Content ([]byte) is serialized by encoding/json as base64 text.
//   - Lines are append-only and chronological for stable replay/hydration.
func (c *JSONLCache) Append(sessionID string, entry Entry) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.validateOpenLocked(sessionID); err != nil {
		return err
	}

	count, err := c.sessionCountLocked(sessionID)
	if err != nil {
		return err
	}

	line, err := encodeEntryLine(entry)
	if err != nil {
		return err
	}

	writer, err := c.writerLocked(sessionID)
	if err != nil {
		return err
	}
	if _, err := writer.writer.Write(line); err != nil {
		_ = c.closeWriterLocked(sessionID)
		return err
	}
	if err := writer.writer.Flush(); err != nil {
		_ = c.closeWriterLocked(sessionID)
		return err
	}

	c.lru.AddSize(sessionID, int64(len(line)))
	c.markAccessLocked(sessionID)
	c.entryCounts[sessionID] = count + 1

	if c.config.MaxEntriesPerSession > 0 && c.entryCounts[sessionID] > c.config.MaxEntriesPerSession {
		if err := c.trimSessionLocked(sessionID, c.config.MaxEntriesPerSession); err != nil {
			return err
		}
	}

	return c.evictLocked()
}

func (c *JSONLCache) Get(sessionID string, offset, limit int) ([]Entry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.validateOpenLocked(sessionID); err != nil {
		return nil, err
	}

	entries, err := c.readEntriesLocked(sessionID)
	if err != nil {
		return nil, err
	}
	c.markAccessLocked(sessionID)

	if offset < 0 {
		offset = 0
	}
	if offset >= len(entries) {
		return []Entry{}, nil
	}

	end := len(entries)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}

	out := make([]Entry, end-offset)
	copy(out, entries[offset:end])
	return out, nil
}

func (c *JSONLCache) Trim(sessionID string, maxEntries int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.validateOpenLocked(sessionID); err != nil {
		return err
	}
	return c.trimSessionLocked(sessionID, maxEntries)
}

func (c *JSONLCache) Clear(sessionID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.validateOpenLocked(sessionID); err != nil {
		return err
	}
	return c.removeSessionLocked(sessionID)
}

func (c *JSONLCache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}

	var closeErr error
	for sessionID := range c.writers {
		closeErr = errors.Join(closeErr, c.closeWriterLocked(sessionID))
	}
	c.closed = true
	return closeErr
}

func (c *JSONLCache) bootstrapFromDisk() error {
	entries, err := os.ReadDir(c.config.StoragePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	sessionIDs := make([]string, 0, len(entries))
	sizes := make(map[string]int64, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, jsonlExtension) {
			continue
		}

		sessionID := strings.TrimSuffix(name, jsonlExtension)
		if strings.TrimSpace(sessionID) == "" {
			continue
		}

		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		sizes[sessionID] = info.Size()
		sessionIDs = append(sessionIDs, sessionID)
	}

	sort.Strings(sessionIDs)
	for _, sessionID := range sessionIDs {
		c.lru.SetSize(sessionID, sizes[sessionID])
		c.lru.Ensure(sessionID)
	}

	return nil
}

func (c *JSONLCache) validateOpenLocked(sessionID string) error {
	if c.closed {
		return ErrCacheClosed
	}
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("sessionID is required")
	}
	return nil
}

func (c *JSONLCache) sessionPath(sessionID string) string {
	return filepath.Join(c.config.StoragePath, sessionID+jsonlExtension)
}

func (c *JSONLCache) writerLocked(sessionID string) (*sessionWriter, error) {
	if writer, ok := c.writers[sessionID]; ok {
		return writer, nil
	}

	if err := os.MkdirAll(c.config.StoragePath, storageDirPerm); err != nil {
		return nil, err
	}

	file, err := os.OpenFile(c.sessionPath(sessionID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, sessionFilePerm)
	if err != nil {
		return nil, err
	}

	writer := &sessionWriter{
		file:   file,
		writer: bufio.NewWriter(file),
	}
	c.writers[sessionID] = writer
	return writer, nil
}

func (c *JSONLCache) closeWriterLocked(sessionID string) error {
	writer, ok := c.writers[sessionID]
	if !ok {
		return nil
	}
	delete(c.writers, sessionID)

	flushErr := writer.writer.Flush()
	closeErr := writer.file.Close()
	return errors.Join(flushErr, closeErr)
}

func (c *JSONLCache) sessionCountLocked(sessionID string) (int, error) {
	if count, ok := c.entryCounts[sessionID]; ok {
		return count, nil
	}

	entries, err := c.readEntriesLocked(sessionID)
	if err != nil {
		return 0, err
	}
	count := len(entries)
	c.entryCounts[sessionID] = count
	return count, nil
}

func (c *JSONLCache) markAccessLocked(sessionID string) {
	if c.config.EvictionPolicy == EvictionPolicyFIFO {
		c.lru.Ensure(sessionID)
		return
	}
	c.lru.Touch(sessionID)
}

func (c *JSONLCache) readEntriesLocked(sessionID string) ([]Entry, error) {
	path := c.sessionPath(sessionID)
	entries, err := c.decodeJSONLFile(path, sessionID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			c.entryCounts[sessionID] = 0
			c.lru.SetSize(sessionID, 0)
			return []Entry{}, nil
		}
		return nil, err
	}

	size, err := fileSize(path)
	if err != nil {
		return nil, err
	}
	c.lru.SetSize(sessionID, size)
	c.entryCounts[sessionID] = len(entries)
	return entries, nil
}

func (c *JSONLCache) decodeJSONLFile(path, sessionID string) ([]Entry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	entries := make([]Entry, 0, 128)
	reader := bufio.NewReader(file)
	lineNo := 0

	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineNo++
			trimmed := bytes.TrimRight(line, "\r\n")
			if len(trimmed) > 0 {
				var entry Entry
				if err := json.Unmarshal(trimmed, &entry); err != nil {
					c.logger.Warn("cache skipping malformed JSONL line", "session_id", sessionID, "line", lineNo, "error", err)
				} else {
					entries = append(entries, entry)
				}
			}
		}

		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}

	return entries, nil
}

func (c *JSONLCache) trimSessionLocked(sessionID string, maxEntries int) error {
	if maxEntries <= 0 {
		return c.removeSessionLocked(sessionID)
	}

	entries, err := c.readEntriesLocked(sessionID)
	if err != nil {
		return err
	}
	if len(entries) <= maxEntries {
		c.markAccessLocked(sessionID)
		return c.evictLocked()
	}

	trimmed := entries[len(entries)-maxEntries:]
	if err := c.rewriteSessionLocked(sessionID, trimmed); err != nil {
		return err
	}

	c.entryCounts[sessionID] = len(trimmed)
	c.markAccessLocked(sessionID)
	return c.evictLocked()
}

func (c *JSONLCache) rewriteSessionLocked(sessionID string, entries []Entry) error {
	if err := c.closeWriterLocked(sessionID); err != nil {
		return err
	}

	if err := os.MkdirAll(c.config.StoragePath, storageDirPerm); err != nil {
		return err
	}

	path := c.sessionPath(sessionID)
	tmpPath := path + ".tmp"

	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, sessionFilePerm)
	if err != nil {
		return err
	}

	writer := bufio.NewWriter(file)
	var totalBytes int64
	writeErr := func() error {
		for _, entry := range entries {
			line, lineErr := encodeEntryLine(entry)
			if lineErr != nil {
				return lineErr
			}
			written, lineErr := writer.Write(line)
			if lineErr != nil {
				return lineErr
			}
			totalBytes += int64(written)
		}
		return writer.Flush()
	}()
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		if removeErr := os.Remove(tmpPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			c.logger.Debug("failed to remove temporary cache file", "path", tmpPath, "error", removeErr)
			return errors.Join(writeErr, closeErr, removeErr)
		}
		return errors.Join(writeErr, closeErr)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		if removeErr := os.Remove(tmpPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			c.logger.Debug("failed to remove temporary cache file after rename error", "path", tmpPath, "error", removeErr)
			return errors.Join(err, removeErr)
		}
		return err
	}

	c.lru.SetSize(sessionID, totalBytes)
	return nil
}

func (c *JSONLCache) removeSessionLocked(sessionID string) error {
	closeErr := c.closeWriterLocked(sessionID)
	removeErr := os.Remove(c.sessionPath(sessionID))
	if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return errors.Join(closeErr, removeErr)
	}

	delete(c.entryCounts, sessionID)
	c.lru.Remove(sessionID)
	return closeErr
}

func (c *JSONLCache) evictLocked() error {
	if c.config.MaxTotalSize <= 0 {
		return nil
	}

	for c.lru.TotalSize() > c.config.MaxTotalSize {
		sessionID, ok := c.lru.Oldest()
		if !ok {
			break
		}
		if err := c.removeSessionLocked(sessionID); err != nil {
			return err
		}
	}

	return nil
}

func encodeEntryLine(entry Entry) ([]byte, error) {
	encoded, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}
