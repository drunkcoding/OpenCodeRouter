package theme

import (
	lipgloss "charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
)

func Auto() Theme {
	adaptive := func(light, dark string) compat.AdaptiveColor {
		return compat.AdaptiveColor{
			Light: lipgloss.Color(light),
			Dark:  lipgloss.Color(dark),
		}
	}

	bg := adaptive("#F8FAFC", "#05070D")
	panel := adaptive("#E7EDF7", "#0D1424")
	border := adaptive("#B7C5DA", "#22324C")
	text := adaptive("#1A2940", "#D9E6FF")
	muted := adaptive("#586882", "#7A8AA8")
	accent := adaptive("#005FCC", "#58D9FF")
	good := adaptive("#008F5A", "#5DF2A3")
	bad := adaptive("#CC2B42", "#FF6B8A")

	return Theme{
		Name: "auto",

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
