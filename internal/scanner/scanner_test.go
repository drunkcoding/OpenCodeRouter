package scanner

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"opencoderouter/internal/registry"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// fakeOpenCode creates an httptest.Server that mimics OpenCode's health + project endpoints.
func fakeOpenCode(healthy bool, projectName, projectPath, version string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/global/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"healthy": healthy,
			"version": version,
		})
	})
	mux.HandleFunc("/project/current", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":   projectName,
			"name": projectName,
			"path": projectPath,
		})
	})
	return httptest.NewServer(mux)
}

func extractPort(t *testing.T, url string) int {
	t.Helper()
	parts := strings.Split(url, ":")
	port, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		t.Fatalf("failed to extract port from %q: %v", url, err)
	}
	return port
}

// ---------------------------------------------------------------------------
// probePort — healthy instance
// ---------------------------------------------------------------------------

func TestProbePort_Healthy(t *testing.T) {
	srv := fakeOpenCode(true, "myproject", "/home/test/myproject", "1.2.3")
	defer srv.Close()

	port := extractPort(t, srv.URL)
	reg := registry.New(30*time.Second, testLogger())
	sc := New(reg, port, port, 5*time.Second, 1, 2*time.Second, testLogger())

	sc.probePort(context.Background(), port)

	if reg.Len() != 1 {
		t.Fatalf("expected 1 registered backend, got %d", reg.Len())
	}

	b, ok := reg.Lookup("myproject")
	if !ok {
		t.Fatal("expected to find 'myproject' in registry")
	}
	if b.Port != port {
		t.Errorf("expected port %d, got %d", port, b.Port)
	}
	if b.Version != "1.2.3" {
		t.Errorf("expected version '1.2.3', got %q", b.Version)
	}
	if b.ProjectPath != "/home/test/myproject" {
		t.Errorf("expected path '/home/test/myproject', got %q", b.ProjectPath)
	}
}

// ---------------------------------------------------------------------------
// probePort — unhealthy instance
// ---------------------------------------------------------------------------

func TestProbePort_Unhealthy(t *testing.T) {
	srv := fakeOpenCode(false, "proj", "/proj", "1.0")
	defer srv.Close()

	port := extractPort(t, srv.URL)
	reg := registry.New(30*time.Second, testLogger())
	sc := New(reg, port, port, 5*time.Second, 1, 2*time.Second, testLogger())

	sc.probePort(context.Background(), port)

	if reg.Len() != 0 {
		t.Error("unhealthy instance should not be registered")
	}
}

// ---------------------------------------------------------------------------
// probePort — no server running (connection refused)
// ---------------------------------------------------------------------------

func TestProbePort_NoServer(t *testing.T) {
	reg := registry.New(30*time.Second, testLogger())
	sc := New(reg, 19999, 19999, 5*time.Second, 1, 200*time.Millisecond, testLogger())

	sc.probePort(context.Background(), 19999) // nothing listening here

	if reg.Len() != 0 {
		t.Error("should not register when nothing is listening")
	}
}

// ---------------------------------------------------------------------------
// probePort — health OK but project endpoint fails
// ---------------------------------------------------------------------------

func TestProbePort_HealthOK_ProjectFails(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/global/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"healthy": true,
			"version": "1.0.0",
		})
	})
	mux.HandleFunc("/project/current", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	port := extractPort(t, srv.URL)
	reg := registry.New(30*time.Second, testLogger())
	sc := New(reg, port, port, 5*time.Second, 1, 2*time.Second, testLogger())

	sc.probePort(context.Background(), port)

	// Should still register with fallback info.
	if reg.Len() != 1 {
		t.Fatal("expected 1 backend even when project endpoint fails")
	}
}

// ---------------------------------------------------------------------------
// probePort — malformed health JSON
// ---------------------------------------------------------------------------

func TestProbePort_MalformedHealth(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/global/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	port := extractPort(t, srv.URL)
	reg := registry.New(30*time.Second, testLogger())
	sc := New(reg, port, port, 5*time.Second, 1, 2*time.Second, testLogger())

	sc.probePort(context.Background(), port)

	// Malformed JSON → health.Healthy is false (zero value) → not registered.
	if reg.Len() != 0 {
		t.Error("malformed health response should not result in registration")
	}
}

