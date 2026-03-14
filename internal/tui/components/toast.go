package components

import (
	"fmt"
	"strings"
	"time"

	tuimodel "opencoderouter/internal/tui/model"
	"opencoderouter/internal/tui/theme"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

type ToastSeverity string

const (
	ToastSeverityInfo    ToastSeverity = "info"
	ToastSeverityWarning ToastSeverity = "warning"
	ToastSeverityError   ToastSeverity = "error"
)

type InlineToast struct {
	theme        theme.Theme
	width        int
	height       int
	message      string
	visible      bool
	severity     ToastSeverity
	dismissAfter time.Duration
	token        uint64
}

func NewInlineToast(th theme.Theme) InlineToast {
	return InlineToast{theme: th}
}

func (t *InlineToast) SetSize(width int) {
	t.width = width
	t.height = 1
}

func (t *InlineToast) Show(message string, severity ToastSeverity, timeout time.Duration) tea.Cmd {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		trimmed = "Unknown error"
	}

	t.message = trimmed
	t.visible = true
	t.severity = severity
	t.dismissAfter = timeout
	t.token++
	currentToken := t.token

	if timeout <= 0 {
		return nil
	}

	return tea.Tick(timeout, func(_ time.Time) tea.Msg {
		return tuimodel.ToastExpiredMsg{Token: currentToken}
	})
}

func (t *InlineToast) Hide() {
	t.visible = false
	t.message = ""
	t.dismissAfter = 0
}

func (t InlineToast) Visible() bool {
	return t.visible
}

func (t InlineToast) Update(msg tea.Msg) (InlineToast, tea.Cmd) {
	typed, ok := msg.(tuimodel.ToastExpiredMsg)
	if !ok {
		return t, nil
	}

	if !t.visible || typed.Token != t.token {
		return t, nil
	}

	t.Hide()
	return t, nil
}

func (t InlineToast) View() string {
	if !t.visible {
		return ""
	}

	label := "info"
	bodyStyle := t.theme.Muted
	switch t.severity {
	case ToastSeverityError:
		label = "error"
		bodyStyle = t.theme.Danger
	case ToastSeverityWarning:
		label = "warning"
		bodyStyle = t.theme.Accent
	}

	line := lipgloss.JoinHorizontal(
		lipgloss.Top,
		bodyStyle.Render(label+":"),
		" ",
		bodyStyle.Render(t.message),
	)

	if t.dismissAfter > 0 {
		line = lipgloss.JoinHorizontal(
			lipgloss.Top,
			line,
			"  ",
			t.theme.Muted.Render(fmt.Sprintf("auto-dismisses in %s", t.dismissAfter.Round(time.Second))),
		)
	}

	if t.width > 0 {
		line = lipgloss.NewStyle().Width(t.width).Render(line)
	}

	return t.theme.Footer.Render(line)
}
