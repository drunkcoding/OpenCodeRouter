package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"opencoderouter/internal/model"
)

type fakeRemoteDiscoverer struct {
	hosts []model.Host
	err   error
	path  string
}

func (f *fakeRemoteDiscoverer) Discover(_ context.Context) ([]model.Host, error) {
	return cloneHosts(f.hosts), f.err
}

func (f *fakeRemoteDiscoverer) SetSSHConfigPath(path string) {
	f.path = path
}

type fakeRemoteProber struct {
	hosts []model.Host
	err   error
	calls int
}

func (f *fakeRemoteProber) ProbeHosts(_ context.Context, hosts []model.Host) ([]model.Host, error) {
	f.calls++
	if len(f.hosts) > 0 {
		return cloneHosts(f.hosts), f.err
	}
	return cloneHosts(hosts), f.err
}

func TestRemoteHostsHandlerReturnsFreshScan(t *testing.T) {
	discoverer := &fakeRemoteDiscoverer{
		hosts: []model.Host{{
			Name:      "dev-host",
			Address:   "10.0.0.9",
			User:      "alice",
			Label:     "dev-host",
			Status:    model.HostStatusUnknown,
			Transport: model.TransportReady,
			Projects: []model.Project{{
				Name: "demo",
				Sessions: []model.Session{{
					ID:           "s-1",
					Title:        "Build",
					Directory:    "/repo",
					Status:       model.SessionStatusActive,
					Activity:     model.ActivityActive,
					LastActivity: time.Now().Add(-time.Minute),
				}},
			}},
		}},
	}

	prober := &fakeRemoteProber{
		hosts: []model.Host{{
			Name:      "dev-host",
			Address:   "10.0.0.9",
			User:      "alice",
			Label:     "dev-host",
			Status:    model.HostStatusOnline,
			Transport: model.TransportReady,
			Projects: []model.Project{{
				Name: "demo",
				Sessions: []model.Session{{
					ID:           "s-1",
					Title:        "Build",
					Directory:    "/repo",
					Status:       model.SessionStatusActive,
					Activity:     model.ActivityActive,
					LastActivity: time.Now().Add(-time.Minute),
				}},
			}},
		}},
	}

	h := NewRemoteHostsHandler(RemoteHostsHandlerConfig{
		CacheTTL:         time.Minute,
		DiscoveryService: discoverer,
		ProbeService:     prober,
	})

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/remote/hosts?refresh=true&sshConfigPath=%2Ftmp%2Fssh.conf", nil)
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		t.Fatalf("status=%d want=%d", resp.StatusCode, http.StatusOK)
	}
	body := decodeResponseJSON[remoteHostsResponse](t, resp.Body)
	_ = resp.Body.Close()

	if body.Cached {
		t.Fatal("expected uncached response")
	}
	if body.Stale {
		t.Fatal("expected non-stale response")
	}
	if body.Partial {
		t.Fatal("expected full response")
	}
	if len(body.Hosts) != 1 {
		t.Fatalf("hosts len=%d want=1", len(body.Hosts))
	}
	if body.Hosts[0].Name != "dev-host" {
		t.Fatalf("host name=%q want=dev-host", body.Hosts[0].Name)
	}
	if body.Hosts[0].Status != string(model.HostStatusOnline) {
		t.Fatalf("host status=%q want=%q", body.Hosts[0].Status, model.HostStatusOnline)
	}
	if body.Hosts[0].SessionCount != 1 {
		t.Fatalf("session count=%d want=1", body.Hosts[0].SessionCount)
	}
	if discoverer.path != "/tmp/ssh.conf" {
		t.Fatalf("ssh config path=%q want=%q", discoverer.path, "/tmp/ssh.conf")
	}
	if prober.calls != 1 {
		t.Fatalf("probe calls=%d want=1", prober.calls)
	}
}

func TestRemoteHostsHandlerReturnsCachedWithinTTL(t *testing.T) {
	discoverer := &fakeRemoteDiscoverer{hosts: []model.Host{{Name: "alpha", Address: "alpha.local", Label: "alpha", Status: model.HostStatusUnknown}}}
	prober := &fakeRemoteProber{hosts: []model.Host{{Name: "alpha", Address: "alpha.local", Label: "alpha", Status: model.HostStatusOnline}}}

	h := NewRemoteHostsHandler(RemoteHostsHandlerConfig{
		CacheTTL:         time.Minute,
		DiscoveryService: discoverer,
		ProbeService:     prober,
	})

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	first := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/remote/hosts", nil)
	if first.StatusCode != http.StatusOK {
		defer first.Body.Close()
		t.Fatalf("first status=%d want=%d", first.StatusCode, http.StatusOK)
	}
	_ = first.Body.Close()

	if prober.calls != 1 {
		t.Fatalf("probe calls after first=%d want=1", prober.calls)
	}

	second := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/remote/hosts", nil)
	if second.StatusCode != http.StatusOK {
		defer second.Body.Close()
		t.Fatalf("second status=%d want=%d", second.StatusCode, http.StatusOK)
	}
	body := decodeResponseJSON[remoteHostsResponse](t, second.Body)
	_ = second.Body.Close()

	if !body.Cached {
		t.Fatal("expected cached response")
	}
	if prober.calls != 1 {
		t.Fatalf("probe calls after cached response=%d want=1", prober.calls)
	}
}

