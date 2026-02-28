package proxy

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"opencoderouter/internal/config"
	"opencoderouter/internal/registry"
)

func testCfg() config.Config {
	cfg := config.Defaults()
	cfg.Username = "testuser"
	cfg.ListenPort = 8080
	return cfg
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newTestRouter(reg *registry.Registry) *Router {
	return New(reg, testCfg(), testLogger())
}

// ---------------------------------------------------------------------------
// slugFromHost
// ---------------------------------------------------------------------------

func TestSlugFromHost(t *testing.T) {
	rt := newTestRouter(registry.New(30*time.Second, testLogger()))

	tests := []struct {
		name string
		host string
		want string
	}{
		{"valid", "myproject-testuser.local", "myproject"},
		{"valid with port", "myproject-testuser.local:8080", "myproject"},
		{"wrong username", "myproject-otheruser.local", ""},
		{"no .local", "myproject-testuser.com", ""},
		{"no slug", "-testuser.local", ""},
		{"just username", "testuser.local", ""},
		{"empty", "", ""},
		{"localhost", "localhost", ""},
		{"localhost with port", "localhost:8080", ""},
		{"multi-part slug", "my-cool-project-testuser.local", "my-cool-project"},
		{"slug with numbers", "proj123-testuser.local:9090", "proj123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rt.slugFromHost(tt.host)
			if got != tt.want {
				t.Errorf("slugFromHost(%q) = %q, want %q", tt.host, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// slugFromPath
// ---------------------------------------------------------------------------

func TestSlugFromPath(t *testing.T) {
	rt := newTestRouter(registry.New(30*time.Second, testLogger()))

	tests := []struct {
		name     string
		path     string
		wantSlug string
		wantRest string
	}{
		{"slug with path", "/myproject/api/v1", "myproject", "/api/v1"},
		{"slug only", "/myproject", "myproject", "/"},
		{"slug trailing slash", "/myproject/", "myproject", "/"},
		{"root", "/", "", ""},
		{"empty", "", "", ""},
		{"deep nesting", "/proj/a/b/c/d", "proj", "/a/b/c/d"},
		{"api prefix", "/api/backends", "api", "/backends"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slug, rest := rt.slugFromPath(tt.path)
			if slug != tt.wantSlug {
				t.Errorf("slugFromPath(%q) slug = %q, want %q", tt.path, slug, tt.wantSlug)
			}
			if rest != tt.wantRest {
				t.Errorf("slugFromPath(%q) remainder = %q, want %q", tt.path, rest, tt.wantRest)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Host-based proxy routing
// ---------------------------------------------------------------------------

func TestServeHTTP_HostRouting(t *testing.T) {
	// Start a fake backend.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "reached")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello from backend"))
	}))
	defer backend.Close()

	// Extract port from the test server URL.
	parts := strings.Split(backend.URL, ":")
	port := parts[len(parts)-1]

	reg := registry.New(30*time.Second, testLogger())
	// Register with the test server's port. We need an int.
	var portInt int
	for _, c := range port {
		portInt = portInt*10 + int(c-'0')
	}
	reg.Upsert(portInt, "myproject", "/home/test/myproject", "1.0")

	rt := newTestRouter(reg)
	srv := httptest.NewServer(rt)
	defer srv.Close()

	// Make a request with the correct Host header.
	req, _ := http.NewRequest("GET", srv.URL+"/some/path", nil)
	req.Host = "myproject-testuser.local:8080"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello from backend" {
		t.Errorf("unexpected body: %s", body)
	}
}

// ---------------------------------------------------------------------------
// Path-based proxy routing
// ---------------------------------------------------------------------------

func TestServeHTTP_PathRouting(t *testing.T) {
	// Start a fake backend that echoes the received path.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("path=" + r.URL.Path))
	}))
	defer backend.Close()

	parts := strings.Split(backend.URL, ":")
	port := parts[len(parts)-1]
	var portInt int
	for _, c := range port {
		portInt = portInt*10 + int(c-'0')
	}

	reg := registry.New(30*time.Second, testLogger())
	reg.Upsert(portInt, "proj", "/home/test/proj", "1.0")

	rt := newTestRouter(reg)
	srv := httptest.NewServer(rt)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/proj/api/v1/health")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	// The path prefix "/proj" should be stripped, leaving "/api/v1/health".
	if string(body) != "path=/api/v1/health" {
		t.Errorf("unexpected body: %s (expected path=/api/v1/health)", body)
	}
}

// ---------------------------------------------------------------------------
// Dashboard (fallback)
// ---------------------------------------------------------------------------

func TestServeHTTP_Dashboard(t *testing.T) {
	reg := registry.New(30*time.Second, testLogger())
	rt := newTestRouter(reg)
	srv := httptest.NewServer(rt)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("expected text/html content type, got %q", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "OpenCode Router") {
		t.Error("dashboard should contain 'OpenCode Router'")
	}
	if !strings.Contains(string(body), "testuser") {
		t.Error("dashboard should contain the username")
	}
}

func TestServeHTTP_DashboardWithBackends(t *testing.T) {
	reg := registry.New(30*time.Second, testLogger())
	reg.Upsert(4096, "my-app", "/home/test/my-app", "2.0.0")

	rt := newTestRouter(reg)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	rt.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "my-app") {
		t.Error("dashboard should list the backend slug")
	}
	if !strings.Contains(body, "4096") {
		t.Error("dashboard should show the backend port")
	}
	if !strings.Contains(body, "2.0.0") {
		t.Error("dashboard should show the version")
	}
}

// ---------------------------------------------------------------------------
// API: /api/health
// ---------------------------------------------------------------------------

func TestAPIHealth(t *testing.T) {
	reg := registry.New(30*time.Second, testLogger())
	reg.Upsert(4096, "a", "/a", "1.0")
	reg.Upsert(4097, "b", "/b", "1.0")

	rt := newTestRouter(reg)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/health", nil)
	rt.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["healthy"] != true {
		t.Error("expected healthy=true")
	}
	if resp["username"] != "testuser" {
		t.Errorf("expected username=testuser, got %v", resp["username"])
	}
	// JSON numbers are float64.
	if resp["backends"].(float64) != 2 {
		t.Errorf("expected backends=2, got %v", resp["backends"])
	}
}

// ---------------------------------------------------------------------------
// API: /api/backends
// ---------------------------------------------------------------------------

func TestAPIBackends_Empty(t *testing.T) {
	reg := registry.New(30*time.Second, testLogger())
	rt := newTestRouter(reg)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/backends", nil)
	rt.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var items []interface{}
	json.Unmarshal(w.Body.Bytes(), &items)
	if len(items) != 0 {
		t.Errorf("expected empty list, got %d items", len(items))
	}
}

func TestAPIBackends_WithEntries(t *testing.T) {
	reg := registry.New(30*time.Second, testLogger())
	reg.Upsert(4096, "proj-a", "/home/test/proj-a", "1.0")

	rt := newTestRouter(reg)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/backends", nil)
	rt.ServeHTTP(w, req)

	var items []map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &items)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}

	item := items[0]
	if item["slug"] != "proj-a" {
		t.Errorf("expected slug 'proj-a', got %v", item["slug"])
	}
	if item["domain"] != "proj-a-testuser.local" {
		t.Errorf("expected domain 'proj-a-testuser.local', got %v", item["domain"])
	}
	if item["path_prefix"] != "/proj-a/" {
		t.Errorf("expected path_prefix '/proj-a/', got %v", item["path_prefix"])
	}
}

func TestAPIBackends_MethodNotAllowed(t *testing.T) {
	reg := registry.New(30*time.Second, testLogger())
	rt := newTestRouter(reg)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/backends", nil)
	rt.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Backend unavailable → 502
// ---------------------------------------------------------------------------

func TestServeHTTP_BackendDown(t *testing.T) {
	reg := registry.New(30*time.Second, testLogger())
	// Register a backend on a port where nothing is listening.
	reg.Upsert(19999, "dead", "/home/test/dead", "1.0")

	rt := newTestRouter(reg)
	srv := httptest.NewServer(rt)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/dead/anything")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Unknown slug falls through to dashboard
// ---------------------------------------------------------------------------

func TestServeHTTP_UnknownSlug(t *testing.T) {
	reg := registry.New(30*time.Second, testLogger())
	rt := newTestRouter(reg)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/nonexistent/path", nil)
	rt.ServeHTTP(w, req)

	// Should fall through to dashboard since "nonexistent" is not registered.
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 (dashboard), got %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "text/html") {
		t.Error("expected HTML dashboard for unknown slug")
	}
}
