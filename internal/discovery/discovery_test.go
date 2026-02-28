package discovery

import (
	"log/slog"
	"os"
	"testing"
	"time"

	"opencoderouter/internal/config"
	"opencoderouter/internal/registry"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func testCfg() config.Config {
	cfg := config.Defaults()
	cfg.Username = "testuser"
	cfg.ListenPort = 8080
	cfg.MDNSServiceType = "_opencode._tcp"
	return cfg
}

// ---------------------------------------------------------------------------
// New
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	adv := New(testCfg(), testLogger())
	if adv == nil {
		t.Fatal("New returned nil")
	}
	if adv.servers == nil {
		t.Error("servers map should be initialized")
	}
	if adv.outboundIP == nil {
		t.Error("outboundIP should be detected")
	}
}

// ---------------------------------------------------------------------------
// Sync — add new backends
// ---------------------------------------------------------------------------

func TestSync_RegistersNew(t *testing.T) {
	adv := New(testCfg(), testLogger())
	defer adv.Shutdown()

	backends := []*registry.Backend{
		{
			Port:        4096,
			ProjectName: "alpha",
			ProjectPath: "/home/test/alpha",
			Slug:        "alpha",
			Version:     "1.0",
			LastSeen:    time.Now(),
		},
	}

	adv.Sync(backends)

	adv.mu.Lock()
	defer adv.mu.Unlock()
	if len(adv.servers) != 1 {
		t.Errorf("expected 1 mDNS server, got %d", len(adv.servers))
	}
	if _, ok := adv.servers["alpha"]; !ok {
		t.Error("expected 'alpha' to be registered in mDNS")
	}
}

// ---------------------------------------------------------------------------
// Sync — remove stale backends
// ---------------------------------------------------------------------------

func TestSync_RemovesStale(t *testing.T) {
	adv := New(testCfg(), testLogger())
	defer adv.Shutdown()

	// First sync: register alpha and beta.
	adv.Sync([]*registry.Backend{
		{Slug: "alpha", Port: 4096, ProjectName: "alpha", ProjectPath: "/alpha", Version: "1.0", LastSeen: time.Now()},
		{Slug: "beta", Port: 4097, ProjectName: "beta", ProjectPath: "/beta", Version: "1.0", LastSeen: time.Now()},
	})

	adv.mu.Lock()
	if len(adv.servers) != 2 {
		t.Fatalf("expected 2 servers after first sync, got %d", len(adv.servers))
	}
	adv.mu.Unlock()

	// Second sync: only alpha remains.
	adv.Sync([]*registry.Backend{
		{Slug: "alpha", Port: 4096, ProjectName: "alpha", ProjectPath: "/alpha", Version: "1.0", LastSeen: time.Now()},
	})

	adv.mu.Lock()
	defer adv.mu.Unlock()
	if len(adv.servers) != 1 {
		t.Errorf("expected 1 server after second sync, got %d", len(adv.servers))
	}
	if _, ok := adv.servers["alpha"]; !ok {
		t.Error("expected 'alpha' to survive")
	}
	if _, ok := adv.servers["beta"]; ok {
		t.Error("expected 'beta' to be removed")
	}
}

// ---------------------------------------------------------------------------
// Sync — no-op when unchanged
// ---------------------------------------------------------------------------

func TestSync_NoopWhenUnchanged(t *testing.T) {
	adv := New(testCfg(), testLogger())
	defer adv.Shutdown()

	backends := []*registry.Backend{
		{Slug: "alpha", Port: 4096, ProjectName: "alpha", ProjectPath: "/alpha", Version: "1.0", LastSeen: time.Now()},
	}

	adv.Sync(backends)
	adv.mu.Lock()
	srv1 := adv.servers["alpha"]
	adv.mu.Unlock()

	// Sync again with the same set.
	adv.Sync(backends)
	adv.mu.Lock()
	srv2 := adv.servers["alpha"]
	adv.mu.Unlock()

	// Same server instance should be reused (not re-registered).
	if srv1 != srv2 {
		t.Error("expected same server instance when backend is unchanged")
	}
}

// ---------------------------------------------------------------------------
// Sync — empty list clears all
// ---------------------------------------------------------------------------

func TestSync_EmptyClearsAll(t *testing.T) {
	adv := New(testCfg(), testLogger())
	defer adv.Shutdown()

	adv.Sync([]*registry.Backend{
		{Slug: "alpha", Port: 4096, ProjectName: "alpha", ProjectPath: "/alpha", Version: "1.0", LastSeen: time.Now()},
		{Slug: "beta", Port: 4097, ProjectName: "beta", ProjectPath: "/beta", Version: "1.0", LastSeen: time.Now()},
	})

	adv.Sync(nil) // empty

	adv.mu.Lock()
	defer adv.mu.Unlock()
	if len(adv.servers) != 0 {
		t.Errorf("expected 0 servers after empty sync, got %d", len(adv.servers))
	}
}

// ---------------------------------------------------------------------------
// Shutdown
// ---------------------------------------------------------------------------

func TestShutdown(t *testing.T) {
	adv := New(testCfg(), testLogger())

	adv.Sync([]*registry.Backend{
		{Slug: "alpha", Port: 4096, ProjectName: "alpha", ProjectPath: "/alpha", Version: "1.0", LastSeen: time.Now()},
	})

	adv.Shutdown()

	adv.mu.Lock()
	defer adv.mu.Unlock()
	if len(adv.servers) != 0 {
		t.Errorf("expected 0 servers after shutdown, got %d", len(adv.servers))
	}
}

// ---------------------------------------------------------------------------
// Shutdown is idempotent
// ---------------------------------------------------------------------------

func TestShutdown_Idempotent(t *testing.T) {
	adv := New(testCfg(), testLogger())

	adv.Sync([]*registry.Backend{
		{Slug: "alpha", Port: 4096, ProjectName: "alpha", ProjectPath: "/alpha", Version: "1.0", LastSeen: time.Now()},
	})

	// Should not panic.
	adv.Shutdown()
	adv.Shutdown()
}
