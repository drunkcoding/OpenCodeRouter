package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"opencoderouter/internal/session"

	"github.com/charmbracelet/x/xpty"
)

type SessionDialerConfig struct {
	Logger            *slog.Logger
	OpenCodeBinary    string
	AttachArgsBuilder func(handle session.SessionHandle) ([]string, error)
	CommandFactory    func(binary string, args ...string) *exec.Cmd
}

type ptySessionConn struct {
	pty       xpty.Pty
	cmd       *exec.Cmd
	logger    *slog.Logger
	sessionID string
	closeOnce sync.Once
	closeErr  error
}

var errDaemonSessionNotFound = errors.New("daemon session not found for workspace")

func NewSessionDialer(cfg SessionDialerConfig) func(ctx context.Context, handle session.SessionHandle) (session.TerminalConn, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	binary := cfg.OpenCodeBinary
	if binary == "" {
		binary = "opencode"
	}

	argsBuilder := cfg.AttachArgsBuilder

	cmdFactory := cfg.CommandFactory
	if cmdFactory == nil {
		cmdFactory = exec.Command
	}

	return func(_ context.Context, handle session.SessionHandle) (session.TerminalConn, error) {
		if handle.ID == "" {
			return nil, errors.New("session id is required")
		}
		if handle.DaemonPort <= 0 {
			return nil, errors.New("daemon port must be greater than zero")
		}
		if handle.WorkspacePath == "" {
			return nil, errors.New("workspace path is required")
		}

		var (
			args []string
			err  error
		)

		if argsBuilder != nil {
			args, err = argsBuilder(handle)
			if err != nil {
				return nil, err
			}
		} else {
			daemonURL := fmt.Sprintf("http://127.0.0.1:%d", handle.DaemonPort)
			daemonSessionID, resolveErr := resolveDaemonSessionID(handle.DaemonPort, handle.WorkspacePath)
			if resolveErr != nil {
				if errors.Is(resolveErr, errDaemonSessionNotFound) {
					args = []string{"attach", daemonURL}
				} else {
					return nil, resolveErr
				}
			} else {
				args = []string{"attach", daemonURL, "-s", daemonSessionID}
			}
		}

		ptyHandle, err := xpty.NewPty(80, 24)
		if err != nil {
			return nil, fmt.Errorf("allocate pty: %w", err)
		}

		cmd := cmdFactory(binary, args...)
		if cmd == nil {
			_ = ptyHandle.Close()
			return nil, errors.New("command factory returned nil command")
		}
		cmd.Dir = handle.WorkspacePath
		cmd.Env = append([]string{}, os.Environ()...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}

		if err := ptyHandle.Start(cmd); err != nil {
			_ = ptyHandle.Close()
			return nil, fmt.Errorf("start attach command: %w", err)
		}

		if unixPTY, ok := ptyHandle.(*xpty.UnixPty); ok {
			_ = unixPTY.Slave().Close()
		}

		conn := &ptySessionConn{
			pty:       ptyHandle,
			cmd:       cmd,
			logger:    logger,
			sessionID: handle.ID,
		}
		return conn, nil
	}
}

type daemonSessionRecord struct {
	ID           string
	Status       string
	Workspace    string
	LastActivity time.Time
}

func resolveDaemonSessionID(daemonPort int, workspacePath string) (string, error) {
	daemonURL := fmt.Sprintf("http://127.0.0.1:%d", daemonPort)
	sessions, err := fetchDaemonSessions(daemonURL)
	if err != nil {
		return "", err
	}

	targetWorkspace := filepath.Clean(workspacePath)
	matches := make([]daemonSessionRecord, 0, len(sessions))
	for _, s := range sessions {
		if s.ID == "" {
			continue
		}
		if s.Workspace == "" {
			continue
		}
		if filepath.Clean(s.Workspace) == targetWorkspace {
			matches = append(matches, s)
		}
	}

	if len(matches) == 0 {
		return "", fmt.Errorf("%w %q at %s/session; start or resume a daemon session in that workspace first", errDaemonSessionNotFound, workspacePath, daemonURL)
	}

	best := matches[0]
	for _, candidate := range matches[1:] {
		if isBetterSessionCandidate(candidate, best) {
			best = candidate
		}
	}

	return best.ID, nil
}

