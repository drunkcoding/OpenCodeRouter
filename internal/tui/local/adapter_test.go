package local

import (
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"opencoderouter/internal/model"
	"opencoderouter/internal/registry"
)

func TestAdapterGetLocalHostConvertsRegistryEntries(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 14, 10, 30, 0, 0, time.UTC)
	reg := registry.New(2*time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))

	reg.Upsert(30000, "alpha", "/work/alpha", "1.0.0")
	reg.Upsert(30001, "beta", "/work/beta", "1.1.0")

	reg.ReplaceSessions("alpha", []registry.SessionMetadata{
		{
			ID:           "sess-active",
			Title:        "active session",
			Directory:    "/work/alpha",
			Status:       "running",
			LastActivity: now.Add(-2 * time.Minute),
		},
		{
			ID:           "sess-idle",
			Title:        "idle session",
			Directory:    "/work/alpha",
			Status:       "idle",
			LastActivity: now.Add(-2 * time.Hour),
		},
	})

	adapter := NewAdapter(reg, 10*time.Minute, 24*time.Hour)
	adapter.nowFn = func() time.Time { return now }

	host, err := adapter.GetLocalHost()
	if err != nil {
		t.Fatalf("GetLocalHost() error = %v, want nil", err)
	}

	if host.Name != LocalHostName {
		t.Fatalf("host.Name = %q, want %q", host.Name, LocalHostName)
	}
	if host.Status != model.HostStatusOnline {
		t.Fatalf("host.Status = %q, want %q", host.Status, model.HostStatusOnline)
	}
	if len(host.Projects) != 2 {
		t.Fatalf("project count = %d, want 2", len(host.Projects))
	}

	if host.Projects[0].Name != "alpha" || host.Projects[1].Name != "beta" {
		t.Fatalf("project order = [%q, %q], want [alpha, beta]", host.Projects[0].Name, host.Projects[1].Name)
	}

	alpha := host.Projects[0]
	if len(alpha.Sessions) != 2 {
		t.Fatalf("alpha session count = %d, want 2", len(alpha.Sessions))
	}

	active := findSessionByID(alpha.Sessions, "sess-active")
	if active == nil {
		t.Fatal("expected sess-active in alpha project")
	}
	if active.Status != model.SessionStatusActive {
		t.Fatalf("sess-active status = %q, want %q", active.Status, model.SessionStatusActive)
	}
	if active.Activity != model.ActivityActive {
		t.Fatalf("sess-active activity = %q, want %q", active.Activity, model.ActivityActive)
	}

	idle := findSessionByID(alpha.Sessions, "sess-idle")
	if idle == nil {
		t.Fatal("expected sess-idle in alpha project")
	}
	if idle.Status != model.SessionStatusIdle {
		t.Fatalf("sess-idle status = %q, want %q", idle.Status, model.SessionStatusIdle)
	}
	if idle.Activity != model.ActivityIdle {
		t.Fatalf("sess-idle activity = %q, want %q", idle.Activity, model.ActivityIdle)
	}

	beta := host.Projects[1]
	if len(beta.Sessions) != 0 {
		t.Fatalf("beta session count = %d, want 0", len(beta.Sessions))
	}
}

func TestAdapterGetLocalHostNoBackends(t *testing.T) {
	t.Parallel()

	reg := registry.New(2*time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	adapter := NewAdapter(reg, 10*time.Minute, 24*time.Hour)

	_, err := adapter.GetLocalHost()
	if !errors.Is(err, ErrNoLocalBackends) {
		t.Fatalf("GetLocalHost() error = %v, want ErrNoLocalBackends", err)
	}
}

func findSessionByID(sessions []model.Session, id string) *model.Session {
	for i := range sessions {
		if sessions[i].ID == id {
			return &sessions[i]
		}
	}
	return nil
}
