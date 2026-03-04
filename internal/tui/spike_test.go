package tui

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"
	"github.com/charmbracelet/x/xpty"
)

var ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// vtSpikeModel keeps this spike fully isolated from production models while
// proving that Bubble Tea View can display vt.Render() output.
type vtSpikeModel struct {
	emulator *vt.SafeEmulator
}

var _ tea.Model = (*vtSpikeModel)(nil)

func (m *vtSpikeModel) Init() tea.Cmd {
	return nil
}

func (m *vtSpikeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m, nil
}

func (m *vtSpikeModel) View() tea.View {
	return tea.NewView(m.emulator.Render())
}

func stripANSI(input string) string {
	withoutCSI := ansiEscapePattern.ReplaceAllString(input, "")
	return strings.ReplaceAll(withoutCSI, "\r", "")
}

func isCtrlRightBracket(msg tea.KeyPressMsg) bool {
	key := msg.Key()
	if key.Code == rune(0x1d) {
		return true
	}

	if key.Code == ']' && (key.Mod&tea.ModCtrl) != 0 {
		return true
	}

	return msg.String() == "ctrl+]"
}

func TestVTSpike(t *testing.T) {
	t.Run("safe emulator renders ANSI content", func(t *testing.T) {
		emulator := vt.NewSafeEmulator(80, 24)
		input := "\x1b[31mHello\x1b[0m World"

		written, err := emulator.Write([]byte(input))
		if err != nil {
			t.Fatalf("emulator write failed: %v", err)
		}
		if written != len(input) {
			t.Fatalf("unexpected write length: got %d want %d", written, len(input))
		}

		rendered := emulator.Render()
		if strings.TrimSpace(rendered) == "" {
			t.Fatal("Render returned empty output")
		}

		plain := stripANSI(rendered)
		if !strings.Contains(plain, "Hello World") {
			t.Fatalf("render output missing expected text; got %q", rendered)
		}
	})

	t.Run("Bubble Tea View displays VT Render output", func(t *testing.T) {
		emulator := vt.NewSafeEmulator(80, 24)
		_, err := emulator.Write([]byte("\x1b[32mBubbleTea VT\x1b[0m"))
		if err != nil {
			t.Fatalf("emulator write failed: %v", err)
		}

		model := &vtSpikeModel{emulator: emulator}
		view := model.View()
		if strings.TrimSpace(view.Content) == "" {
			t.Fatal("Bubble Tea view content is empty")
		}

		plain := stripANSI(view.Content)
		if !strings.Contains(plain, "BubbleTea VT") {
			t.Fatalf("Bubble Tea view did not expose VT output; got %q", view.Content)
		}
	})

	t.Run("Ctrl+] detection is reliable", func(t *testing.T) {
		const ctrlRightBracketASCII = rune(0x1d)

		asciiMsg := tea.KeyPressMsg{Code: ctrlRightBracketASCII}
		if !isCtrlRightBracket(asciiMsg) {
			t.Fatalf("ASCII 0x1d was not detected as Ctrl+], msg=%q code=%U", asciiMsg.String(), asciiMsg.Key().Code)
		}

		comboMsg := tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl}
		if !isCtrlRightBracket(comboMsg) {
			t.Fatalf("ctrl modifier + ] was not detected, msg=%q code=%U mod=%v", comboMsg.String(), comboMsg.Key().Code, comboMsg.Key().Mod)
		}

		if comboMsg.String() != "ctrl+]" {
			t.Fatalf("unexpected keystroke representation for ctrl+]: %q", comboMsg.String())
		}

		nonDetachMsg := tea.KeyPressMsg{Code: ']'}
		if isCtrlRightBracket(nonDetachMsg) {
			t.Fatalf("plain ] should not be detected as detach key, msg=%q", nonDetachMsg.String())
		}
	})

	t.Run("xpty subprocess output flows into VT Render", func(t *testing.T) {
		pty, err := xpty.NewPty(80, 24)
		if err != nil {
			t.Fatalf("failed to allocate xpty: %v", err)
		}
		defer func() {
			_ = pty.Close()
		}()

		cmd := exec.Command("echo", "hello")
		if err := pty.Start(cmd); err != nil {
			t.Fatalf("failed to start subprocess on pty: %v", err)
		}

		type readResult struct {
			data []byte
			err  error
		}

		readCh := make(chan readResult, 1)
		go func() {
			buf := make([]byte, 512)
			n, readErr := pty.Read(buf)
			payload := make([]byte, 0, max(n, 0))
			if n > 0 {
				payload = append(payload, buf[:n]...)
			}
			readCh <- readResult{data: payload, err: readErr}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := xpty.WaitProcess(ctx, cmd); err != nil {
			t.Fatalf("pty subprocess did not exit cleanly: %v", err)
		}

		var output []byte
		select {
		case result := <-readCh:
			if result.err != nil && !errors.Is(result.err, io.EOF) {
				t.Fatalf("pty read failed: %v", result.err)
			}
			output = result.data
		case <-time.After(2 * time.Second):
			t.Fatal("timed out reading xpty output")
		}

		if len(output) == 0 {
			t.Fatal("pty produced no output")
		}

		emulator := vt.NewSafeEmulator(80, 24)
		written, err := emulator.Write(output)
		if err != nil {
			t.Fatalf("failed to write pty output into emulator: %v", err)
		}
		if written != len(output) {
			t.Fatalf("partial emulator write: got %d want %d", written, len(output))
		}

		rendered := emulator.Render()
		if strings.TrimSpace(rendered) == "" {
			t.Fatal("Render returned empty output for PTY data")
		}

		plain := strings.ToLower(stripANSI(rendered))
		if !strings.Contains(plain, "hello") {
			t.Fatalf("render output missing subprocess text 'hello'; got %q", rendered)
		}
	})
}
