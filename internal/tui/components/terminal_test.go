package components

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"opencoderouter/internal/tui/model"

	tea "charm.land/bubbletea/v2"
)

var testANSIPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func stripANSIAndCR(s string) string {
	noANSI := testANSIPattern.ReplaceAllString(s, "")
	return strings.ReplaceAll(noANSI, "\r", "")
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}

func setupMockSSHEnvironment(t *testing.T) model.Host {
	t.Helper()

	binDir := t.TempDir()

	sshScript := `#!/bin/sh
set -eu
while [ "$#" -gt 0 ]; do
  case "$1" in
    -t|-tt)
      shift
      ;;
    -S)
      if [ "$#" -lt 2 ]; then
        exit 2
      fi
      shift 2
      ;;
    -o)
      if [ "$#" -lt 2 ]; then
        exit 2
      fi
      shift 2
      ;;
    -*)
      shift
      ;;
    *)
      break
      ;;
  esac
done
if [ "$#" -lt 2 ]; then
  exit 2
fi
shift
exec /bin/sh -lc "$1"
`
	opencodeScript := `#!/bin/sh
set -eu
if [ "${1:-}" = "-s" ] && [ "$#" -ge 2 ]; then
  mode="$2"
else
  mode=""
fi

case "$mode" in
  long)
    trap 'exit 0' TERM INT
    while :; do
      sleep 0.05
    done
    ;;
  view)
    printf 'view-ready\n'
    ;;
  write)
    IFS= read -r line || true
    printf 'echo:%s\n' "$line"
    ;;
  graceful)
    printf 'graceful-exit\n'
    exit 0
    ;;
  crash)
    printf 'crash\n' >&2
    exit 7
    ;;
  *)
    printf 'created\n'
    ;;
esac
`

	writeExecutable(t, filepath.Join(binDir, "ssh"), sshScript)
	writeExecutable(t, filepath.Join(binDir, "mock-opencode"), opencodeScript)

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return model.Host{Name: "mock-host", OpencodeBin: "mock-opencode"}
}

func waitFor(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s", desc)
}

func waitForClosedMsg(t *testing.T, ch <-chan tea.Msg, sessionID string, timeout time.Duration) model.TerminalClosedMsg {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case msg := <-ch:
			if closed, ok := msg.(model.TerminalClosedMsg); ok && closed.SessionID == sessionID {
				return closed
			}
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	t.Fatalf("timed out waiting for TerminalClosedMsg for session %q", sessionID)
	return model.TerminalClosedMsg{}
}

func newTestSession(sessionID string) model.Session {
	return model.Session{ID: sessionID}
}

func TestSessionTerminalCreationWithMockSubprocess(t *testing.T) {
	host := setupMockSSHEnvironment(t)
	msgs := make(chan tea.Msg, 64)

	terminal, err := NewSessionTerminal(host, newTestSession("long"), 80, 24, func(msg tea.Msg) { msgs <- msg }, slog.Default(), nil)
	if err != nil {
		t.Fatalf("NewSessionTerminal: %v", err)
	}

	if terminal == nil {
		t.Fatal("expected non-nil terminal")
	}
	if terminal.IsClosed() {
		t.Fatal("terminal should be open immediately after creation")
	}

	if err := terminal.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	waitFor(t, 2*time.Second, "terminal close", terminal.IsClosed)
	_ = waitForClosedMsg(t, msgs, "long", 2*time.Second)
}

func TestSessionTerminalViewNonEmptyAfterOutput(t *testing.T) {
	host := setupMockSSHEnvironment(t)
	terminal, err := NewSessionTerminal(host, newTestSession("view"), 80, 24, nil, slog.Default(), nil)
	if err != nil {
		t.Fatalf("NewSessionTerminal: %v", err)
	}
	t.Cleanup(func() { _ = terminal.Close() })

	waitFor(t, 2*time.Second, "terminal output in view", func() bool {
		plain := stripANSIAndCR(terminal.View())
		return strings.Contains(plain, "view-ready")
	})
}

