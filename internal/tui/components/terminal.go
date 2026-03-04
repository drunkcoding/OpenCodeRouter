package components

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"opencoderouter/internal/tui/model"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"
	"github.com/charmbracelet/x/xpty"
)

const (
	defaultTerminalWidth  = 80
	defaultTerminalHeight = 24
	// Prevent interactive SSH prompts that cause TUI black-screen hang when auth/network fails.
	attachSSHBatchMode = "BatchMode=yes"
	attachSSHTimeout   = "ConnectTimeout=10"
)

var ErrSessionTerminalClosed = errors.New("session terminal is closed")

type SessionTerminal struct {
	sessionID       string
	emulator        *vt.SafeEmulator
	cmd             *exec.Cmd
	pty             xpty.Pty
	sendMsg         func(tea.Msg)
	logger          *slog.Logger
	viewEmptyLogged bool

	mu        sync.RWMutex
	width     int
	height    int
	closed    bool
	err       error
	closeOnce sync.Once
}

func NewSessionTerminal(host model.Host, session model.Session, width, height int, sendMsg func(tea.Msg), logger *slog.Logger) (*SessionTerminal, error) {
	if host.Name == "" {
		return nil, errors.New("host name is required")
	}
	if session.ID == "" {
		return nil, errors.New("session id is required")
	}

	if logger == nil {
		logger = slog.Default()
	}

	width, height = normalizeTerminalSize(width, height)

	logger.Info("terminal creating", "host", host.Name, "session_id", session.ID, "width", width, "height", height)

	ptyHandle, err := xpty.NewPty(width, height)
	if err != nil {
		logger.Error("terminal pty allocation failed", "error", err)
		return nil, fmt.Errorf("allocate pty: %w", err)
	}
	logger.Debug("terminal pty allocated", "session_id", session.ID)

	remoteCmd := buildAttachRemoteCommand(host, session)
	cmd := exec.Command("ssh", buildAttachSSHArgs(host, remoteCmd)...)
	logger.Debug("terminal ssh command", "session_id", session.ID, "args", cmd.Args)

	// Set controlling terminal so SSH can properly allocate a remote PTY.
	// Without this, SSH has no controlling terminal and silently sends 0 bytes.
	// Ctty is an index into the child fd array (0=stdin), not a raw fd number.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0,
	}

	if err := ptyHandle.Start(cmd); err != nil {
		_ = ptyHandle.Close()
		logger.Error("terminal ssh start failed", "session_id", session.ID, "error", err)
		return nil, fmt.Errorf("start ssh process in pty: %w", err)
	}

	// Close slave fd in parent so Read() gets EOF when child exits
	// instead of blocking forever.
	if upty, ok := ptyHandle.(*xpty.UnixPty); ok {
		_ = upty.Slave().Close()
	}

	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	logger.Info("terminal ssh started", "session_id", session.ID, "pid", pid)

	t := &SessionTerminal{
		sessionID: session.ID,
		emulator:  vt.NewSafeEmulator(width, height),
		cmd:       cmd,
		pty:       ptyHandle,
		sendMsg:   sendMsg,
		logger:    logger,
		width:     width,
		height:    height,
	}

	logger.Debug("terminal goroutines launching", "session_id", session.ID)
	go t.readLoop()
	go t.waitLoop()

	return t, nil
}

func (t *SessionTerminal) View() string {
	if t == nil || t.emulator == nil {
		return ""
	}
	rendered := t.emulator.Render()
	if rendered == "" && !t.viewEmptyLogged {
		t.logger.Warn("terminal view empty", "session_id", t.sessionID)
		t.viewEmptyLogged = true
	}
	return rendered
}

func (t *SessionTerminal) WriteInput(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	t.logger.Debug("terminal writeInput", "session_id", t.sessionID, "bytes", len(data))
	if t.IsClosed() {
		return t.closedError()
	}

	if _, err := t.pty.Write(data); err != nil {
		if isPTYClosureError(err) {
			_ = t.closeWithErr(nil)
			return t.closedError()
		}
		_ = t.closeWithErr(err)
		return err
	}

	return nil
}

func (t *SessionTerminal) Resize(width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("invalid terminal size %dx%d", width, height)
	}
	if t.IsClosed() {
		return t.closedError()
	}

	if err := t.pty.Resize(width, height); err != nil {
		if isPTYClosureError(err) {
			_ = t.closeWithErr(nil)
			return t.closedError()
		}
		_ = t.closeWithErr(err)
		return err
	}

	t.emulator.Resize(width, height)

	t.mu.Lock()
	t.width = width
	t.height = height
	t.mu.Unlock()

	return nil
}

