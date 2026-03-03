package components

import (
	"fmt"
	"strings"

	"opencoderouter/internal/tui/keys"
	"opencoderouter/internal/tui/theme"

	lipgloss "charm.land/lipgloss/v2"
)

// FooterContext controls context-sensitive hints in the footer.
type FooterContext struct {
	ModalOpen   bool
	SearchFocus bool
}

// FooterHelpBar renders keybinding hints.
type FooterHelpBar struct {
	keyMap  keys.KeyMap
	context FooterContext
	width   int
	theme   theme.Theme
}

// NewFooterHelpBar creates a footer help component.
func NewFooterHelpBar(keyMap keys.KeyMap, th theme.Theme) FooterHelpBar {
	return FooterHelpBar{keyMap: keyMap, theme: th}
}

// SetSize sets available width.
func (f *FooterHelpBar) SetSize(width int) {
	f.width = width
}

// SetContext updates the current interaction context.
func (f *FooterHelpBar) SetContext(ctx FooterContext) {
	f.context = ctx
}

// View renders the footer help line.
func (f FooterHelpBar) View() string {
	hints := make([]keys.Binding, 0, 8)
	if f.context.ModalOpen {
		hints = append(hints, f.keyMap.Close)
	} else if f.context.SearchFocus {
		hints = append(hints, keys.Binding{Key: "esc", Description: "blur"})
		hints = append(hints, keys.Binding{Key: "ctrl+u", Description: "clear"})
		hints = append(hints, f.keyMap.Refresh, f.keyMap.Quit)
	} else {
		hints = append(hints, f.keyMap.ShortHelp()...)
	}

	parts := make([]string, 0, len(hints))
	for _, hint := range hints {
		parts = append(parts, fmt.Sprintf("%s %s", f.theme.FooterKey.Render(hint.Key), f.theme.FooterDesc.Render(hint.Description)))
	}
	line := strings.Join(parts, "  ")
	if f.width > 0 {
		line = lipgloss.NewStyle().Width(f.width).Render(line)
	}
	return f.theme.Footer.Render(line)
}
