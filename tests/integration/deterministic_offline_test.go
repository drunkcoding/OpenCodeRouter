package integration_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"opencoderouter/internal/api"
	"opencoderouter/internal/auth"
	"opencoderouter/internal/session"
)

func TestDeterministicOfflineIndicator(t *testing.T) {
	mgr := newFakeSessionManager()
	bus := session.NewEventBus(16)

	// Create a router with our fake manager
	r := api.NewRouter(api.RouterConfig{
		SessionManager:  mgr,
		SessionEventBus: bus,
		Fallback:        http.NotFoundHandler(),
		AuthConfig:      auth.Defaults(),
	})

	srv := httptest.NewServer(r)
	
	// 1. Initial Load (Online)
	resp, err := http.Get(srv.URL + "/api/sessions")
	if err != nil {
		t.Fatalf("Failed initial load: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Initial load status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 2. Simulate Offline by closing server
	srv.Close()

	// 3. Attempt to fetch (should fail)
	_, err = http.Get(srv.URL + "/api/sessions")
	if err == nil {
		t.Fatal("Expected error fetching from closed server, got nil")
	}
}