func TestSessionTerminalWriteInputPath(t *testing.T) {
	host := setupMockSSHEnvironment(t)
	terminal, err := NewSessionTerminal(host, newTestSession("write"), 80, 24, nil, slog.Default(), nil)
	if err != nil {
		t.Fatalf("NewSessionTerminal: %v", err)
	}
	t.Cleanup(func() { _ = terminal.Close() })

	if err := terminal.WriteInput([]byte("hello-terminal\n")); err != nil {
		t.Fatalf("WriteInput: %v", err)
	}

	waitFor(t, 2*time.Second, "echoed input", func() bool {
		plain := stripANSIAndCR(terminal.View())
		return strings.Contains(plain, "echo:hello-terminal")
	})
}

func TestSessionTerminalCloseAndIsClosed(t *testing.T) {
	host := setupMockSSHEnvironment(t)
	msgs := make(chan tea.Msg, 64)

	terminal, err := NewSessionTerminal(host, newTestSession("long"), 80, 24, func(msg tea.Msg) { msgs <- msg }, slog.Default(), nil)
	if err != nil {
		t.Fatalf("NewSessionTerminal: %v", err)
	}

	if terminal.IsClosed() {
		t.Fatal("terminal unexpectedly closed before explicit close")
	}

	if err := terminal.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitFor(t, 2*time.Second, "terminal close", terminal.IsClosed)

	closedMsg := waitForClosedMsg(t, msgs, "long", 2*time.Second)
	if closedMsg.Err != nil {
		t.Fatalf("close error = %v, want nil", closedMsg.Err)
	}

	err = terminal.WriteInput([]byte("x"))
	if !errors.Is(err, ErrSessionTerminalClosed) {
		t.Fatalf("WriteInput after close error = %v, want ErrSessionTerminalClosed", err)
	}
}

