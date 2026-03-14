package terminal

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"opencoderouter/internal/session"
)

func TestSessionDialerReadWriteResizeClose(t *testing.T) {
	workspace := t.TempDir()
	binaryPath := filepath.Join(workspace, "opencode-test-attach.sh")
	script := "#!/bin/sh\ncat\n"
	if err := os.WriteFile(binaryPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write test attach script: %v", err)
	}

	dialer := NewSessionDialer(SessionDialerConfig{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		OpenCodeBinary: binaryPath,
		AttachArgsBuilder: func(handle session.SessionHandle) ([]string, error) {
			return []string{}, nil
		},
	})

	conn, err := dialer(nil, session.SessionHandle{ID: "session-1", DaemonPort: 1234, WorkspacePath: workspace})
	if err != nil {
		t.Fatalf("dial terminal: %v", err)
	}
	defer func() { _ = conn.Close() }()

	in := []byte("hello-terminal\n")
	if _, err := conn.Write(in); err != nil {
		t.Fatalf("write terminal input: %v", err)
	}

	if err := conn.Resize(100, 30); err != nil {
		t.Fatalf("resize terminal: %v", err)
	}

	if err := conn.Resize(0, 30); err == nil {
		t.Fatal("expected invalid resize error")
	}

	buf := make([]byte, len(in))
	if err := readFullWithTimeout(conn, buf, 2*time.Second); err != nil {
		t.Fatalf("read terminal output: %v", err)
	}
	echo := string(buf)
	if echo != "hello-terminal\r" && echo != "hello-terminal\n" {
		t.Fatalf("terminal echo=%q want either carriage-return or newline form", echo)
	}

	_ = conn.Close()
	if err := conn.Close(); err != nil {
		t.Fatalf("second close terminal conn: %v", err)
	}
}

func TestSessionDialerRequiresSessionMetadata(t *testing.T) {
	dialer := NewSessionDialer(SessionDialerConfig{})

	if _, err := dialer(nil, session.SessionHandle{}); err == nil {
		t.Fatal("expected error when session id/workspace/daemon missing")
	}

	if _, err := dialer(nil, session.SessionHandle{ID: "session-1", WorkspacePath: t.TempDir()}); err == nil {
		t.Fatal("expected error when daemon port missing")
	}

	if _, err := dialer(nil, session.SessionHandle{ID: "session-1"}); err == nil {
		t.Fatal("expected error when workspace missing")
	}

	if _, err := dialer(nil, session.SessionHandle{ID: "session-1", DaemonPort: 1}); err == nil {
		t.Fatal("expected error when workspace missing")
	}
}

func TestSessionDialerCommandFactoryNilCommand(t *testing.T) {
	dialer := NewSessionDialer(SessionDialerConfig{
		CommandFactory: func(string, ...string) *exec.Cmd { return nil },
		AttachArgsBuilder: func(handle session.SessionHandle) ([]string, error) {
			return []string{}, nil
		},
	})

	if _, err := dialer(nil, session.SessionHandle{ID: "session-1", DaemonPort: 1234, WorkspacePath: t.TempDir()}); err == nil {
		t.Fatal("expected error when command factory returns nil")
	}
}

func TestSessionDialerAttachArgsBuilderOverrideBypassesDefaultDaemonArgs(t *testing.T) {
	workspace := t.TempDir()
	binaryPath := filepath.Join(workspace, "opencode-test-override-args.sh")
	script := "#!/bin/sh\ncat\n"
	if err := os.WriteFile(binaryPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write override args script: %v", err)
	}

	var capturedArgs []string

	dialer := NewSessionDialer(SessionDialerConfig{
		OpenCodeBinary: binaryPath,
		AttachArgsBuilder: func(handle session.SessionHandle) ([]string, error) {
			return []string{"custom", "args"}, nil
		},
		CommandFactory: func(binary string, args ...string) *exec.Cmd {
			capturedArgs = append([]string(nil), args...)
			return exec.Command(binary, args...)
		},
	})

	conn, err := dialer(nil, session.SessionHandle{ID: "session-override", DaemonPort: 12345, WorkspacePath: workspace})
	if err != nil {
		t.Fatalf("dial with override args: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if len(capturedArgs) != 2 || capturedArgs[0] != "custom" || capturedArgs[1] != "args" {
		t.Fatalf("override args=%v want [custom args]", capturedArgs)
	}
}

func TestResolveDaemonSessionIDByWorkspaceSuccess(t *testing.T) {
	workspace := "/tmp/workspace-match"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`[
			{"id":"daemon-old","directory":"%s","status":"active","lastActivity":"2026-03-06T10:00:00Z"},
			{"id":"daemon-new","directory":"%s","status":"active","lastActivity":"2026-03-06T11:00:00Z"},
			{"id":"daemon-other","directory":"/tmp/other","status":"active","lastActivity":"2026-03-06T12:00:00Z"}
		]`, workspace, workspace)))
	}))
	defer server.Close()

	resolved, err := resolveDaemonSessionID(daemonPortFromServer(t, server), workspace)
	if err != nil {
		t.Fatalf("resolve daemon session id: %v", err)
	}
	if resolved != "daemon-new" {
		t.Fatalf("resolved daemon session id=%q want=%q", resolved, "daemon-new")
	}
}