func fetchDaemonSessions(daemonURL string) ([]daemonSessionRecord, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, daemonURL+"/session", nil)
	if err != nil {
		return nil, fmt.Errorf("build daemon session request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query daemon sessions at %s/session: %w", daemonURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read daemon sessions response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("daemon session query failed at %s/session: status %d", daemonURL, resp.StatusCode)
	}

	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode daemon sessions payload: %w", err)
	}

	array, err := extractSessionArray(payload)
	if err != nil {
		return nil, err
	}

	records := make([]daemonSessionRecord, 0, len(array))
	for _, item := range array {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		records = append(records, daemonSessionRecord{
			ID:           firstString(m, "id"),
			Status:       strings.ToLower(firstString(m, "status")),
			Workspace:    firstString(m, "directory", "workspacePath", "workspace_path", "path", "cwd"),
			LastActivity: extractLastActivity(m),
		})
	}

	return records, nil
}

func extractSessionArray(payload any) ([]any, error) {
	if arr, ok := payload.([]any); ok {
		return arr, nil
	}
	if m, ok := payload.(map[string]any); ok {
		if arr, ok := m["sessions"].([]any); ok {
			return arr, nil
		}
	}
	return nil, errors.New("decode daemon sessions payload: unexpected shape")
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		v, ok := m[key]
		if !ok || v == nil {
			continue
		}
		switch value := v.(type) {
		case string:
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		case json.Number:
			if s := strings.TrimSpace(value.String()); s != "" {
				return s
			}
		}
	}
	return ""
}

func extractLastActivity(m map[string]any) time.Time {
	for _, key := range []string{"lastActivity", "last_activity", "updated", "created", "createdAt"} {
		v, ok := m[key]
		if !ok || v == nil {
			continue
		}
		if ts := parseTimestamp(v); !ts.IsZero() {
			return ts
		}
	}
	return time.Time{}
}

func parseTimestamp(v any) time.Time {
	switch raw := v.(type) {
	case string:
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return time.Time{}
		}
		if ts, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return ts
		}
		if iv, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return epochToTime(iv)
		}
	case float64:
		return epochToTime(int64(raw))
	case json.Number:
		if iv, err := raw.Int64(); err == nil {
			return epochToTime(iv)
		}
		if fv, err := raw.Float64(); err == nil {
			return epochToTime(int64(fv))
		}
	}
	return time.Time{}
}

func epochToTime(v int64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	if v > 1_000_000_000_000 {
		return time.UnixMilli(v)
	}
	return time.Unix(v, 0)
}

func isBetterSessionCandidate(a, b daemonSessionRecord) bool {
	if sessionStatusRank(a.Status) != sessionStatusRank(b.Status) {
		return sessionStatusRank(a.Status) > sessionStatusRank(b.Status)
	}
	if !a.LastActivity.Equal(b.LastActivity) {
		return a.LastActivity.After(b.LastActivity)
	}
	return a.ID < b.ID
}

func sessionStatusRank(status string) int {
	switch status {
	case "active", "running":
		return 2
	case "idle":
		return 1
	default:
		return 0
	}
}

func (c *ptySessionConn) Read(p []byte) (int, error) {
	return c.pty.Read(p)
}

func (c *ptySessionConn) Write(p []byte) (int, error) {
	return c.pty.Write(p)
}

func (c *ptySessionConn) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("invalid terminal size %dx%d", cols, rows)
	}
	return c.pty.Resize(cols, rows)
}

func (c *ptySessionConn) Close() error {
	c.closeOnce.Do(func() {
		if c.pty != nil {
			if err := c.pty.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
				c.closeErr = errors.Join(c.closeErr, err)
			}
		}

		if c.cmd != nil && c.cmd.Process != nil {
			if err := c.cmd.Process.Kill(); !isIgnorableKillError(err) {
				c.closeErr = errors.Join(c.closeErr, err)
			}
		}

		if c.logger != nil && c.closeErr != nil {
			c.logger.Debug("terminal dialer close completed with errors", "session_id", c.sessionID, "error", c.closeErr)
		}
	})
	return c.closeErr
}

func isIgnorableKillError(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, os.ErrProcessDone) {
		return true
	}
	if errors.Is(err, syscall.ESRCH) {
		return true
	}
	return false
}