// ---------------------------------------------------------------------------
// probePort — health returns non-200
// ---------------------------------------------------------------------------

func TestProbePort_HealthNon200(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/global/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	port := extractPort(t, srv.URL)
	reg := registry.New(30*time.Second, testLogger())
	sc := New(reg, port, port, 5*time.Second, 1, 2*time.Second, testLogger())

	sc.probePort(context.Background(), port)

	if reg.Len() != 0 {
		t.Error("non-200 health should not result in registration")
	}
}

// ---------------------------------------------------------------------------
// probePort — project with empty name/path falls back to ID
// ---------------------------------------------------------------------------

func TestProbePort_FallbackToID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/global/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"healthy": true, "version": "1.0"})
	})
	mux.HandleFunc("/project/current", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":   "fallback-id",
			"name": "",
			"path": "",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	port := extractPort(t, srv.URL)
	reg := registry.New(30*time.Second, testLogger())
	sc := New(reg, port, port, 5*time.Second, 1, 2*time.Second, testLogger())

	sc.probePort(context.Background(), port)

	if reg.Len() != 1 {
		t.Fatal("expected 1 backend")
	}
	// The slug comes from Slugify(projectPath) where projectPath = "/unknown/fallback-id".
	b, ok := reg.Lookup("fallback-id")
	if !ok {
		t.Error("expected to find backend registered with fallback ID")
	}
	if b.ProjectName != "fallback-id" {
		t.Errorf("expected project name 'fallback-id', got %q", b.ProjectName)
	}
}

// ---------------------------------------------------------------------------
// Full scan cycle with multiple backends
// ---------------------------------------------------------------------------

func TestScan_MultipleBackends(t *testing.T) {
	srv1 := fakeOpenCode(true, "alpha", "/home/test/alpha", "1.0")
	defer srv1.Close()
	srv2 := fakeOpenCode(true, "beta", "/home/test/beta", "2.0")
	defer srv2.Close()
	// srv3 is unhealthy — should not be registered.
	srv3 := fakeOpenCode(false, "gamma", "/home/test/gamma", "3.0")
	defer srv3.Close()

	port1 := extractPort(t, srv1.URL)
	port2 := extractPort(t, srv2.URL)
	port3 := extractPort(t, srv3.URL)

	// Determine a range that covers all three test server ports.
	minPort := port1
	maxPort := port1
	for _, p := range []int{port2, port3} {
		if p < minPort {
			minPort = p
		}
		if p > maxPort {
			maxPort = p
		}
	}

	reg := registry.New(30*time.Second, testLogger())
	sc := New(reg, minPort, maxPort, 5*time.Second, 10, 2*time.Second, testLogger())

	sc.scan(context.Background())

	if reg.Len() != 2 {
		t.Errorf("expected 2 healthy backends, got %d", reg.Len())
	}

	_, ok1 := reg.Lookup("alpha")
	_, ok2 := reg.Lookup("beta")
	if !ok1 {
		t.Error("expected 'alpha' to be registered")
	}
	if !ok2 {
		t.Error("expected 'beta' to be registered")
	}
}

// ---------------------------------------------------------------------------
// Scan with context cancellation
// ---------------------------------------------------------------------------

func TestScan_ContextCancelled(t *testing.T) {
	reg := registry.New(30*time.Second, testLogger())
	sc := New(reg, 10000, 10100, 5*time.Second, 5, 200*time.Millisecond, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	// Should not panic or hang.
	sc.scan(ctx)
}

// ---------------------------------------------------------------------------
// Run stops on context cancellation
// ---------------------------------------------------------------------------

func TestRun_StopsOnCancel(t *testing.T) {
	reg := registry.New(30*time.Second, testLogger())
	// Use a tiny range (single port, nothing listening) so it's fast.
	sc := New(reg, 19998, 19998, 100*time.Millisecond, 1, 100*time.Millisecond, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sc.Run(ctx)
		close(done)
	}()

	// Let it run a cycle or two.
	time.Sleep(250 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// OK — Run exited.
	case <-time.After(2 * time.Second):
		t.Error("Run did not exit after context cancellation")
	}
}
