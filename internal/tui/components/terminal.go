package components

import (
	"context"
	"errors"
	"fmt"
	"io"
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
)

var ErrSessionTerminalClosed = errors.New("session terminal is closed")

type SessionTerminal struct {
	sessionID string
	emulator  *vt.SafeEmulator
	cmd       *exec.Cmd
	pty       xpty.Pty
	sendMsg   func(tea.Msg)

	mu        sync.RWMutex
	width     int
	height    int
	closed    bool
	err       error
	closeOnce sync.Once
}

func NewSessionTerminal(host model.Host, session model.Session, width, height int, sendMsg func(tea.Msg)) (*SessionTerminal, error) {
	if host.Name == "" {
		return nil, errors.New("host name is required")
	}
	if session.ID == "" {
		return nil, errors.New("session id is required")
	}

	width, height = normalizeTerminalSize(width, height)

	ptyHandle, err := xpty.NewPty(width, height)
	if err != nil {
		return nil, fmt.Errorf("allocate pty: %w", err)
	}

	remoteCmd := buildAttachRemoteCommand(host, session)
	cmd := exec.Command("ssh", "-t", host.Name, remoteCmd)
	if err := ptyHandle.Start(cmd); err != nil {
		_ = ptyHandle.Close()
		return nil, fmt.Errorf("start ssh process in pty: %w", err)
	}

	t := &SessionTerminal{
		sessionID: session.ID,
		emulator:  vt.NewSafeEmulator(width, height),
		cmd:       cmd,
		pty:       ptyHandle,
		sendMsg:   sendMsg,
		width:     width,
		height:    height,
	}

	go t.readLoop()
	go t.waitLoop()

	return t, nil
}

func (t *SessionTerminal) View() string {
	if t == nil || t.emulator == nil {
		return ""
	}
	return t.emulator.Render()
}

func (t *SessionTerminal) WriteInput(data []byte) error {
	if len(data) == 0 {
		return nil
	}
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
	buf := make([]byte, 4096)

	for {
		n, err := t.pty.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			if _, writeErr := t.emulator.Write(chunk); writeErr != nil {
				_ = t.closeWithErr(writeErr)
				return
			}
			t.emit(model.TerminalOutputMsg{SessionID: t.sessionID, Data: chunk})
		}

		if err != nil {
			if isPTYClosureError(err) || t.IsClosed() {
				return
			}
			_ = t.closeWithErr(err)
			return
		}
	}
}

func (t *SessionTerminal) waitLoop() {
	err := xpty.WaitProcess(context.Background(), t.cmd)
	_ = t.closeWithErr(err)
}

func (t *SessionTerminal) closeWithErr(reason error) error {
	if t == nil {
		return nil
	}

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
		t.sendMsg(msg)
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
