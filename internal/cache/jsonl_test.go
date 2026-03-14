package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestJSONLCacheAppendGetRoundTripAndMalformedLine(t *testing.T) {
	cache := newTestCache(t, CacheConfig{StoragePath: t.TempDir(), MaxEntriesPerSession: 1000, MaxTotalSize: 64 * 1024 * 1024})

	sessionID := "roundtrip"
	base := time.Unix(1_700_000_000, 0).UTC()
	entries := []Entry{
		{Timestamp: base, Type: EntryTypeAgentMessage, Content: []byte("alpha"), Metadata: map[string]any{"idx": 1}},
		{Timestamp: base.Add(time.Second), Type: EntryTypeToolCall, Content: []byte("beta"), Metadata: map[string]any{"idx": 2}},
		{Timestamp: base.Add(2 * time.Second), Type: EntryTypeTerminalOutput, Content: []byte("gamma"), Metadata: map[string]any{"idx": 3}},
	}

	for _, entry := range entries {
		if err := cache.Append(sessionID, entry); err != nil {
			t.Fatalf("append failed: %v", err)
		}
	}

	file, err := os.OpenFile(filepath.Join(cache.config.StoragePath, sessionID+jsonlExtension), os.O_APPEND|os.O_WRONLY, sessionFilePerm)
	if err != nil {
		t.Fatalf("open cache file failed: %v", err)
	}
	if _, err := file.WriteString("{this-is-not-json}\n"); err != nil {
		_ = file.Close()
		t.Fatalf("write malformed line failed: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close malformed writer failed: %v", err)
	}

	all, err := cache.Get(sessionID, 0, 0)
	if err != nil {
		t.Fatalf("get all failed: %v", err)
	}
	if len(all) != len(entries) {
		t.Fatalf("unexpected entries length: got %d want %d", len(all), len(entries))
	}
	for i := range entries {
		if !all[i].Timestamp.Equal(entries[i].Timestamp) || all[i].Type != entries[i].Type || string(all[i].Content) != string(entries[i].Content) {
			t.Fatalf("entry mismatch at index %d: got %+v want %+v", i, all[i], entries[i])
		}
		if fmt.Sprint(all[i].Metadata["idx"]) != fmt.Sprint(entries[i].Metadata["idx"]) {
			t.Fatalf("metadata mismatch at index %d: got=%v want=%v", i, all[i].Metadata["idx"], entries[i].Metadata["idx"])
		}
	}

	window, err := cache.Get(sessionID, 1, 1)
	if err != nil {
		t.Fatalf("paged get failed: %v", err)
	}
	if len(window) != 1 || window[0].Type != entries[1].Type || string(window[0].Content) != string(entries[1].Content) {
		t.Fatalf("unexpected paged result: %+v", window)
	}
}

func TestJSONLCacheTrimAndClear(t *testing.T) {
	cache := newTestCache(t, CacheConfig{StoragePath: t.TempDir(), MaxEntriesPerSession: 1000, MaxTotalSize: 64 * 1024 * 1024})

	sessionID := "trim-clear"
	base := time.Unix(1_700_000_000, 0).UTC()
	for i := 0; i < 5; i++ {
		entry := Entry{Timestamp: base.Add(time.Duration(i) * time.Second), Type: EntryTypeSystemEvent, Content: []byte(fmt.Sprintf("line-%d", i))}
		if err := cache.Append(sessionID, entry); err != nil {
			t.Fatalf("append %d failed: %v", i, err)
		}
	}

	if err := cache.Trim(sessionID, 2); err != nil {
		t.Fatalf("trim failed: %v", err)
	}

	trimmed, err := cache.Get(sessionID, 0, 0)
	if err != nil {
		t.Fatalf("get after trim failed: %v", err)
	}
	if len(trimmed) != 2 {
		t.Fatalf("unexpected trim size: got %d want 2", len(trimmed))
	}
	if string(trimmed[0].Content) != "line-3" || string(trimmed[1].Content) != "line-4" {
		t.Fatalf("trim kept unexpected entries: %+v", trimmed)
	}

	if err := cache.Clear(sessionID); err != nil {
		t.Fatalf("clear failed: %v", err)
	}

	entries, err := cache.Get(sessionID, 0, 0)
	if err != nil {
		t.Fatalf("get after clear failed: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty result after clear, got %d entries", len(entries))
	}
	if _, err := os.Stat(filepath.Join(cache.config.StoragePath, sessionID+jsonlExtension)); !os.IsNotExist(err) {
		t.Fatalf("expected cache file to be removed, stat err=%v", err)
	}
}

