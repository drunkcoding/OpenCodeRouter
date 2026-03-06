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

func TestSessionTreeViewProjectsAreCollapsedByDefault(t *testing.T) {
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

	view := tree.View()

	if !strings.Contains(view, "▸ proj-a") {
		t.Fatalf("expected collapsed project row by default, got %q", view)
	}
	if strings.Contains(view, "Session One") {
		t.Fatalf("expected project sessions to be hidden by default, got %q", view)
	}
}
