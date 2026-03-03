package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"opencoderouter/internal/tui/config"
	"opencoderouter/internal/tui/model"

	tea "charm.land/bubbletea/v2"
)

type fakeDiscoverer struct {
	hosts []model.Host
	err   error
}

func (f fakeDiscoverer) Discover(_ context.Context) ([]model.Host, error) {
	return append([]model.Host(nil), f.hosts...), f.err
}

type fakeProber struct{}

func (fakeProber) ProbeHosts(_ context.Context, hosts []model.Host) ([]model.Host, error) {
	if len(hosts) == 0 {
		return nil, nil
	}
	host := hosts[0]
	host.Status = model.HostStatusOnline
	host.Projects = []model.Project{{
		Name: "alpha",
		Sessions: []model.Session{{
			ID:           "session-1",
			Project:      "alpha",
			Title:        "Smoke session",
			LastActivity: time.Now(),
			Status:       model.SessionStatusActive,
			Activity:     model.ActivityActive,
			MessageCount: 1,
			Agents:       []string{"coder"},
		}},
	}}
	return []model.Host{host}, nil
}

func TestAppSmoke(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Display.Animation = false

	app := NewApp(cfg, fakeDiscoverer{hosts: []model.Host{{Name: "dev-1", Label: "dev-1"}}}, fakeProber{})
	initCmd := app.Init()
	if initCmd == nil {
		t.Fatal("expected init command")
	}

	if msg := initCmd(); msg != nil {
		if _, cmd := app.Update(msg); cmd != nil {
			_ = cmd
		}
	}

	_, _ = app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	view := app.View()
	if strings.TrimSpace(view.Content) == "" {
		t.Fatal("expected non-empty view output")
	}
}