func TestJSONLCacheLRUEviction(t *testing.T) {
	entry := Entry{Timestamp: time.Unix(1_700_000_000, 0).UTC(), Type: EntryTypeTerminalOutput, Content: []byte("payload")}
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal test entry failed: %v", err)
	}
	lineSize := int64(len(encoded) + 1)

	cache := newTestCache(t, CacheConfig{
		StoragePath:          t.TempDir(),
		MaxEntriesPerSession: 1000,
		MaxTotalSize:         (lineSize * 2) + 5,
		EvictionPolicy:       EvictionPolicyLRU,
	})

	if err := cache.Append("s1", entry); err != nil {
		t.Fatalf("append s1 failed: %v", err)
	}
	if err := cache.Append("s2", entry); err != nil {
		t.Fatalf("append s2 failed: %v", err)
	}
	if _, err := cache.Get("s1", 0, 1); err != nil {
		t.Fatalf("touch s1 failed: %v", err)
	}
	if err := cache.Append("s3", entry); err != nil {
		t.Fatalf("append s3 failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(cache.config.StoragePath, "s2"+jsonlExtension)); !os.IsNotExist(err) {
		t.Fatalf("expected s2 to be evicted, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(cache.config.StoragePath, "s1"+jsonlExtension)); err != nil {
		t.Fatalf("expected s1 to be kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache.config.StoragePath, "s3"+jsonlExtension)); err != nil {
		t.Fatalf("expected s3 to be kept: %v", err)
	}

	total := int64(0)
	for _, sessionID := range []string{"s1", "s3"} {
		info, err := os.Stat(filepath.Join(cache.config.StoragePath, sessionID+jsonlExtension))
		if err != nil {
			t.Fatalf("stat %s failed: %v", sessionID, err)
		}
		total += info.Size()
	}
	if total > cache.config.MaxTotalSize {
		t.Fatalf("total cache size exceeds limit: total=%d limit=%d", total, cache.config.MaxTotalSize)
	}
}

func TestJSONLCacheConcurrentAppend(t *testing.T) {
	cache := newTestCache(t, CacheConfig{StoragePath: t.TempDir(), MaxEntriesPerSession: 20000, MaxTotalSize: 64 * 1024 * 1024})

	const goroutines = 16
	const perWorker = 250

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				entry := Entry{
					Timestamp: time.Unix(1_700_000_000+int64(i), 0).UTC(),
					Type:      EntryTypeTerminalOutput,
					Content:   []byte(fmt.Sprintf("g%d-%d", g, i)),
				}
				if err := cache.Append("concurrent", entry); err != nil {
					t.Errorf("append failed: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	entries, err := cache.Get("concurrent", 0, 0)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	expected := goroutines * perWorker
	if len(entries) != expected {
		t.Fatalf("unexpected entry count: got %d want %d", len(entries), expected)
	}
}

func TestJSONLCacheRoundTripPerformanceSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping performance smoke test in short mode")
	}

	cache := newTestCache(t, CacheConfig{StoragePath: t.TempDir(), MaxEntriesPerSession: 11000, MaxTotalSize: 128 * 1024 * 1024})

	const count = 10000
	start := time.Now()
	for i := 0; i < count; i++ {
		entry := Entry{
			Timestamp: time.Unix(1_700_000_000+int64(i), 0).UTC(),
			Type:      EntryTypeAgentMessage,
			Content:   []byte("perf"),
		}
		if err := cache.Append("perf", entry); err != nil {
			t.Fatalf("append failed at %d: %v", i, err)
		}
	}
	entries, err := cache.Get("perf", 0, count)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if len(entries) != count {
		t.Fatalf("unexpected perf entry count: got %d want %d", len(entries), count)
	}

	duration := time.Since(start)
	if duration > 8*time.Second {
		t.Fatalf("round-trip too slow: %s", duration)
	}
}

func newTestCache(t *testing.T, cfg CacheConfig) *JSONLCache {
	t.Helper()

	instance, err := NewJSONLCache(cfg)
	if err != nil {
		t.Fatalf("create cache failed: %v", err)
	}

	cache, ok := instance.(*JSONLCache)
	if !ok {
		t.Fatalf("unexpected cache type: %T", instance)
	}

	t.Cleanup(func() {
		if err := cache.Close(); err != nil {
			t.Fatalf("close cache failed: %v", err)
		}
	})

	return cache
}
