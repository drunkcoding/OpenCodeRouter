package components

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// SpinnerTickMsg advances the Braille spinner.
type SpinnerTickMsg struct {
	Time time.Time
}

// BrailleSpinner renders frames: ⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏.
type BrailleSpinner struct {
	frames   []string
	index    int
	enabled  bool
	interval time.Duration
}

// NewBrailleSpinner creates a spinner using the Night Ops frame set.
func NewBrailleSpinner(enabled bool) BrailleSpinner {
	return BrailleSpinner{
		frames:   []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		enabled:  enabled,
		interval: 90 * time.Millisecond,
	}
}

// Init returns the initial tick command for animation.
func (s BrailleSpinner) Init() tea.Cmd {
	if !s.enabled {
		return nil
	}
	return tea.Tick(s.interval, func(t time.Time) tea.Msg {
		return SpinnerTickMsg{Time: t}
	})
}

// Update consumes SpinnerTickMsg and advances the frame pointer.
func (s BrailleSpinner) Update(msg tea.Msg) (BrailleSpinner, tea.Cmd) {
	if !s.enabled {
		return s, nil
	}
	if _, ok := msg.(SpinnerTickMsg); !ok {
		return s, nil
	}
	s.index = (s.index + 1) % len(s.frames)
	return s, tea.Tick(s.interval, func(t time.Time) tea.Msg {
		return SpinnerTickMsg{Time: t}
	})
}

// Frame returns the current spinner glyph.
func (s BrailleSpinner) Frame() string {
	if !s.enabled || len(s.frames) == 0 {
		return ""
	}
	return s.frames[s.index]
}
