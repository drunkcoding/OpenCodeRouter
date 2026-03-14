package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"opencoderouter/internal/cache"
	"opencoderouter/internal/session"
)

func TestScrollbackEndpointReturnsEntriesWithDefaultLimit(t *testing.T) {
	mgr := newFakeStatefulSessionManager()
	sc := newTestScrollbackCache()
	workspace := t.TempDir()
	created, err := mgr.Create(context.Background(), session.CreateOpts{WorkspacePath: workspace})
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}

	for i := 0; i < 3; i++ {
		err := sc.Append(created.ID, cache.Entry{Timestamp: time.Now().UTC(), Type: cache.EntryTypeTerminalOutput, Content: []byte{byte('a' + i)}})
		if err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	srv := newScrollbackTestServer(t, mgr, sc)
	defer srv.Close()

	resp := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/sessions/"+created.ID+"/scrollback", nil)
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		t.Fatalf("status=%d want=%d", resp.StatusCode, http.StatusOK)
	}
	var entries []cache.Entry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		_ = resp.Body.Close()
		t.Fatalf("decode: %v", err)
	}
	_ = resp.Body.Close()
	if len(entries) != 3 {
		t.Fatalf("entries=%d want=3", len(entries))
	}
}

func TestScrollbackEndpointSupportsLimitOffsetAndTypeFilter(t *testing.T) {
	mgr := newFakeStatefulSessionManager()
	sc := newTestScrollbackCache()
	workspace := t.TempDir()
	created, err := mgr.Create(context.Background(), session.CreateOpts{WorkspacePath: workspace})
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}

	seed := []cache.Entry{
		{Timestamp: time.Now().UTC(), Type: cache.EntryTypeTerminalOutput, Content: []byte("o1")},
		{Timestamp: time.Now().UTC(), Type: cache.EntryTypeSystemEvent, Content: []byte("s1")},
		{Timestamp: time.Now().UTC(), Type: cache.EntryTypeTerminalOutput, Content: []byte("o2")},
		{Timestamp: time.Now().UTC(), Type: cache.EntryTypeTerminalOutput, Content: []byte("o3")},
	}
	for _, entry := range seed {
		if err := sc.Append(created.ID, entry); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	srv := newScrollbackTestServer(t, mgr, sc)
	defer srv.Close()

	resp := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/sessions/"+created.ID+"/scrollback?type=terminal_output&offset=1&limit=1", nil)
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		t.Fatalf("status=%d want=%d", resp.StatusCode, http.StatusOK)
	}
	var entries []cache.Entry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		_ = resp.Body.Close()
		t.Fatalf("decode: %v", err)
	}
	_ = resp.Body.Close()
	if len(entries) != 1 {
		t.Fatalf("entries=%d want=1", len(entries))
	}
	if entries[0].Type != cache.EntryTypeTerminalOutput || string(entries[0].Content) != "o2" {
		t.Fatalf("unexpected filtered entry: %+v", entries[0])
	}
}

func TestScrollbackEndpointRejectsInvalidQuery(t *testing.T) {
	mgr := newFakeStatefulSessionManager()
	sc := newTestScrollbackCache()
	workspace := t.TempDir()
	created, err := mgr.Create(context.Background(), session.CreateOpts{WorkspacePath: workspace})
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}

	srv := newScrollbackTestServer(t, mgr, sc)
	defer srv.Close()

	resp := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/sessions/"+created.ID+"/scrollback?limit=abc", nil)
	assertErrorShape(t, resp, http.StatusBadRequest, "INVALID_SCROLLBACK_QUERY")

	resp2 := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/sessions/"+created.ID+"/scrollback?offset=-1", nil)
	assertErrorShape(t, resp2, http.StatusBadRequest, "INVALID_SCROLLBACK_QUERY")
}

func newScrollbackTestServer(t *testing.T, mgr session.SessionManager, sc cache.ScrollbackCache) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	NewSessionsHandler(SessionsHandlerConfig{SessionManager: mgr, ScrollbackCache: sc}).Register(mux)
	return httptest.NewServer(mux)
}

type testScrollbackCache struct {
	bySession map[string][]cache.Entry
}

func newTestScrollbackCache() *testScrollbackCache {
	return &testScrollbackCache{bySession: map[string][]cache.Entry{}}
}

func (c *testScrollbackCache) Append(sessionID string, entry cache.Entry) error {
	c.bySession[sessionID] = append(c.bySession[sessionID], entry)
	return nil
}

func (c *testScrollbackCache) Get(sessionID string, offset, limit int) ([]cache.Entry, error) {
	entries := c.bySession[sessionID]
	if offset < 0 {
		offset = 0
	}
	if offset >= len(entries) {
		return []cache.Entry{}, nil
	}
	end := len(entries)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	out := make([]cache.Entry, end-offset)
	copy(out, entries[offset:end])
	return out, nil
}

func (c *testScrollbackCache) Trim(sessionID string, maxEntries int) error {
	entries := c.bySession[sessionID]
	if maxEntries <= 0 {
		c.bySession[sessionID] = []cache.Entry{}
		return nil
	}
	if len(entries) <= maxEntries {
		return nil
	}
	c.bySession[sessionID] = append([]cache.Entry(nil), entries[len(entries)-maxEntries:]...)
	return nil
}

func (c *testScrollbackCache) Clear(sessionID string) error {
	delete(c.bySession, sessionID)
	return nil
}

func (c *testScrollbackCache) Close() error {
	return nil
}