func (t *SessionTerminal) Close() error {
	return t.closeWithErr(nil)
}

func (t *SessionTerminal) IsClosed() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.closed
}

func (t *SessionTerminal) Err() error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.err
}

func (t *SessionTerminal) readLoop() {
	t.logger.Debug("terminal readLoop started", "session_id", t.sessionID)
	buf := make([]byte, 4096)
	defer t.logger.Debug("terminal readLoop exiting", "session_id", t.sessionID)

	for {
		n, err := t.pty.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			if _, writeErr := t.emulator.Write(chunk); writeErr != nil {
				t.logger.Error("terminal emulator write error", "session_id", t.sessionID, "error", writeErr)
				_ = t.closeWithErr(writeErr)
				return
			}
			t.logger.Debug("terminal readLoop chunk", "session_id", t.sessionID, "bytes", n, "preview", previewBytes(chunk, 200))
			t.emit(model.TerminalOutputMsg{SessionID: t.sessionID, Data: chunk})
		}

		if err != nil {
			if isPTYClosureError(err) || t.IsClosed() {
				t.logger.Debug("terminal readLoop closure", "session_id", t.sessionID, "reason", "pty_closed")
				return
			}
			t.logger.Error("terminal readLoop error", "session_id", t.sessionID, "error", err)
			_ = t.closeWithErr(err)
			return
		}
	}
}

func (t *SessionTerminal) waitLoop() {
	t.logger.Debug("terminal waitLoop started", "session_id", t.sessionID)
	err := xpty.WaitProcess(context.Background(), t.cmd)
	t.logger.Info("terminal process exited", "session_id", t.sessionID, "error", err)
	_ = t.closeWithErr(err)
}

func (t *SessionTerminal) closeWithErr(reason error) error {
	if t == nil {
		return nil
	}

	t.logger.Info("terminal closing", "session_id", t.sessionID, "reason", reason)

	var closeErr error

	t.closeOnce.Do(func() {
		if t.pty != nil {
			if err := t.pty.Close(); err != nil && !isPTYClosureError(err) {
				closeErr = errors.Join(closeErr, err)
			}
		}

		if t.cmd != nil && t.cmd.Process != nil {
			if err := t.cmd.Process.Kill(); !isIgnorableKillError(err) {
				closeErr = errors.Join(closeErr, err)
			}
		}

		finalErr := errors.Join(reason, closeErr)

		t.mu.Lock()
		t.closed = true
		t.err = finalErr
		t.mu.Unlock()

		t.logger.Info("terminal closed", "session_id", t.sessionID, "error", finalErr)
		t.emit(model.TerminalClosedMsg{SessionID: t.sessionID, Err: finalErr})
	})

	return closeErr
}

func (t *SessionTerminal) closedError() error {
	if err := t.Err(); err != nil {
		return errors.Join(ErrSessionTerminalClosed, err)
	}
	return ErrSessionTerminalClosed
}

func (t *SessionTerminal) emit(msg tea.Msg) {
	if t.sendMsg != nil {
		go t.sendMsg(msg)
	}
}

func normalizeTerminalSize(width, height int) (int, int) {
	if width <= 0 {
		width = defaultTerminalWidth
	}
	if height <= 0 {
		height = defaultTerminalHeight
	}
	return width, height
}

func buildAttachRemoteCommand(host model.Host, session model.Session) string {
	bin := host.OpencodeBin
	if bin == "" {
		bin = "opencode"
	}

	if session.Directory != "" {
		return fmt.Sprintf(
			`OC=$(command -v %s 2>/dev/null || echo "$HOME/.opencode/bin/%s"); cd %s && exec "$OC" -s %s`,
			bin, bin, session.Directory, session.ID,
		)
	}

	return fmt.Sprintf(
		`OC=$(command -v %s 2>/dev/null || echo "$HOME/.opencode/bin/%s"); exec "$OC" -s %s`,
		bin, bin, session.ID,
	)
}

func buildAttachSSHArgs(host model.Host, remoteCmd string) []string {
	return []string{"-o", attachSSHBatchMode, "-o", attachSSHTimeout, "-t", "-t", host.Name, remoteCmd}
}

func isPTYClosureError(err error) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, os.ErrClosed) ||
		errors.Is(err, syscall.EIO)
}

func isIgnorableKillError(err error) bool {
	return err == nil ||
		errors.Is(err, os.ErrProcessDone) ||
		errors.Is(err, syscall.ESRCH)
}

func previewBytes(data []byte, maxLen int) string {
	if len(data) > maxLen {
		return string(data[:maxLen])
	}
	return string(data)
}
