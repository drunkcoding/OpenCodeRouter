package theme

import lipgloss "charm.land/lipgloss/v2"

// Theme is a complete visual style set used by all TUI components.
type Theme struct {
	Name string

	Header       lipgloss.Style
	HeaderTitle  lipgloss.Style
	HeaderSearch lipgloss.Style
	HeaderStat   lipgloss.Style

	MainPane    lipgloss.Style
	Tree        lipgloss.Style
	TreeHost    lipgloss.Style
	TreeProject lipgloss.Style
	TreeSession lipgloss.Style
	TreeCursor  lipgloss.Style
	TreeMuted   lipgloss.Style

	Inspect      lipgloss.Style
	InspectTitle lipgloss.Style

	Footer     lipgloss.Style
	FooterKey  lipgloss.Style
	FooterDesc lipgloss.Style

	ModalBackdrop lipgloss.Style
	ModalBox      lipgloss.Style
	ModalTitle    lipgloss.Style
	ModalBody     lipgloss.Style

	Spinner lipgloss.Style
	Accent  lipgloss.Style
	Success lipgloss.Style
	Danger  lipgloss.Style
	Muted   lipgloss.Style
}

// ByName returns the requested theme, falling back to NightOps.
func ByName(name string) Theme {
	switch name {
	case "auto":
		return Auto()
	case "nightops":
		return NightOps()
	case "light":
		return Light()
	case "minimal":
		return Minimal()
	default:
		return NightOps()
	}
}
