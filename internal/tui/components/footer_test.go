package components

import (
	"strings"
	"testing"

	"opencoderouter/internal/tui/config"
	"opencoderouter/internal/tui/keys"
	"opencoderouter/internal/tui/theme"
)

func TestFooterHelpBarTerminalModeShowsDetachOnly(t *testing.T) {
	keyMap := keys.NewKeyMap(config.DefaultConfig().Keybindings)
	footer := NewFooterHelpBar(keyMap, theme.Minimal())

	footer.SetMode(FooterModeTerminal)
	footer.SetContext(FooterContext{ModalOpen: true, SearchFocus: true, ErrorDetailActive: true})

	view := footer.View()

	if !strings.Contains(view, keyMap.Detach.Description) {
		t.Fatalf("terminal footer should contain detach hint, got %q", view)
	}

	for _, binding := range []keys.Binding{keyMap.Search, keyMap.Refresh, keyMap.Attach, keyMap.Quit} {
		if strings.Contains(view, binding.Description) {
			t.Fatalf("terminal footer should not include tree binding %q, got %q", binding.Description, view)
		}
	}
}

func TestFooterHelpBarTreeModeShowsNormalBindings(t *testing.T) {
	keyMap := keys.NewKeyMap(config.DefaultConfig().Keybindings)
	footer := NewFooterHelpBar(keyMap, theme.Minimal())

	footer.SetMode(FooterModeTree)
	footer.SetContext(FooterContext{})

	view := footer.View()

	for _, binding := range []keys.Binding{keyMap.Search, keyMap.Refresh, keyMap.Attach, keyMap.Detach, keyMap.Quit} {
		if !strings.Contains(view, binding.Description) {
			t.Fatalf("tree footer missing binding %q in %q", binding.Description, view)
		}
	}
}