func TestRemoteHostsHandlerCacheScopedBySSHConfigPath(t *testing.T) {
	discoverer := &fakeRemoteDiscoverer{hosts: []model.Host{{Name: "alpha", Address: "alpha.local", Label: "alpha", Status: model.HostStatusUnknown}}}
	prober := &fakeRemoteProber{hosts: []model.Host{{Name: "alpha", Address: "alpha.local", Label: "alpha", Status: model.HostStatusOnline}}}

	h := NewRemoteHostsHandler(RemoteHostsHandlerConfig{
		CacheTTL:         time.Minute,
		DiscoveryService: discoverer,
		ProbeService:     prober,
	})

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	first := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/remote/hosts?sshConfigPath=%2Ftmp%2Fa.conf", nil)
	if first.StatusCode != http.StatusOK {
		defer first.Body.Close()
		t.Fatalf("first status=%d want=%d", first.StatusCode, http.StatusOK)
	}
	_ = first.Body.Close()

	second := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/remote/hosts?sshConfigPath=%2Ftmp%2Fa.conf", nil)
	if second.StatusCode != http.StatusOK {
		defer second.Body.Close()
		t.Fatalf("second status=%d want=%d", second.StatusCode, http.StatusOK)
	}
	body := decodeResponseJSON[remoteHostsResponse](t, second.Body)
	_ = second.Body.Close()
	if !body.Cached {
		t.Fatal("expected same-config request to be served from cache")
	}
	if prober.calls != 1 {
		t.Fatalf("probe calls after cached same-config response=%d want=1", prober.calls)
	}

	third := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/remote/hosts?sshConfigPath=%2Ftmp%2Fb.conf", nil)
	if third.StatusCode != http.StatusOK {
		defer third.Body.Close()
		t.Fatalf("third status=%d want=%d", third.StatusCode, http.StatusOK)
	}
	body = decodeResponseJSON[remoteHostsResponse](t, third.Body)
	_ = third.Body.Close()
	if body.Cached {
		t.Fatal("expected different-config request to trigger fresh scan")
	}
	if prober.calls != 2 {
		t.Fatalf("probe calls after different-config response=%d want=2", prober.calls)
	}
	if discoverer.path != "/tmp/b.conf" {
		t.Fatalf("ssh config path=%q want=%q", discoverer.path, "/tmp/b.conf")
	}
}

func TestRemoteHostsHandlerFallsBackToStaleCacheOnFailure(t *testing.T) {
	discoverer := &fakeRemoteDiscoverer{hosts: []model.Host{{Name: "alpha", Address: "alpha.local", Label: "alpha", Status: model.HostStatusUnknown}}}
	prober := &fakeRemoteProber{hosts: []model.Host{{Name: "alpha", Address: "alpha.local", Label: "alpha", Status: model.HostStatusOnline}}}

	h := NewRemoteHostsHandler(RemoteHostsHandlerConfig{
		CacheTTL:         time.Second,
		DiscoveryService: discoverer,
		ProbeService:     prober,
	})

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	seed := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/remote/hosts?refresh=true", nil)
	if seed.StatusCode != http.StatusOK {
		defer seed.Body.Close()
		t.Fatalf("seed status=%d want=%d", seed.StatusCode, http.StatusOK)
	}
	_ = seed.Body.Close()

	discoverer.err = errors.New("lookup failed")
	discoverer.hosts = nil
	prober.err = errors.New("unreachable")
	prober.hosts = nil

	resp := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/remote/hosts?refresh=true", nil)
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		t.Fatalf("status=%d want=%d", resp.StatusCode, http.StatusOK)
	}
	body := decodeResponseJSON[remoteHostsResponse](t, resp.Body)
	_ = resp.Body.Close()

	if !body.Cached {
		t.Fatal("expected cached fallback response")
	}
	if !body.Stale {
		t.Fatal("expected stale fallback response")
	}
	if !body.Partial {
		t.Fatal("expected partial fallback response")
	}
	if len(body.Hosts) != 1 || body.Hosts[0].Name != "alpha" {
		t.Fatalf("unexpected fallback hosts: %#v", body.Hosts)
	}
	if len(body.Warnings) == 0 {
		t.Fatal("expected warnings for failed refresh")
	}
}

func TestRemoteHostsHandlerMethodAndValidationErrors(t *testing.T) {
	h := NewRemoteHostsHandler(RemoteHostsHandlerConfig{
		DiscoveryService: &fakeRemoteDiscoverer{},
		ProbeService:     &fakeRemoteProber{},
	})

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	methodResp := doJSONRequest(t, srv.Client(), http.MethodPost, srv.URL+"/api/remote/hosts", nil)
	assertErrorShape(t, methodResp, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")

	invalidQuery := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/remote/hosts?refresh=not-bool", nil)
	assertErrorShape(t, invalidQuery, http.StatusBadRequest, "INVALID_QUERY")
}

func TestRemoteHostsHandlerUnsupportedSSHConfigOverride(t *testing.T) {
	h := NewRemoteHostsHandler(RemoteHostsHandlerConfig{
		DiscoveryService: remoteDiscovererNoPathSetter{},
		ProbeService:     &fakeRemoteProber{},
	})

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/remote/hosts?sshConfigPath=%2Ftmp%2Fssh.conf", nil)
	assertErrorShape(t, resp, http.StatusBadRequest, "SSH_CONFIG_OVERRIDE_UNSUPPORTED")
}

type remoteDiscovererNoPathSetter struct{}

func (remoteDiscovererNoPathSetter) Discover(_ context.Context) ([]model.Host, error) {
	return []model.Host{}, nil
}
