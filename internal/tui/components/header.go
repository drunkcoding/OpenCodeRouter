package components

import (
	"fmt"
	"strings"
	"time"

	"opencoderouter/internal/tui/theme"

	textinput "charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// FleetStats summarizes discovered infrastructure state.
type FleetStats struct {
	HostsTotal    int
	HostsOnline   int
	SessionsTotal int
}

// HeaderBar renders search input, refresh countdown, and fleet stats.
type HeaderBar struct {
	searchInput   textinput.Model
	stats         FleetStats
	nextRefresh   time.Time
	refreshPeriod time.Duration
	width         int
	theme         theme.Theme
}

// NewHeaderBar creates a header component.
func NewHeaderBar(th theme.Theme, refreshPeriod time.Duration) HeaderBar {
	input := textinput.New()
	input.Prompt = "search> "
	input.Placeholder = "host, project, session"
	input.SetWidth(30)

	return HeaderBar{
		searchInput:   input,
		refreshPeriod: refreshPeriod,
		theme:         th,
	}
}

// Init returns startup commands required by the input element.
func (h HeaderBar) Init() tea.Cmd {
	return textinput.Blink
}

// SetSize sets the available width for header layout.
func (h *HeaderBar) SetSize(width int) {
	h.width = width
}

// SetStats updates the current fleet counters.
func (h *HeaderBar) SetStats(stats FleetStats) {
	h.stats = stats
}

// SetRefreshDeadline updates the countdown target timestamp.
func (h *HeaderBar) SetRefreshDeadline(next time.Time) {
	h.nextRefresh = next
}

// FocusSearch places cursor focus in the search input.
func (h *HeaderBar) FocusSearch() {
	h.searchInput.Focus()
}

// BlurSearch removes cursor focus from the search input.
func (h *HeaderBar) BlurSearch() {
	h.searchInput.Blur()
}

// SearchQuery returns current search input value.
func (h HeaderBar) SearchQuery() string {
	return strings.TrimSpace(h.searchInput.Value())
}

// SearchFocused returns true when search input has cursor focus.
func (h HeaderBar) SearchFocused() bool {
	return h.searchInput.Focused()
}

// SetSearchQuery sets the search value.
func (h *HeaderBar) SetSearchQuery(value string) {
	h.searchInput.SetValue(value)
}

// Update updates search input state.
func (h HeaderBar) Update(msg tea.Msg) (HeaderBar, tea.Cmd) {
	input, cmd := h.searchInput.Update(msg)
	h.searchInput = input

	if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
		switch keyMsg.String() {
		case "esc":
			h.searchInput.Blur()
		case "ctrl+u":
			h.searchInput.SetValue("")
		}
	}

	return h, cmd
}

// View renders the header line.
func (h HeaderBar) View(now time.Time, spinnerFrame string) string {
	title := h.theme.HeaderTitle.Render("OpenCode Router")
	if spinnerFrame != "" {
		title = lipgloss.JoinHorizontal(lipgloss.Center, spinnerFrame+" ", title)
	}

	search := h.theme.HeaderSearch.Render(h.searchInput.View())
	stats := h.theme.HeaderStat.Render(
		fmt.Sprintf("hosts %d/%d • sessions %d • refresh %s", h.stats.HostsOnline, h.stats.HostsTotal, h.stats.SessionsTotal, h.countdown(now)),
	)

	content := lipgloss.JoinHorizontal(lipgloss.Center, title, "  ", search, "  ", stats)
	if h.width > 0 {
		content = lipgloss.NewStyle().Width(h.width).Render(content)
	}
	return h.theme.Header.Render(content)
}

func (h HeaderBar) countdown(now time.Time) string {
	if h.nextRefresh.IsZero() {
		if h.refreshPeriod <= 0 {
			return "--"
		}
		return h.refreshPeriod.Round(time.Second).String()
	}
	remaining := time.Until(h.nextRefresh)
	if !now.IsZero() {
		remaining = h.nextRefresh.Sub(now)
	}
	if remaining < 0 {
		remaining = 0
	}
	return remaining.Round(time.Second).String()
}