func TestResolveDaemonSessionIDNoMatch(t *testing.T) {
	workspace := "/tmp/workspace-missing"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"daemon-other","directory":"/tmp/other","status":"active"}]`))
	}))
	defer server.Close()

	_, err := resolveDaemonSessionID(daemonPortFromServer(t, server), workspace)
	if err == nil {
		t.Fatal("expected no-match error")
	}
	if !errors.Is(err, errDaemonSessionNotFound) {
		t.Fatalf("error=%v want errDaemonSessionNotFound", err)
	}
	if !strings.Contains(err.Error(), "daemon session not found for workspace") {
		t.Fatalf("error=%q want no-match message", err)
	}
}

func TestSessionDialerDefaultArgsUseResolvedDaemonSessionID(t *testing.T) {
	workspace := t.TempDir()
	binaryPath := filepath.Join(workspace, "opencode-test-resolved-daemon-id.sh")
	script := "#!/bin/sh\ncat\n"
	if err := os.WriteFile(binaryPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write resolved-id args script: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`[{"id":"daemon-resolved-42","directory":%q,"status":"active","lastActivity":"2026-03-06T11:00:00Z"}]`, workspace)))
	}))
	defer server.Close()

	var capturedArgs []string
	port := daemonPortFromServer(t, server)

	dialer := NewSessionDialer(SessionDialerConfig{
		OpenCodeBinary: binaryPath,
		CommandFactory: func(binary string, args ...string) *exec.Cmd {
			capturedArgs = append([]string(nil), args...)
			return exec.Command(binary, args...)
		},
	})

	conn, err := dialer(nil, session.SessionHandle{ID: "session-1", DaemonPort: port, WorkspacePath: workspace})
	if err != nil {
		t.Fatalf("dial with resolved daemon session id: %v", err)
	}
	defer func() { _ = conn.Close() }()

	wantURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	if len(capturedArgs) != 4 || capturedArgs[0] != "attach" || capturedArgs[1] != wantURL || capturedArgs[2] != "-s" || capturedArgs[3] != "daemon-resolved-42" {
		t.Fatalf("default args=%v want [attach %s -s daemon-resolved-42]", capturedArgs, wantURL)
	}
	if strings.Contains(strings.Join(capturedArgs, " "), "session-1") {
		t.Fatalf("default args should not include control-plane session id, got %v", capturedArgs)
	}
}

func TestSessionDialerDefaultArgsFallbackToAttachWithoutSessionWhenNoMatch(t *testing.T) {
	workspace := t.TempDir()
	binaryPath := filepath.Join(workspace, "opencode-test-fallback-daemon-id.sh")
	script := "#!/bin/sh\ncat\n"
	if err := os.WriteFile(binaryPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fallback args script: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"daemon-other","directory":"/tmp/other","status":"active"}]`))
	}))
	defer server.Close()

	var capturedArgs []string
	port := daemonPortFromServer(t, server)

	dialer := NewSessionDialer(SessionDialerConfig{
		OpenCodeBinary: binaryPath,
		CommandFactory: func(binary string, args ...string) *exec.Cmd {
			capturedArgs = append([]string(nil), args...)
			return exec.Command(binary, args...)
		},
	})

	conn, err := dialer(nil, session.SessionHandle{ID: "session-1", DaemonPort: port, WorkspacePath: workspace})
	if err != nil {
		t.Fatalf("dial with no-match fallback: %v", err)
	}
	defer func() { _ = conn.Close() }()

	wantURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	if len(capturedArgs) != 2 || capturedArgs[0] != "attach" || capturedArgs[1] != wantURL {
		t.Fatalf("fallback args=%v want [attach %s]", capturedArgs, wantURL)
	}
	if strings.Contains(strings.Join(capturedArgs, " "), "-s") {
		t.Fatalf("fallback args should not include -s, got %v", capturedArgs)
	}
}

func TestSessionDialerDefaultArgsTransportOrParseErrorStillFails(t *testing.T) {
	workspace := t.TempDir()
	binaryPath := filepath.Join(workspace, "opencode-test-fallback-error.sh")
	script := "#!/bin/sh\ncat\n"
	if err := os.WriteFile(binaryPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write transport/parse error script: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"bad":"shape"}`))
	}))
	defer server.Close()

	dialer := NewSessionDialer(SessionDialerConfig{OpenCodeBinary: binaryPath})
	_, err := dialer(nil, session.SessionHandle{ID: "session-1", DaemonPort: daemonPortFromServer(t, server), WorkspacePath: workspace})
	if err == nil {
		t.Fatal("expected parse-shape error")
	}
	if errors.Is(err, errDaemonSessionNotFound) {
		t.Fatalf("unexpected no-match fallback error classification: %v", err)
	}
	if !strings.Contains(err.Error(), "decode daemon sessions payload") {
		t.Fatalf("error=%q want decode error", err)
	}
}

func daemonPortFromServer(t *testing.T, server *httptest.Server) int {
	t.Helper()

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}

	portText := u.Port()
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse daemon port %q: %v", portText, err)
	}

	return port
}

func readFullWithTimeout(r io.Reader, buf []byte, timeout time.Duration) error {
	errCh := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(r, buf)
		errCh <- err
	}()

	select {
	case err := <-errCh:
		return err
	case <-time.After(timeout):
		return io.EOF
	}
}
