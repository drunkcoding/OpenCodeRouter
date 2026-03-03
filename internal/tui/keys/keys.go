package keys

import (
	"fmt"
	"strings"

	"opencoderouter/internal/tui/config"
)

// Binding defines a logical key mapping and description.
type Binding struct {
	Key         string
	Description string
}

// KeyMap is the complete set of runtime bindings used by the TUI.
type KeyMap struct {
	Attach      Binding
	Search      Binding
	Refresh     Binding
	Quit        Binding
	NewSession  Binding
	KillSession Binding
	Inspect     Binding
	CycleView   Binding

	Up       Binding
	Down     Binding
	Toggle   Binding
	Collapse Binding
	Expand   Binding
	Close    Binding
}

// NewKeyMap builds key bindings from loaded config values.
func NewKeyMap(cfg config.KeybindingsConfig) KeyMap {
	attach := firstNonEmpty(cfg.Attach, "enter")
	search := firstNonEmpty(cfg.Search, "/")
	refresh := firstNonEmpty(cfg.Refresh, "r")
	quit := firstNonEmpty(cfg.Quit, "q")
	newSession := firstNonEmpty(cfg.NewSession, "n")
	killSession := firstNonEmpty(cfg.KillSession, "d")
	inspect := firstNonEmpty(cfg.Inspect, "i")
	cycleView := firstNonEmpty(cfg.CycleView, "tab")

	return KeyMap{
		Attach:      Binding{Key: attach, Description: "attach"},
		Search:      Binding{Key: search, Description: "search"},
		Refresh:     Binding{Key: refresh, Description: "refresh"},
		Quit:        Binding{Key: quit, Description: "quit"},
		NewSession:  Binding{Key: newSession, Description: "new"},
		KillSession: Binding{Key: killSession, Description: "kill"},
		Inspect:     Binding{Key: inspect, Description: "inspect"},
		CycleView:   Binding{Key: cycleView, Description: "cycle"},
		Up:          Binding{Key: "up", Description: "up"},
		Down:        Binding{Key: "down", Description: "down"},
		Toggle:      Binding{Key: "enter", Description: "toggle"},
		Collapse:    Binding{Key: "left", Description: "collapse"},
		Expand:      Binding{Key: "right", Description: "expand"},
		Close:       Binding{Key: "esc", Description: "close"},
	}
}

// Matches checks whether the keypress matches a binding.
func Matches(pressed string, binding Binding) bool {
	return strings.EqualFold(strings.TrimSpace(pressed), strings.TrimSpace(binding.Key))
}

// ShortHelp returns compact bindings suitable for footer rendering.
func (k KeyMap) ShortHelp() []Binding {
	return []Binding{k.Search, k.Refresh, k.NewSession, k.KillSession, k.Attach, k.Quit}
}

// FullHelp returns grouped bindings for expanded help views.
func (k KeyMap) FullHelp() [][]Binding {
	return [][]Binding{
		{k.Search, k.Refresh, k.CycleView, k.Quit},
		{k.Up, k.Down, k.Toggle, k.Attach, k.Inspect},
		{k.NewSession, k.KillSession, k.Collapse, k.Expand, k.Close},
	}
}

// HelpText renders a one-line key hint string.
func (k KeyMap) HelpText() string {
	parts := make([]string, 0, len(k.ShortHelp()))
	for _, binding := range k.ShortHelp() {
		parts = append(parts, fmt.Sprintf("%s %s", binding.Key, binding.Description))
	}
	return strings.Join(parts, " • ")
}

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
