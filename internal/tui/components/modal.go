package components

import (
	"fmt"
	"strings"

	"opencoderouter/internal/tui/theme"

	textinput "charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// ModalType identifies the active modal overlay.
type ModalType string

const (
	// ModalTypeNone means no active modal.
	ModalTypeNone ModalType = "none"
	// ModalTypeNewSession prompts for session creation fields.
	ModalTypeNewSession ModalType = "new_session"
	// ModalTypeConfirmKill prompts for kill confirmation.
	ModalTypeConfirmKill ModalType = "confirm_kill"
	// ModalTypeErrorDetail displays detailed errors.
	ModalTypeErrorDetail ModalType = "error_detail"
	// ModalTypeAuthBootstrap shows SSH ControlMaster bootstrap command.
	ModalTypeAuthBootstrap ModalType = "auth_bootstrap"
)

// ModalLayer renders and updates overlay dialogs.
type ModalLayer struct {
	active      bool
	modalType   ModalType
	title       string
	body        string
	confirmText string
	input       textinput.Model
	width       int
	height      int
	theme       theme.Theme
}

// NewModalLayer creates an empty modal manager.
func NewModalLayer(th theme.Theme) ModalLayer {
	input := textinput.New()
	input.Prompt = "name> "
	input.Placeholder = "new session title"
	input.SetWidth(36)
	return ModalLayer{modalType: ModalTypeNone, input: input, theme: th}
}

// SetSize sets viewport dimensions for modal centering.
func (m *ModalLayer) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// Active returns true when a modal is visible.
func (m ModalLayer) Active() bool {
	return m.active
}

// Type returns the current modal type.
func (m ModalLayer) Type() ModalType {
	return m.modalType
}

// OpenNewSession opens the create-session modal.
func (m *ModalLayer) OpenNewSession() {
	m.active = true
	m.modalType = ModalTypeNewSession
	m.title = "Create Session"
	m.body = "Provide a title and press enter to submit."
	m.confirmText = "Enter create • Esc cancel"
	m.input.SetValue("")
	m.input.Focus()
}

// OpenConfirmKill opens a destructive confirmation modal.
func (m *ModalLayer) OpenConfirmKill(sessionID string) {
	m.active = true
	m.modalType = ModalTypeConfirmKill
	m.title = "Kill Session"
	m.body = fmt.Sprintf("Kill session %s?", sessionID)
	m.confirmText = "y confirm • n cancel"
	m.input.Blur()
}

// OpenError opens an error details modal.
func (m *ModalLayer) OpenError(err error) {
	m.active = true
	m.modalType = ModalTypeErrorDetail
	m.title = "Error"
	if err == nil {
		m.body = "Unknown error"
	} else {
		m.body = err.Error()
	}
	m.confirmText = "Esc close"
	m.input.Blur()
}

// OpenAuthBootstrap opens a modal showing the SSH ControlMaster bootstrap command.
// The user copies and runs this command to establish a persistent socket.
func (m *ModalLayer) OpenAuthBootstrap(hostName, bootstrapCmd string) {
	m.active = true
	m.modalType = ModalTypeAuthBootstrap
	m.title = fmt.Sprintf("Authenticate: %s", hostName)
	m.body = fmt.Sprintf(
		"This host requires password authentication.\n"+
			"Run the following command in another terminal:\n\n"+
			"  %s\n\n"+
			"Then press 'r' to refresh.",
		bootstrapCmd,
	)
	m.confirmText = "Esc close"
	m.input.Blur()
}

// Close dismisses any active modal.
func (m *ModalLayer) Close() {
	m.active = false
	m.modalType = ModalTypeNone
	m.title = ""
	m.body = ""
	m.confirmText = ""
	m.input.Blur()
}

// InputValue returns current modal input value.
func (m ModalLayer) InputValue() string {
	return strings.TrimSpace(m.input.Value())
}

// Given a key message, when Update runs, then modal state transitions are applied.
func (m ModalLayer) Update(msg tea.Msg) (ModalLayer, tea.Cmd) {
	if !m.active {
		return m, nil
	}

	if m.modalType == ModalTypeNewSession {
		input, cmd := m.input.Update(msg)
		m.input = input
		if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
			switch keyMsg.String() {
			case "esc":
				m.Close()
				return m, nil
			case "enter":
				// TODO: dispatch create-session command through service layer.
				m.Close()
				return m, nil
			}
		}
		return m, cmd
	}

	if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
		switch m.modalType {
		case ModalTypeConfirmKill:
			switch keyMsg.String() {
			case "y":
				// TODO: dispatch kill-session command through service layer.
				m.Close()
			case "n", "esc":
				m.Close()
			}
		case ModalTypeErrorDetail, ModalTypeAuthBootstrap:
			if keyMsg.String() == "esc" || keyMsg.String() == "enter" {
				m.Close()
			}
		}
	}

	return m, nil
}

// View renders the active modal overlay.
func (m ModalLayer) View() string {
	if !m.active {
		return ""
	}

	contentLines := []string{
		m.theme.ModalTitle.Render(m.title),
		m.theme.ModalBody.Render(m.body),
	}
	if m.modalType == ModalTypeNewSession {
		contentLines = append(contentLines, "", m.input.View())
	}
	if m.confirmText != "" {
		contentLines = append(contentLines, "", m.theme.Muted.Render(m.confirmText))
	}
	box := m.theme.ModalBox.Render(strings.Join(contentLines, "\n"))

	if m.width <= 0 || m.height <= 0 {
		return box
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}
