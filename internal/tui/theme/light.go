package theme

import lipgloss "charm.land/lipgloss/v2"

// Light returns a high-contrast light theme.
func Light() Theme {
	bg := lipgloss.Color("#F8FAFC")
	panel := lipgloss.Color("#E7EDF7")
	border := lipgloss.Color("#B7C5DA")
	text := lipgloss.Color("#1A2940")
	muted := lipgloss.Color("#586882")
	accent := lipgloss.Color("#005FCC")
	good := lipgloss.Color("#008F5A")
	bad := lipgloss.Color("#CC2B42")

	return Theme{
		Name: "light",

		Header:       lipgloss.NewStyle().Background(panel).Foreground(text).Padding(0, 1).BorderBottom(true).BorderStyle(lipgloss.NormalBorder()).BorderForeground(border),
		HeaderTitle:  lipgloss.NewStyle().Foreground(accent).Bold(true),
		HeaderSearch: lipgloss.NewStyle().Foreground(text).Background(bg).Padding(0, 1).Border(lipgloss.RoundedBorder()).BorderForeground(border),
		HeaderStat:   lipgloss.NewStyle().Foreground(muted),

		MainPane:    lipgloss.NewStyle().Background(bg),
		Tree:        lipgloss.NewStyle().Background(bg).Foreground(text).Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1),
		TreeHost:    lipgloss.NewStyle().Foreground(accent).Bold(true),
		TreeProject: lipgloss.NewStyle().Foreground(text),
		TreeSession: lipgloss.NewStyle().Foreground(text),
		TreeCursor:  lipgloss.NewStyle().Foreground(bg).Background(accent).Bold(true),
		TreeMuted:   lipgloss.NewStyle().Foreground(muted),

		Inspect:      lipgloss.NewStyle().Background(bg).Foreground(text).Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1),
		InspectTitle: lipgloss.NewStyle().Foreground(accent).Bold(true),

		Footer:     lipgloss.NewStyle().Background(panel).Foreground(text).Padding(0, 1).BorderTop(true).BorderStyle(lipgloss.NormalBorder()).BorderForeground(border),
		FooterKey:  lipgloss.NewStyle().Foreground(accent).Bold(true),
		FooterDesc: lipgloss.NewStyle().Foreground(muted),

		ModalBackdrop: lipgloss.NewStyle().Background(bg),
		ModalBox:      lipgloss.NewStyle().Foreground(text).Background(panel).Border(lipgloss.DoubleBorder()).BorderForeground(accent).Padding(1, 2),
		ModalTitle:    lipgloss.NewStyle().Foreground(accent).Bold(true),
		ModalBody:     lipgloss.NewStyle().Foreground(text),

		Spinner: lipgloss.NewStyle().Foreground(accent).Bold(true),
		Accent:  lipgloss.NewStyle().Foreground(accent),
		Success: lipgloss.NewStyle().Foreground(good),
		Danger:  lipgloss.NewStyle().Foreground(bad),
		Muted:   lipgloss.NewStyle().Foreground(muted),
	}
}
