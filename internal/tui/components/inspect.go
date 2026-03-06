package components

import (
	"fmt"
	"strings"
	"time"

	"opencoderouter/internal/model"
	"opencoderouter/internal/tui/theme"

	lipgloss "charm.land/lipgloss/v2"
)

// InspectPanel renders detailed information for the selected session.
type InspectPanel struct {
	host     *model.Host
	project  *model.Project
	session  *model.Session
	width    int
	height   int
	theme    theme.Theme
	emptyMsg string
}

// NewInspectPanel creates an inspect panel with default placeholder text.
func NewInspectPanel(th theme.Theme) InspectPanel {
	return InspectPanel{
		theme:    th,
		emptyMsg: "Select a session to inspect details",
	}
}

// SetSize sets panel dimensions for layout.
func (p *InspectPanel) SetSize(width, height int) {
	p.width = width
	p.height = height
}

// SetSelection updates the selected host/project/session.
func (p *InspectPanel) SetSelection(host model.Host, project model.Project, session model.Session) {
	h := host
	pr := project
	s := session
	p.host = &h
	p.project = &pr
	p.session = &s
}

// ClearSelection clears currently inspected data.
func (p *InspectPanel) ClearSelection() {
	p.host = nil
	p.project = nil
	p.session = nil
}

// View renders the inspect panel.
func (p InspectPanel) View() string {
	if p.session == nil || p.project == nil || p.host == nil {
		empty := p.theme.TreeMuted.Render(p.emptyMsg)
		if p.width > 0 {
			empty = lipgloss.NewStyle().Width(maxInt(0, p.width-2)).Render(empty)
		}
		return p.theme.Inspect.Render(empty)
	}

	lines := []string{
		p.theme.InspectTitle.Render("Session Inspect"),
		fmt.Sprintf("Host: %s (%s)", p.host.Label, p.host.Name),
		fmt.Sprintf("Project: %s", p.project.Name),
		fmt.Sprintf("Session ID: %s", p.session.ID),
		fmt.Sprintf("Title: %s", nonEmpty(p.session.Title, "(untitled)")),
		fmt.Sprintf("Status: %s", p.session.Status),
		fmt.Sprintf("Activity: %s", p.session.Activity),
		fmt.Sprintf("Last Activity: %s", formatTime(p.session.LastActivity)),
		fmt.Sprintf("Messages: %d", p.session.MessageCount),
		fmt.Sprintf("Agents: %s", nonEmpty(strings.Join(p.session.Agents, ", "), "(none)")),
		"",
		"Actions: Enter attach • n new session • d delete • g clone",
	}

	body := strings.Join(lines, "\n")
	if p.width > 0 {
		body = lipgloss.NewStyle().Width(maxInt(0, p.width-2)).Render(body)
	}
	return p.theme.Inspect.Render(body)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.Format(time.RFC3339)
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
