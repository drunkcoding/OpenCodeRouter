package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"opencoderouter/internal/auth"
	"opencoderouter/internal/cache"
	"opencoderouter/internal/session"
)

func TestNewRouterMountsSessionRoutesAndFallback(t *testing.T) {
	mgr := newFakeStatefulSessionManager()
	workspace := t.TempDir()
	_, err := mgr.Create(context.Background(), session.CreateOpts{WorkspacePath: workspace})
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}

	fallback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/backends" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.WriteHeader(http.StatusTeapot)
	})

	h := NewRouter(RouterConfig{SessionManager: mgr, Fallback: fallback})
	srv := httptest.NewServer(h)
	defer srv.Close()

	respSessions := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/sessions", nil)
	if respSessions.StatusCode != http.StatusOK {
		defer respSessions.Body.Close()
		t.Fatalf("sessions status=%d want=%d", respSessions.StatusCode, http.StatusOK)
	}
	_ = respSessions.Body.Close()

	respFallback := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/backends", nil)
	if respFallback.StatusCode != http.StatusAccepted {
		defer respFallback.Body.Close()
		t.Fatalf("fallback status=%d want=%d", respFallback.StatusCode, http.StatusAccepted)
	}
	_ = respFallback.Body.Close()
}

func TestNewRouterAppliesAuthMiddlewareToSessionEndpoints(t *testing.T) {
	mgr := newFakeStatefulSessionManager()
	workspace := t.TempDir()
	_, err := mgr.Create(context.Background(), session.CreateOpts{WorkspacePath: workspace})
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}

	authCfg := auth.Defaults()
	authCfg.Enabled = true
	authCfg.BearerTokens = []string{"secret-token"}

	h := NewRouter(RouterConfig{
		SessionManager:  mgr,
		AuthConfig:      authCfg,
		ScrollbackCache: newRouterTestScrollbackCache(),
		Fallback:        http.NotFoundHandler(),
	})

	srv := httptest.NewServer(h)
	defer srv.Close()

	unauthorized := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/sessions", nil)
	if unauthorized.StatusCode != http.StatusUnauthorized {
		defer unauthorized.Body.Close()
		t.Fatalf("unauthorized status=%d want=%d", unauthorized.StatusCode, http.StatusUnauthorized)
	}
	_ = unauthorized.Body.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/sessions", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer secret-token")
	authorized, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("authorized request failed: %v", err)
	}
	if authorized.StatusCode != http.StatusOK {
		defer authorized.Body.Close()
		t.Fatalf("authorized status=%d want=%d", authorized.StatusCode, http.StatusOK)
	}
	_ = authorized.Body.Close()
}

func TestNewRouterKeepsAuthBypassPaths(t *testing.T) {
	authCfg := auth.Defaults()
	authCfg.Enabled = true
	authCfg.BearerTokens = []string{"secret-token"}

	h := NewRouter(RouterConfig{
		AuthConfig:      authCfg,
		ScrollbackCache: newRouterTestScrollbackCache(),
		Fallback: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/health" {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
		}),
	})

	srv := httptest.NewServer(h)
	defer srv.Close()

	resp := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/health", nil)
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		t.Fatalf("health status=%d want=%d", resp.StatusCode, http.StatusOK)
	}
	_ = resp.Body.Close()
}

func TestNewRouterMountsEventsRoute(t *testing.T) {
	eventBus := session.NewEventBus(8)
	h := NewRouter(RouterConfig{
		SessionEventBus: eventBus,
		ScrollbackCache: newRouterTestScrollbackCache(),
		Fallback:        http.NotFoundHandler(),
	})

	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/api/events")
	if err != nil {
		t.Fatalf("events request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		t.Fatalf("events status=%d want=%d", resp.StatusCode, http.StatusOK)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		defer resp.Body.Close()
		t.Fatalf("events content-type=%q want=%q", got, "text/event-stream")
	}
	_ = resp.Body.Close()
}

type routerTestScrollbackCache struct{}

func newRouterTestScrollbackCache() *routerTestScrollbackCache { return &routerTestScrollbackCache{} }

func (c *routerTestScrollbackCache) Append(sessionID string, entry cache.Entry) error { return nil }
func (c *routerTestScrollbackCache) Get(sessionID string, offset, limit int) ([]cache.Entry, error) {
	return []cache.Entry{}, nil
}
func (c *routerTestScrollbackCache) Trim(sessionID string, maxEntries int) error { return nil }
func (c *routerTestScrollbackCache) Clear(sessionID string) error                { return nil }
func (c *routerTestScrollbackCache) Close() error                                { return nil }
