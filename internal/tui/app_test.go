package tui

import (
	"bytes"
	"context"
	"log/slog"
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

	app := NewApp(cfg, fakeDiscoverer{hosts: []model.Host{{Name: "dev-1", Label: "dev-1"}}}, fakeProber{}, nil)
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

func TestNewApp_NilLoggerDefaultsToDiscard(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()

	app := NewApp(cfg, fakeDiscoverer{}, fakeProber{}, nil)
	if app == nil {
		t.Fatal("expected app to be constructed")
	}
	if app.logger == nil {
		t.Fatal("expected app.logger to be non-nil when input logger is nil")
	}
}

func TestNewApp_LoggerPropagated(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	app := NewApp(cfg, fakeDiscoverer{}, fakeProber{}, logger)
	if app.logger == nil {
		t.Fatal("expected app logger to be initialized")
	}

	_ = app.Init()
	if !strings.Contains(buf.String(), "component=app") {
		t.Fatal("expected app logger output to include component field")
	}
}
