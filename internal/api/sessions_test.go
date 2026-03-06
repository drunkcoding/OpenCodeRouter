package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"opencoderouter/internal/session"
)

type fakeTerminalConn struct {
	mu      sync.Mutex
	onClose func()
	closed  bool
}

func (c *fakeTerminalConn) Read(_ []byte) (int, error) { return 0, io.EOF }

func (c *fakeTerminalConn) Write(p []byte) (int, error) { return len(p), nil }

func (c *fakeTerminalConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	onClose := c.onClose
	c.mu.Unlock()
	if onClose != nil {
		onClose()
	}
	return nil
}

func (c *fakeTerminalConn) Resize(_, _ int) error { return nil }

type fakeStatefulSessionManager struct {
	mu         sync.Mutex
	sessions   map[string]session.SessionHandle
	health     map[string]session.HealthStatus
	nextID     int
	createErr  error
	listErr    error
	getErr     error
	stopErr    error
	restartErr error
	deleteErr  error
	attachErr  error
	healthErr  error
}

func newFakeStatefulSessionManager() *fakeStatefulSessionManager {
	return &fakeStatefulSessionManager{
		sessions: make(map[string]session.SessionHandle),
		health:   make(map[string]session.HealthStatus),
	}
}

func (m *fakeStatefulSessionManager) Create(_ context.Context, opts session.CreateOpts) (*session.SessionHandle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.createErr != nil {
		return nil, m.createErr
	}
	if strings.TrimSpace(opts.WorkspacePath) == "" {
		return nil, session.ErrWorkspacePathRequired
	}

	m.nextID++
	id := "session-" + time.Now().UTC().Format("150405") + "-" + string(rune('a'+m.nextID))
	now := time.Now().UTC()
	handle := session.SessionHandle{
		ID:            id,
		DaemonPort:    32000 + m.nextID,
		WorkspacePath: opts.WorkspacePath,
		Status:        session.SessionStatusActive,
		CreatedAt:     now,
		LastActivity:  now,
		Labels:        cloneLabels(opts.Labels),
	}
	m.sessions[id] = handle
	m.health[id] = session.HealthStatus{State: session.HealthStateHealthy, LastCheck: now}
	clone := handle
	clone.Labels = cloneLabels(handle.Labels)
	return &clone, nil
}

func (m *fakeStatefulSessionManager) Get(id string) (*session.SessionHandle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.getErr != nil {
		return nil, m.getErr
	}
	handle, ok := m.sessions[id]
	if !ok {
		return nil, session.ErrSessionNotFound
	}
	clone := handle
	clone.Labels = cloneLabels(handle.Labels)
	return &clone, nil
}

func (m *fakeStatefulSessionManager) List(filter session.SessionListFilter) ([]session.SessionHandle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.listErr != nil {
		return nil, m.listErr
	}

	out := make([]session.SessionHandle, 0, len(m.sessions))
	for _, handle := range m.sessions {
		if filter.Status != "" && handle.Status != filter.Status {
			continue
		}
		clone := handle
		clone.Labels = cloneLabels(handle.Labels)
		out = append(out, clone)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (m *fakeStatefulSessionManager) Stop(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.stopErr != nil {
		return m.stopErr
	}
	handle, ok := m.sessions[id]
	if !ok {
		return session.ErrSessionNotFound
	}
	handle.Status = session.SessionStatusStopped
	handle.LastActivity = time.Now().UTC()
	m.sessions[id] = handle
	health := m.health[id]
	health.State = session.HealthStateUnknown
	health.LastCheck = time.Now().UTC()
	m.health[id] = health
	return nil
}

func (m *fakeStatefulSessionManager) Restart(_ context.Context, id string) (*session.SessionHandle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.restartErr != nil {
		return nil, m.restartErr
	}
	handle, ok := m.sessions[id]
	if !ok {
		return nil, session.ErrSessionNotFound
	}
	handle.Status = session.SessionStatusActive
	handle.LastActivity = time.Now().UTC()
	m.sessions[id] = handle
	health := m.health[id]
	health.State = session.HealthStateHealthy
	health.LastCheck = time.Now().UTC()
	health.Error = ""
	m.health[id] = health
	clone := handle
	clone.Labels = cloneLabels(handle.Labels)
	return &clone, nil
}

func (m *fakeStatefulSessionManager) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.deleteErr != nil {
		return m.deleteErr
	}
	if _, ok := m.sessions[id]; !ok {
		return session.ErrSessionNotFound
	}
	delete(m.sessions, id)
	delete(m.health, id)
	return nil
}

func (m *fakeStatefulSessionManager) AttachTerminal(_ context.Context, id string) (session.TerminalConn, error) {
	m.mu.Lock()
	if m.attachErr != nil {
		m.mu.Unlock()
		return nil, m.attachErr
	}
	handle, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return nil, session.ErrSessionNotFound
	}
	handle.AttachedClients++
	handle.LastActivity = time.Now().UTC()
	m.sessions[id] = handle
	m.mu.Unlock()

	return &fakeTerminalConn{onClose: func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		handle, ok := m.sessions[id]
		if !ok {
			return
		}
		if handle.AttachedClients > 0 {
			handle.AttachedClients--
		}
		handle.LastActivity = time.Now().UTC()
		m.sessions[id] = handle
	}}, nil
}

