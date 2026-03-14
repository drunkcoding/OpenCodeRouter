package components

import (
	"strings"
	"testing"

	"opencoderouter/internal/model"
	"opencoderouter/internal/tui/theme"
)

func TestSessionTreeViewSessionIndicatorShownForActiveSession(t *testing.T) {
	tree := NewSessionTreeView(theme.Minimal())
	tree.SetHosts([]model.Host{
		{
			Name:  "host-a",
			Label: "host-a",
			Projects: []model.Project{
				{
					Name: "proj-a",
					Sessions: []model.Session{
						{ID: "session-1", Title: "Session One", Activity: model.ActivityActive},
					},
				},
			},
		},
	})
	tree.SetActiveSessionLookup(func(sessionID string) bool { return sessionID == "session-1" })
	tree.cursor = 1
	tree.expandAtCursor()

	view := tree.View()

	if !strings.Contains(view, "●") {
		t.Fatalf("expected active session indicator in view, got %q", view)
	}
	if !strings.Contains(view, "• Session One (ACTIVE)") {
		t.Fatalf("expected session row text in view, got %q", view)
	}
}

func TestSessionTreeViewSessionIndicatorHiddenForInactiveSession(t *testing.T) {
	tree := NewSessionTreeView(theme.Minimal())
	tree.SetHosts([]model.Host{
		{
			Name:  "host-a",
			Label: "host-a",
			Projects: []model.Project{
				{
					Name: "proj-a",
					Sessions: []model.Session{
						{ID: "session-1", Title: "Session One", Activity: model.ActivityActive},
					},
				},
			},
		},
	})

	active := map[string]bool{"session-1": true}
	tree.SetActiveSessionLookup(func(sessionID string) bool { return active[sessionID] })
	tree.cursor = 1
	tree.expandAtCursor()
	if !strings.Contains(tree.View(), "●") {
		t.Fatalf("expected indicator while session is active")
	}

	delete(active, "session-1")
	view := tree.View()

	if strings.Contains(view, "●") {
		t.Fatalf("expected no indicator for inactive session, got %q", view)
	}
	if !strings.Contains(view, "• Session One (ACTIVE)") {
		t.Fatalf("expected session row text in view, got %q", view)
	}
}

func TestSessionTreeViewProjectsWithSessionsAreExpandedByDefault(t *testing.T) {
	tree := NewSessionTreeView(theme.Minimal())
	tree.SetHosts([]model.Host{
		{
			Name:   "host-a",
			Label:  "host-a",
			Status: model.HostStatusOnline,
			Projects: []model.Project{
				{
					Name: "proj-a",
					Sessions: []model.Session{
						{ID: "session-1", Title: "Session One", Activity: model.ActivityActive},
						{ID: "session-2", Title: "Session Two", Activity: model.ActivityIdle},
					},
				},
			},
		},
		{
			Name:   "host-b",
			Label:  "host-b",
			Status: model.HostStatusOnline,
			Projects: []model.Project{
				{
					Name: "proj-empty",
					Sessions: []model.Session{},
				},
			},
		},
	})

	view := tree.View()

	if !strings.Contains(view, "host-a [online] (2 sessions)") {
		t.Errorf("expected host-a to show session count, got %q", view)
	}

	if !strings.Contains(view, "host-b [online] (no sessions)") {
		t.Errorf("expected empty host to show empty indicator, got %q", view)
	}

	if !strings.Contains(view, "▾ proj-a") {
		t.Errorf("expected expanded project row by default, got %q", view)
	}
	if !strings.Contains(view, "Session One") {
		t.Errorf("expected project sessions to be visible by default, got %q", view)
	}
}

func TestFormatHostLabel(t *testing.T) {
	tests := []struct {
		name     string
		host     model.Host
		expected string
	}{
		{
			name: "offline host",
			host: model.Host{
				Name:   "host-offline",
				Label:  "host-offline",
				Status: model.HostStatusOffline,
			},
			expected: "host-offline [offline] (offline)",
		},
		{
			name: "error host",
			host: model.Host{
				Name:   "host-error",
				Label:  "host-error",
				Status: model.HostStatusError,
			},
			expected: "host-error [error] (offline)",
		},
		{
			name: "zero sessions online",
			host: model.Host{
				Name:   "host-empty",
				Label:  "host-empty",
				Status: model.HostStatusOnline,
			},
			expected: "host-empty [online] (no sessions)",
		},
		{
			name: "one session online",
			host: model.Host{
				Name:   "host-one",
				Label:  "host-one",
				Status: model.HostStatusOnline,
				Projects: []model.Project{
					{
						Sessions: []model.Session{{ID: "1"}},
					},
				},
			},
			expected: "host-one [online] (1 session)",
		},
		{
			name: "multiple sessions online",
			host: model.Host{
				Name:   "host-many",
				Label:  "host-many",
				Status: model.HostStatusOnline,
				Projects: []model.Project{
					{
						Sessions: []model.Session{{ID: "1"}, {ID: "2"}},
					},
				},
			},
			expected: "host-many [online] (2 sessions)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatHostLabel(tt.host)
			if got != tt.expected {
				t.Errorf("formatHostLabel() = %q, want %q", got, tt.expected)
			}
		})
	}
}
