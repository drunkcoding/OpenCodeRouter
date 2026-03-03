package theme

import lipgloss "charm.land/lipgloss/v2"

// Minimal returns an ASCII-safe low-decoration theme.
func Minimal() Theme {
	text := lipgloss.Color("#FFFFFF")
	muted := lipgloss.Color("#B3B3B3")
	accent := lipgloss.Color("#00D7FF")
	good := lipgloss.Color("#5FFF87")
	bad := lipgloss.Color("#FF5F5F")

	return Theme{
		Name: "minimal",

		Header:       lipgloss.NewStyle().Foreground(text).Padding(0, 1),
		HeaderTitle:  lipgloss.NewStyle().Foreground(accent).Bold(true),
		HeaderSearch: lipgloss.NewStyle().Foreground(text).Padding(0, 1),
		HeaderStat:   lipgloss.NewStyle().Foreground(muted),

		MainPane:    lipgloss.NewStyle().Foreground(text),
		Tree:        lipgloss.NewStyle().Foreground(text).Padding(0, 1),
		TreeHost:    lipgloss.NewStyle().Foreground(accent).Bold(true),
		TreeProject: lipgloss.NewStyle().Foreground(text),
		TreeSession: lipgloss.NewStyle().Foreground(text),
		TreeCursor:  lipgloss.NewStyle().Reverse(true),
		TreeMuted:   lipgloss.NewStyle().Foreground(muted),

		Inspect:      lipgloss.NewStyle().Foreground(text).Padding(0, 1),
		InspectTitle: lipgloss.NewStyle().Foreground(accent).Bold(true),

		Footer:     lipgloss.NewStyle().Foreground(text).Padding(0, 1),
		FooterKey:  lipgloss.NewStyle().Foreground(accent).Bold(true),
		FooterDesc: lipgloss.NewStyle().Foreground(muted),

		ModalBackdrop: lipgloss.NewStyle(),
		ModalBox:      lipgloss.NewStyle().Foreground(text).Padding(1, 2),
		ModalTitle:    lipgloss.NewStyle().Foreground(accent).Bold(true),
		ModalBody:     lipgloss.NewStyle().Foreground(text),

		Spinner: lipgloss.NewStyle().Foreground(accent),
		Accent:  lipgloss.NewStyle().Foreground(accent),
		Success: lipgloss.NewStyle().Foreground(good),
		Danger:  lipgloss.NewStyle().Foreground(bad),
		Muted:   lipgloss.NewStyle().Foreground(muted),
	}
}