func (m *fakeStatefulSessionManager) Health(_ context.Context, id string) (session.HealthStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.healthErr != nil {
		return session.HealthStatus{}, m.healthErr
	}
	health, ok := m.health[id]
	if !ok {
		return session.HealthStatus{}, session.ErrSessionNotFound
	}
	return health, nil
}

func cloneLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(labels))
	for k, v := range labels {
		cloned[k] = v
	}
	return cloned
}

func newSessionsTestServer(t *testing.T, mgr session.SessionManager) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	NewSessionsHandler(SessionsHandlerConfig{SessionManager: mgr}).Register(mux)
	return httptest.NewServer(mux)
}

func doJSONRequest(t *testing.T, client *http.Client, method, url string, body any) *http.Response {
	t.Helper()

	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(buf)
	}

	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

func decodeResponseJSON[T any](t *testing.T, body io.Reader) T {
	t.Helper()
	var out T
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}

func assertErrorShape(t *testing.T, resp *http.Response, expectedStatus int, expectedCode string) {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != expectedStatus {
		t.Fatalf("status=%d want=%d", resp.StatusCode, expectedStatus)
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if got := payload["code"]; got != expectedCode {
		t.Fatalf("error code=%v want=%s", got, expectedCode)
	}
	if _, ok := payload["error"]; !ok {
		t.Fatalf("expected error field in payload: %#v", payload)
	}
	if len(payload) != 2 {
		t.Fatalf("expected payload shape {error,code}, got %#v", payload)
	}
}

func TestSessionsLifecycleEndpoints(t *testing.T) {
	mgr := newFakeStatefulSessionManager()
	srv := newSessionsTestServer(t, mgr)
	defer srv.Close()

	workspace := t.TempDir()

	createResp := doJSONRequest(t, srv.Client(), http.MethodPost, srv.URL+"/api/sessions", map[string]any{
		"workspacePath": workspace,
		"label":         "api-test",
	})
	if createResp.StatusCode != http.StatusCreated {
		defer createResp.Body.Close()
		t.Fatalf("create status=%d want=%d", createResp.StatusCode, http.StatusCreated)
	}
	created := decodeResponseJSON[sessionView](t, createResp.Body)
	_ = createResp.Body.Close()
	if created.ID == "" {
		t.Fatal("expected created session id")
	}
	if created.Labels["label"] != "api-test" {
		t.Fatalf("expected label api-test, got %#v", created.Labels)
	}

	listResp := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/sessions", nil)
	if listResp.StatusCode != http.StatusOK {
		defer listResp.Body.Close()
		t.Fatalf("list status=%d want=%d", listResp.StatusCode, http.StatusOK)
	}
	listed := decodeResponseJSON[[]sessionView](t, listResp.Body)
	_ = listResp.Body.Close()
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("unexpected list response: %#v", listed)
	}

	getResp := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/sessions/"+created.ID, nil)
	if getResp.StatusCode != http.StatusOK {
		defer getResp.Body.Close()
		t.Fatalf("get status=%d want=%d", getResp.StatusCode, http.StatusOK)
	}
	detail := decodeResponseJSON[sessionView](t, getResp.Body)
	_ = getResp.Body.Close()
	if detail.Health.State != session.HealthStateHealthy {
		t.Fatalf("expected healthy state, got %s", detail.Health.State)
	}

	stopResp := doJSONRequest(t, srv.Client(), http.MethodPost, srv.URL+"/api/sessions/"+created.ID+"/stop", nil)
	if stopResp.StatusCode != http.StatusOK {
		defer stopResp.Body.Close()
		t.Fatalf("stop status=%d want=%d", stopResp.StatusCode, http.StatusOK)
	}
	stopped := decodeResponseJSON[sessionView](t, stopResp.Body)
	_ = stopResp.Body.Close()
	if stopped.Status != session.SessionStatusStopped {
		t.Fatalf("stop status field=%s want=%s", stopped.Status, session.SessionStatusStopped)
	}

	restartResp := doJSONRequest(t, srv.Client(), http.MethodPost, srv.URL+"/api/sessions/"+created.ID+"/restart", nil)
	if restartResp.StatusCode != http.StatusOK {
		defer restartResp.Body.Close()
		t.Fatalf("restart status=%d want=%d", restartResp.StatusCode, http.StatusOK)
	}
	restarted := decodeResponseJSON[sessionView](t, restartResp.Body)
	_ = restartResp.Body.Close()
	if restarted.Status != session.SessionStatusActive {
		t.Fatalf("restart status field=%s want=%s", restarted.Status, session.SessionStatusActive)
	}

	stopAgainResp := doJSONRequest(t, srv.Client(), http.MethodPost, srv.URL+"/api/sessions/"+created.ID+"/stop", nil)
	if stopAgainResp.StatusCode != http.StatusOK {
		defer stopAgainResp.Body.Close()
		t.Fatalf("second stop status=%d want=%d", stopAgainResp.StatusCode, http.StatusOK)
	}
	_ = stopAgainResp.Body.Close()

	startResp := doJSONRequest(t, srv.Client(), http.MethodPost, srv.URL+"/api/sessions/"+created.ID+"/start", nil)
	if startResp.StatusCode != http.StatusOK {
		defer startResp.Body.Close()
		t.Fatalf("start status=%d want=%d", startResp.StatusCode, http.StatusOK)
	}
	started := decodeResponseJSON[sessionView](t, startResp.Body)
	_ = startResp.Body.Close()
	if started.Status != session.SessionStatusActive {
		t.Fatalf("start status field=%s want=%s", started.Status, session.SessionStatusActive)
	}

	attachResp := doJSONRequest(t, srv.Client(), http.MethodPost, srv.URL+"/api/sessions/"+created.ID+"/attach", nil)
	if attachResp.StatusCode != http.StatusOK {
		defer attachResp.Body.Close()
		t.Fatalf("attach status=%d want=%d", attachResp.StatusCode, http.StatusOK)
	}
	attached := decodeResponseJSON[sessionView](t, attachResp.Body)
	_ = attachResp.Body.Close()
	if attached.AttachedClients != 1 {
		t.Fatalf("attached clients=%d want=1", attached.AttachedClients)
	}

	detachResp := doJSONRequest(t, srv.Client(), http.MethodPost, srv.URL+"/api/sessions/"+created.ID+"/detach", nil)
	if detachResp.StatusCode != http.StatusOK {
		defer detachResp.Body.Close()
		t.Fatalf("detach status=%d want=%d", detachResp.StatusCode, http.StatusOK)
	}
	detached := decodeResponseJSON[sessionView](t, detachResp.Body)
	_ = detachResp.Body.Close()
	if detached.AttachedClients != 0 {
		t.Fatalf("attached clients after detach=%d want=0", detached.AttachedClients)
	}

	deleteResp := doJSONRequest(t, srv.Client(), http.MethodDelete, srv.URL+"/api/sessions/"+created.ID, nil)
	if deleteResp.StatusCode != http.StatusNoContent {
		defer deleteResp.Body.Close()
		t.Fatalf("delete status=%d want=%d", deleteResp.StatusCode, http.StatusNoContent)
	}
	_ = deleteResp.Body.Close()

	missingResp := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/sessions/"+created.ID, nil)
	assertErrorShape(t, missingResp, http.StatusNotFound, "SESSION_NOT_FOUND")
}

