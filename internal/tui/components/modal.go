package components

import (
	"fmt"
	"path/filepath"
	"strings"

	tuimodel "opencoderouter/internal/tui/model"
	"opencoderouter/internal/tui/theme"

	textinput "charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// ModalType identifies the active modal overlay.
type ModalType string

const (
	ModalTypeNone          ModalType = "none"
	ModalTypeNewSession    ModalType = "new_session"
	ModalTypeNewDirectory  ModalType = "new_directory"
	ModalTypeGitClone      ModalType = "git_clone"
	ModalTypeConfirmKill   ModalType = "confirm_kill"
	ModalTypeConfirmReload ModalType = "confirm_reload"
	ModalTypeErrorDetail   ModalType = "error_detail"
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

	hostName  string
	directory string
	sessionID string
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

// OpenNewSession opens a confirmation modal to create a session in an existing project.
func (m *ModalLayer) OpenNewSession(hostName, projectName, directory string) {
	m.active = true
	m.modalType = ModalTypeNewSession
	m.title = "Create Session"
	m.body = fmt.Sprintf("Create a new session on %s in %s?\n%s", hostName, projectName, directory)
	m.confirmText = "Enter create • Esc cancel"
	m.hostName = hostName
	m.directory = directory
	m.input.Blur()
}

// OpenNewDirectory opens a modal to enter a directory path for session creation.
func (m *ModalLayer) OpenNewDirectory(hostName string) {
	m.active = true
	m.modalType = ModalTypeNewDirectory
	m.title = "New Session — Custom Directory"
	m.body = fmt.Sprintf("Enter directory path on %s:", hostName)
	m.confirmText = "Enter create • Esc cancel"
	m.hostName = hostName
	m.directory = ""
	m.input.Prompt = "dir> "
	m.input.Placeholder = "/path/to/project"
	m.input.SetValue("")
	m.input.Focus()
}

// OpenGitClone opens a modal to enter a git repo URL for cloning + session creation.
func (m *ModalLayer) OpenGitClone(hostName string) {
	m.active = true
	m.modalType = ModalTypeGitClone
	m.title = "Clone & Create Session"
	m.body = fmt.Sprintf("Enter git repository URL to clone on %s:", hostName)
	m.confirmText = "Enter clone • Esc cancel"
	m.hostName = hostName
	m.directory = ""
	m.input.Prompt = "url> "
	m.input.Placeholder = "https://github.com/org/repo.git"
	m.input.SetValue("")
	m.input.Focus()
}

// OpenConfirmKill opens a destructive confirmation modal.
func (m *ModalLayer) OpenConfirmKill(hostName, sessionID, directory string) {
	m.active = true
	m.modalType = ModalTypeConfirmKill
	m.title = "Delete Session"
	m.body = fmt.Sprintf("Delete session %s on %s?\nSave session context before deleting?", sessionID, hostName)
	m.confirmText = "y save + delete • n delete only • Esc cancel"
	m.hostName = hostName
	m.sessionID = sessionID
	m.directory = directory
	m.input.Blur()
}

func (m *ModalLayer) OpenConfirmReload(hostName, directory string) {
	projectName := filepath.Base(filepath.Clean(strings.TrimSpace(directory)))
	if projectName == "." || projectName == string(filepath.Separator) || projectName == "" {
		projectName = directory
	}

	m.active = true
	m.modalType = ModalTypeConfirmReload
	m.title = "Reload OpenCode"
	m.body = fmt.Sprintf("Reload OpenCode for %s on %s?\n%s", projectName, hostName, directory)
	m.confirmText = "y confirm • n cancel"
	m.hostName = hostName
	m.directory = directory
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

// OpenAuthBootstrap opens a modal showing SSH ControlMaster bootstrap commands.
// For multi-hop scenarios, it shows ordered commands for each hop that needs auth.
func (m *ModalLayer) OpenAuthBootstrap(hostName string, bootstrapCmds []string) {
	m.active = true
	m.modalType = ModalTypeAuthBootstrap
	m.title = fmt.Sprintf("Authenticate: %s", hostName)

	if len(bootstrapCmds) == 0 {
		m.body = "No authentication commands needed."
		m.confirmText = "Esc close"
		m.input.Blur()
		return
	}

	var b strings.Builder
	if len(bootstrapCmds) == 1 {
		b.WriteString("This host requires password authentication.\n")
		b.WriteString("Run the following command in another terminal:\n\n")
		b.WriteString(fmt.Sprintf("  %s\n\n", bootstrapCmds[0]))
	} else {
		b.WriteString("Multi-hop authentication required.\n")
		b.WriteString("Run these commands in order:\n\n")
		for i, cmd := range bootstrapCmds {
			b.WriteString(fmt.Sprintf("  %d) %s\n", i+1, cmd))
		}
		b.WriteString("\n")
	}
	b.WriteString("Then press 'r' to refresh.")

	m.body = b.String()
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
	m.hostName = ""
	m.directory = ""
	m.sessionID = ""
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

	switch m.modalType {
	case ModalTypeNewSession:
		if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
			switch keyMsg.String() {
			case "esc":
				m.Close()
				return m, nil
			case "enter":
				hostName := m.hostName
				directory := m.directory
				m.Close()
				return m, func() tea.Msg {
					return tuimodel.ModalConfirmCreateMsg{
						HostName:  hostName,
						Directory: directory,
					}
				}
			}
		}
		return m, nil

	case ModalTypeNewDirectory:
		input, cmd := m.input.Update(msg)
		m.input = input
		if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
			switch keyMsg.String() {
			case "esc":
				m.Close()
				return m, nil
			case "enter":
				dir := strings.TrimSpace(m.input.Value())
				if dir == "" {
					return m, cmd
				}
				hostName := m.hostName
				m.Close()
				return m, func() tea.Msg {
					return tuimodel.ModalConfirmNewDirMsg{
						HostName:  hostName,
						Directory: dir,
					}
				}
			}
		}
		return m, cmd

	case ModalTypeGitClone:
		input, cmd := m.input.Update(msg)
		m.input = input
		if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
			switch keyMsg.String() {
			case "esc":
				m.Close()
				return m, nil
			case "enter":
				gitURL := strings.TrimSpace(m.input.Value())
				if gitURL == "" {
					return m, cmd
				}
				hostName := m.hostName
				m.Close()
				return m, func() tea.Msg {
					return tuimodel.ModalConfirmGitCloneMsg{
						HostName: hostName,
						GitURL:   gitURL,
					}
				}
			}
		}
		return m, cmd

	case ModalTypeConfirmKill:
		if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
			switch keyMsg.String() {
			case "y":
				hostName := m.hostName
				sessionID := m.sessionID
				directory := m.directory
				m.Close()
				return m, func() tea.Msg {
					return tuimodel.ModalConfirmKillMsg{
						HostName:    hostName,
						SessionID:   sessionID,
						Directory:   directory,
						SaveContext: true,
					}
				}
			case "n":
				hostName := m.hostName
				sessionID := m.sessionID
				directory := m.directory
				m.Close()
				return m, func() tea.Msg {
					return tuimodel.ModalConfirmKillMsg{
						HostName:    hostName,
						SessionID:   sessionID,
						Directory:   directory,
						SaveContext: false,
					}
				}
			case "esc":
				m.Close()
			}
		}

	case ModalTypeConfirmReload:
		if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
			switch keyMsg.String() {
			case "y":
				hostName := m.hostName
				directory := m.directory
				m.Close()
				return m, func() tea.Msg {
					return tuimodel.ModalConfirmReloadMsg{
						HostName:  hostName,
						Directory: directory,
					}
				}
			case "n", "esc":
				m.Close()
			}
		}

	case ModalTypeErrorDetail, ModalTypeAuthBootstrap:
		if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
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
	if m.modalType == ModalTypeNewDirectory || m.modalType == ModalTypeGitClone {
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