func TestSessionTerminalResizeUpdatesDimensions(t *testing.T) {
	host := setupMockSSHEnvironment(t)
	terminal, err := NewSessionTerminal(host, newTestSession("long"), 80, 24, nil, slog.Default(), nil)
	if err != nil {
		t.Fatalf("NewSessionTerminal: %v", err)
	}
	t.Cleanup(func() { _ = terminal.Close() })

	if err := terminal.Resize(120, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	terminal.mu.RLock()
	gotWidth := terminal.width
	gotHeight := terminal.height
	terminal.mu.RUnlock()

	if gotWidth != 120 || gotHeight != 40 {
		t.Fatalf("terminal size = %dx%d, want 120x40", gotWidth, gotHeight)
	}
}

func TestSessionTerminalGracefulSubprocessExit(t *testing.T) {
	host := setupMockSSHEnvironment(t)
	msgs := make(chan tea.Msg, 64)

	terminal, err := NewSessionTerminal(host, newTestSession("graceful"), 80, 24, func(msg tea.Msg) { msgs <- msg }, slog.Default(), nil)
	if err != nil {
		t.Fatalf("NewSessionTerminal: %v", err)
	}

	waitFor(t, 2*time.Second, "graceful process exit", terminal.IsClosed)
	if err := terminal.Err(); err != nil {
		t.Fatalf("terminal Err = %v, want nil", err)
	}

	closedMsg := waitForClosedMsg(t, msgs, "graceful", 2*time.Second)
	if closedMsg.Err != nil {
		t.Fatalf("closed message err = %v, want nil", closedMsg.Err)
	}
}

func TestSessionTerminalCrashExitSetsErr(t *testing.T) {
	host := setupMockSSHEnvironment(t)
	msgs := make(chan tea.Msg, 64)

	terminal, err := NewSessionTerminal(host, newTestSession("crash"), 80, 24, func(msg tea.Msg) { msgs <- msg }, slog.Default(), nil)
	if err != nil {
		t.Fatalf("NewSessionTerminal: %v", err)
	}

	waitFor(t, 2*time.Second, "crash process exit", terminal.IsClosed)
	if err := terminal.Err(); err == nil {
		t.Fatal("terminal Err = nil, want non-nil for non-zero exit")
	}

	closedMsg := waitForClosedMsg(t, msgs, "crash", 2*time.Second)
	if closedMsg.Err == nil {
		t.Fatal("closed message err = nil, want non-nil")
	}

	err = terminal.WriteInput([]byte("after-crash"))
	if !errors.Is(err, ErrSessionTerminalClosed) {
		t.Fatalf("WriteInput after crash error = %v, want wrapped ErrSessionTerminalClosed", err)
	}

	if !strings.Contains(fmt.Sprint(err), "exit status") {
		t.Fatalf("WriteInput after crash should include exit context, got: %v", err)
	}
}

func TestSessionTerminalConstructorValidation(t *testing.T) {
	host := setupMockSSHEnvironment(t)

	if _, err := NewSessionTerminal(model.Host{}, newTestSession("x"), 80, 24, nil, slog.Default(), nil); err == nil {
		t.Fatal("expected error when host name is empty")
	}

	if _, err := NewSessionTerminal(host, model.Session{}, 80, 24, nil, slog.Default(), nil); err == nil {
		t.Fatal("expected error when session id is empty")
	}
}

func TestSessionTerminalViewAndResizeGuards(t *testing.T) {
	var nilTerminal *SessionTerminal
	if got := nilTerminal.View(); got != "" {
		t.Fatalf("nil terminal View = %q, want empty string", got)
	}

	host := setupMockSSHEnvironment(t)
	terminal, err := NewSessionTerminal(host, newTestSession("long"), 80, 24, nil, slog.Default(), nil)
	if err != nil {
		t.Fatalf("NewSessionTerminal: %v", err)
	}
	t.Cleanup(func() { _ = terminal.Close() })

	if err := terminal.Resize(0, 24); err == nil {
		t.Fatal("expected resize error for invalid width")
	}

	if err := terminal.WriteInput(nil); err != nil {
		t.Fatalf("WriteInput(nil) should be noop, got %v", err)
	}
}

func TestSessionTerminalHelperFunctions(t *testing.T) {
	if w, h := normalizeTerminalSize(0, 0); w != defaultTerminalWidth || h != defaultTerminalHeight {
		t.Fatalf("normalize defaults = %dx%d, want %dx%d", w, h, defaultTerminalWidth, defaultTerminalHeight)
	}

	host := model.Host{Name: "dev-1"}
	sessionWithoutDir := model.Session{ID: "sess-a"}
	cmdWithoutDir := buildAttachRemoteCommand(host, sessionWithoutDir)
	if !strings.Contains(cmdWithoutDir, `exec "$OC" -s sess-a`) {
		t.Fatalf("command without directory missing attach segment: %q", cmdWithoutDir)
	}

	sessionWithDir := model.Session{ID: "sess-b", Directory: "/tmp/work"}
	cmdWithDir := buildAttachRemoteCommand(model.Host{Name: "dev-1", OpencodeBin: "my-oc"}, sessionWithDir)
	if !strings.Contains(cmdWithDir, "cd /tmp/work") || !strings.Contains(cmdWithDir, `exec "$OC" -s sess-b`) {
		t.Fatalf("command with directory missing expected segments: %q", cmdWithDir)
	}

	attachArgs := buildAttachSSHArgs(model.Host{Name: "dev-1"}, "echo hi", nil)
	wantAttachArgs := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "ControlMaster=no", "-S", "none", "-t", "-t", "dev-1", "echo hi"}
	if !reflect.DeepEqual(attachArgs, wantAttachArgs) {
		t.Fatalf("attach ssh args = %v, want %v", attachArgs, wantAttachArgs)
	}

	attachArgs = buildAttachSSHArgs(
		model.Host{Name: "dev-1"},
		"echo hi",
		[]string{
			"-o", "ControlMaster=auto",
			"-o", "ControlPersist=60",
			"-o", "ControlPath=~/.ssh/ocr-%n-%C",
			"-o", "StrictHostKeyChecking=accept-new",
		},
	)
	wantAttachArgs = []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ControlMaster=no",
		"-S", "none",
		"-t", "-t",
		"dev-1", "echo hi",
	}
	if !reflect.DeepEqual(attachArgs, wantAttachArgs) {
		t.Fatalf("attach ssh args with opts = %v, want %v", attachArgs, wantAttachArgs)
	}

	if !isPTYClosureError(io.EOF) {
		t.Fatal("expected EOF to be treated as PTY closure error")
	}
	if !isPTYClosureError(os.ErrClosed) {
		t.Fatal("expected os.ErrClosed to be treated as PTY closure error")
	}
	if !isPTYClosureError(syscall.EIO) {
		t.Fatal("expected syscall.EIO to be treated as PTY closure error")
	}

	if !isIgnorableKillError(nil) {
		t.Fatal("expected nil kill error to be ignorable")
	}
}