func TestSessionsCreateValidationErrors(t *testing.T) {
	mgr := newFakeStatefulSessionManager()
	srv := newSessionsTestServer(t, mgr)
	defer srv.Close()

	invalidReq, err := http.NewRequest(http.MethodPost, srv.URL+"/api/sessions", strings.NewReader(`{"workspacePath":`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	invalidReq.Header.Set("Content-Type", "application/json")
	invalidResp, err := srv.Client().Do(invalidReq)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	assertErrorShape(t, invalidResp, http.StatusBadRequest, "INVALID_REQUEST_BODY")

	mgr.createErr = session.ErrWorkspacePathInvalid
	badPathResp := doJSONRequest(t, srv.Client(), http.MethodPost, srv.URL+"/api/sessions", map[string]any{
		"workspacePath": "/path/does/not/exist",
	})
	assertErrorShape(t, badPathResp, http.StatusBadRequest, "WORKSPACE_PATH_INVALID")
}

func TestSessionsCreatePortExhaustionError(t *testing.T) {
	mgr := newFakeStatefulSessionManager()
	srv := newSessionsTestServer(t, mgr)
	defer srv.Close()

	mgr.createErr = session.ErrNoAvailableSessionPorts
	resp := doJSONRequest(t, srv.Client(), http.MethodPost, srv.URL+"/api/sessions", map[string]any{
		"workspacePath": t.TempDir(),
	})
	assertErrorShape(t, resp, http.StatusServiceUnavailable, "NO_AVAILABLE_SESSION_PORTS")
}

func TestSessionsListFilterAndSort(t *testing.T) {
	mgr := newFakeStatefulSessionManager()
	workspace := t.TempDir()
	first, err := mgr.Create(context.Background(), session.CreateOpts{WorkspacePath: workspace})
	if err != nil {
		t.Fatalf("seed first session: %v", err)
	}
	second, err := mgr.Create(context.Background(), session.CreateOpts{WorkspacePath: workspace})
	if err != nil {
		t.Fatalf("seed second session: %v", err)
	}
	if err := mgr.Stop(context.Background(), first.ID); err != nil {
		t.Fatalf("seed stop first: %v", err)
	}

	mgr.mu.Lock()
	h := mgr.sessions[first.ID]
	h.LastActivity = time.Now().UTC().Add(-2 * time.Hour)
	mgr.sessions[first.ID] = h
	h2 := mgr.sessions[second.ID]
	h2.LastActivity = time.Now().UTC().Add(-1 * time.Minute)
	mgr.sessions[second.ID] = h2
	mgr.mu.Unlock()

	srv := newSessionsTestServer(t, mgr)
	defer srv.Close()

	filteredResp := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/sessions?status=stopped", nil)
	if filteredResp.StatusCode != http.StatusOK {
		defer filteredResp.Body.Close()
		t.Fatalf("filtered status=%d want=%d", filteredResp.StatusCode, http.StatusOK)
	}
	filtered := decodeResponseJSON[[]sessionView](t, filteredResp.Body)
	_ = filteredResp.Body.Close()
	if len(filtered) != 1 || filtered[0].ID != first.ID {
		t.Fatalf("unexpected filtered list: %#v", filtered)
	}

	sortedResp := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/sessions?sort=lastActivity", nil)
	if sortedResp.StatusCode != http.StatusOK {
		defer sortedResp.Body.Close()
		t.Fatalf("sorted status=%d want=%d", sortedResp.StatusCode, http.StatusOK)
	}
	sortedViews := decodeResponseJSON[[]sessionView](t, sortedResp.Body)
	_ = sortedResp.Body.Close()
	if len(sortedViews) != 2 || sortedViews[0].ID != second.ID {
		t.Fatalf("unexpected sort ordering: %#v", sortedViews)
	}

	invalidSortResp := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/sessions?sort=random", nil)
	assertErrorShape(t, invalidSortResp, http.StatusBadRequest, "INVALID_SORT")
}

func TestSessionsMethodAndRouteErrors(t *testing.T) {
	mgr := newFakeStatefulSessionManager()
	srv := newSessionsTestServer(t, mgr)
	defer srv.Close()

	methodResp := doJSONRequest(t, srv.Client(), http.MethodPut, srv.URL+"/api/sessions", nil)
	assertErrorShape(t, methodResp, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")

	unknownRouteResp := doJSONRequest(t, srv.Client(), http.MethodPost, srv.URL+"/api/sessions/s-1/unknown", nil)
	assertErrorShape(t, unknownRouteResp, http.StatusNotFound, "NOT_FOUND")

	unknownStartMethod := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/sessions/s-1/start", nil)
	assertErrorShape(t, unknownStartMethod, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")

	mgr.attachErr = session.ErrTerminalAttachDisabled
	workspace := t.TempDir()
	created, err := mgr.Create(context.Background(), session.CreateOpts{WorkspacePath: workspace})
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}

	attachResp := doJSONRequest(t, srv.Client(), http.MethodPost, srv.URL+"/api/sessions/"+created.ID+"/attach", nil)
	assertErrorShape(t, attachResp, http.StatusServiceUnavailable, "TERMINAL_ATTACH_UNAVAILABLE")
}

func TestSessionsHandlerUnavailableManager(t *testing.T) {
	srv := newSessionsTestServer(t, nil)
	defer srv.Close()

	resp := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/sessions", nil)
	assertErrorShape(t, resp, http.StatusServiceUnavailable, "SESSION_MANAGER_UNAVAILABLE")
}

func TestDecodeJSONBodyRejectsTrailingPayload(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"workspacePath":"/tmp"}{}`))
	var body createSessionRequest
	err := decodeJSONBody(req, &body)
	if err == nil {
		t.Fatal("expected decode error for trailing payload")
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("expected structured decode error, got EOF")
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
