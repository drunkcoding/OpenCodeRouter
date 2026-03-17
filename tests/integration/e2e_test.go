package integration_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"opencoderouter/internal/api"
	"opencoderouter/internal/auth"
	"opencoderouter/internal/session"
)

type fakeTerminalConn struct {
	mu      sync.Mutex
	onClose func()
	closed  bool
}

func (c *fakeTerminalConn) Read(_ []byte) (int, error)  { return 0, io.EOF }
func (c *fakeTerminalConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *fakeTerminalConn) Resize(_, _ int) error       { return nil }

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

type fakeSessionManager struct {
	mu        sync.Mutex
	sessions  map[string]session.SessionHandle
	health    map[string]session.HealthStatus
	nextID    int
	createErr error
}

func newFakeSessionManager() *fakeSessionManager {
	return &fakeSessionManager{
		sessions: make(map[string]session.SessionHandle),
		health:   make(map[string]session.HealthStatus),
	}
}

func (m *fakeSessionManager) Create(_ context.Context, opts session.CreateOpts) (*session.SessionHandle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createErr != nil {
		return nil, m.createErr
	}

	if strings.TrimSpace(opts.WorkspacePath) == "" {
		return nil, session.ErrWorkspacePathRequired
	}
	abs, err := filepath.Abs(opts.WorkspacePath)
	if err != nil {
		return nil, session.ErrWorkspacePathInvalid
	}
	if stat, err := os.Stat(abs); err != nil || !stat.IsDir() {
		return nil, session.ErrWorkspacePathInvalid
	}

	m.nextID++
	id := "session-" + time.Now().UTC().Format("150405") + "-" + string(rune('a'+m.nextID))
	now := time.Now().UTC()
	h := session.SessionHandle{
		ID:            id,
		DaemonPort:    32000 + m.nextID,
		WorkspacePath: abs,
		Status:        session.SessionStatusActive,
		CreatedAt:     now,
		LastActivity:  now,
		Labels:        cloneLabels(opts.Labels),
	}
	m.sessions[id] = h
	m.health[id] = session.HealthStatus{State: session.HealthStateHealthy, LastCheck: now}
	copy := h
	return &copy, nil
}

func (m *fakeSessionManager) Get(id string) (*session.SessionHandle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.sessions[id]
	if !ok {
		return nil, session.ErrSessionNotFound
	}
	copy := h
	copy.Labels = cloneLabels(h.Labels)
	return &copy, nil
}

func (m *fakeSessionManager) List(filter session.SessionListFilter) ([]session.SessionHandle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]session.SessionHandle, 0, len(m.sessions))
	for _, h := range m.sessions {
		if filter.Status != "" && h.Status != filter.Status {
			continue
		}
		copy := h
		copy.Labels = cloneLabels(h.Labels)
		out = append(out, copy)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *fakeSessionManager) Stop(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.sessions[id]
	if !ok {
		return session.ErrSessionNotFound
	}
	h.Status = session.SessionStatusStopped
	h.LastActivity = time.Now().UTC()
	m.sessions[id] = h
	st := m.health[id]
	st.State = session.HealthStateUnknown
	st.LastCheck = time.Now().UTC()
	m.health[id] = st
	return nil
}

func (m *fakeSessionManager) Restart(_ context.Context, id string) (*session.SessionHandle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.sessions[id]
	if !ok {
		return nil, session.ErrSessionNotFound
	}
	h.Status = session.SessionStatusActive
	h.LastActivity = time.Now().UTC()
	m.sessions[id] = h
	st := m.health[id]
	st.State = session.HealthStateHealthy
	st.LastCheck = time.Now().UTC()
	m.health[id] = st
	copy := h
	return &copy, nil
}

func (m *fakeSessionManager) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[id]; !ok {
		return session.ErrSessionNotFound
	}
	delete(m.sessions, id)
	delete(m.health, id)
	return nil
}

func (m *fakeSessionManager) AttachTerminal(_ context.Context, id string) (session.TerminalConn, error) {
	m.mu.Lock()
	h, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return nil, session.ErrSessionNotFound
	}
	h.AttachedClients++
	h.LastActivity = time.Now().UTC()
	m.sessions[id] = h
	m.mu.Unlock()

	return &fakeTerminalConn{onClose: func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		h, ok := m.sessions[id]
		if !ok {
			return
		}
		if h.AttachedClients > 0 {
			h.AttachedClients--
		}
		h.LastActivity = time.Now().UTC()
		m.sessions[id] = h
	}}, nil
}

func (m *fakeSessionManager) Health(_ context.Context, id string) (session.HealthStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.health[id]
	if !ok {
		return session.HealthStatus{}, session.ErrSessionNotFound
	}
	return h, nil
}

func cloneLabels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func jsonRequest(t *testing.T, client *http.Client, method, url string, payload any) *http.Response {
	t.Helper()
	var body io.Reader
	if payload != nil {
		buf, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		body = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

type sessionView struct {
	ID              string                `json:"id"`
	WorkspacePath   string                `json:"workspacePath"`
	Status          session.SessionStatus `json:"status"`
	AttachedClients int                   `json:"attachedClients"`
}

func decode[T any](t *testing.T, r io.Reader) T {
	t.Helper()
	var out T
	if err := json.NewDecoder(r).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}

func TestE2ESessionLifecycleAndWiring(t *testing.T) {
	mgr := newFakeSessionManager()
	bus := session.NewEventBus(16)

	r := api.NewRouter(api.RouterConfig{
		SessionManager:  mgr,
		SessionEventBus: bus,
		Fallback:        http.NotFoundHandler(),
		AuthConfig:      auth.Defaults(),
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	ws1 := t.TempDir()
	ws2 := t.TempDir()

	create1 := jsonRequest(t, srv.Client(), http.MethodPost, srv.URL+"/api/sessions", map[string]any{"workspacePath": ws1})
	if create1.StatusCode != http.StatusCreated {
		defer create1.Body.Close()
		t.Fatalf("create1 status=%d want=%d", create1.StatusCode, http.StatusCreated)
	}
	s1 := decode[sessionView](t, create1.Body)
	_ = create1.Body.Close()

	create2 := jsonRequest(t, srv.Client(), http.MethodPost, srv.URL+"/api/sessions", map[string]any{"workspacePath": ws2})
	if create2.StatusCode != http.StatusCreated {
		defer create2.Body.Close()
		t.Fatalf("create2 status=%d want=%d", create2.StatusCode, http.StatusCreated)
	}
	s2 := decode[sessionView](t, create2.Body)
	_ = create2.Body.Close()

	if s1.ID == "" || s2.ID == "" || s1.ID == s2.ID {
		t.Fatalf("invalid session ids: s1=%q s2=%q", s1.ID, s2.ID)
	}

	list := jsonRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/sessions", nil)
	if list.StatusCode != http.StatusOK {
		defer list.Body.Close()
		t.Fatalf("list status=%d want=%d", list.StatusCode, http.StatusOK)
	}
	all := decode[[]sessionView](t, list.Body)
	_ = list.Body.Close()
	if len(all) != 2 {
		t.Fatalf("list len=%d want=2", len(all))
	}

	attach := jsonRequest(t, srv.Client(), http.MethodPost, srv.URL+"/api/sessions/"+s1.ID+"/attach", nil)
	if attach.StatusCode != http.StatusOK {
		defer attach.Body.Close()
		t.Fatalf("attach status=%d want=%d", attach.StatusCode, http.StatusOK)
	}
	attached := decode[sessionView](t, attach.Body)
	_ = attach.Body.Close()
	if attached.AttachedClients != 1 {
		t.Fatalf("attached clients=%d want=1", attached.AttachedClients)
	}

	get2 := jsonRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/sessions/"+s2.ID, nil)
	if get2.StatusCode != http.StatusOK {
		defer get2.Body.Close()
		t.Fatalf("get2 status=%d want=%d", get2.StatusCode, http.StatusOK)
	}
	state2 := decode[sessionView](t, get2.Body)
	_ = get2.Body.Close()
	if state2.AttachedClients != 0 {
		t.Fatalf("session2 attached clients=%d want=0", state2.AttachedClients)
	}

	detach := jsonRequest(t, srv.Client(), http.MethodPost, srv.URL+"/api/sessions/"+s1.ID+"/detach", nil)
	if detach.StatusCode != http.StatusOK {
		defer detach.Body.Close()
		t.Fatalf("detach status=%d want=%d", detach.StatusCode, http.StatusOK)
	}
	detached := decode[sessionView](t, detach.Body)
	_ = detach.Body.Close()
	if detached.AttachedClients != 0 {
		t.Fatalf("detached clients=%d want=0", detached.AttachedClients)
	}

	del := jsonRequest(t, srv.Client(), http.MethodDelete, srv.URL+"/api/sessions/"+s1.ID, nil)
	if del.StatusCode != http.StatusNoContent {
		defer del.Body.Close()
		t.Fatalf("delete status=%d want=%d", del.StatusCode, http.StatusNoContent)
	}
	_ = del.Body.Close()

	missing := jsonRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/sessions/"+s1.ID, nil)
	if missing.StatusCode != http.StatusNotFound {
		defer missing.Body.Close()
		t.Fatalf("missing status=%d want=%d", missing.StatusCode, http.StatusNotFound)
	}
	_ = missing.Body.Close()

	eventsReq, err := http.NewRequest(http.MethodGet, srv.URL+"/api/events", nil)
	if err != nil {
		t.Fatalf("events request: %v", err)
	}
	eventsResp, err := srv.Client().Do(eventsReq)
	if err != nil {
		t.Fatalf("events call: %v", err)
	}
	if eventsResp.StatusCode != http.StatusOK {
		defer eventsResp.Body.Close()
		t.Fatalf("events status=%d want=%d", eventsResp.StatusCode, http.StatusOK)
	}
	if got := eventsResp.Header.Get("Content-Type"); got != "text/event-stream" {
		defer eventsResp.Body.Close()
		t.Fatalf("events content-type=%q want=%q", got, "text/event-stream")
	}
	_ = eventsResp.Body.Close()

	terminalReq, err := http.NewRequest(http.MethodGet, srv.URL+"/ws/terminal/"+s2.ID, nil)
	if err != nil {
		t.Fatalf("terminal request: %v", err)
	}
	terminalResp, err := srv.Client().Do(terminalReq)
	if err != nil {
		t.Fatalf("terminal call: %v", err)
	}
	if terminalResp.StatusCode != http.StatusBadRequest {
		defer terminalResp.Body.Close()
		t.Fatalf("terminal status=%d want=%d", terminalResp.StatusCode, http.StatusBadRequest)
	}
	_ = terminalResp.Body.Close()
}

func TestE2ERouterAuthMiddleware(t *testing.T) {
	mgr := newFakeSessionManager()
	ws := t.TempDir()
	if _, err := mgr.Create(context.Background(), session.CreateOpts{WorkspacePath: ws}); err != nil {
		t.Fatalf("seed create: %v", err)
	}

	authCfg := auth.Defaults()
	authCfg.Enabled = true
	authCfg.BearerTokens = []string{"integration-secret"}

	srv := httptest.NewServer(api.NewRouter(api.RouterConfig{
		SessionManager:  mgr,
		SessionEventBus: session.NewEventBus(8),
		AuthConfig:      authCfg,
		Fallback:        http.NotFoundHandler(),
	}))
	defer srv.Close()

	unauth := jsonRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/sessions", nil)
	if unauth.StatusCode != http.StatusUnauthorized {
		defer unauth.Body.Close()
		t.Fatalf("unauthorized status=%d want=%d", unauth.StatusCode, http.StatusUnauthorized)
	}
	_ = unauth.Body.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/sessions", nil)
	if err != nil {
		t.Fatalf("new auth request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer integration-secret")
	authResp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("authorized request: %v", err)
	}
	if authResp.StatusCode != http.StatusOK {
		defer authResp.Body.Close()
		t.Fatalf("authorized status=%d want=%d", authResp.StatusCode, http.StatusOK)
	}
	_ = authResp.Body.Close()
}

func TestE2ERealRuntimeGuard(t *testing.T) {
	if strings.TrimSpace(os.Getenv("RUN_REAL_DAEMON_E2E")) == "" {
		t.Skip("set RUN_REAL_DAEMON_E2E=1 to run daemon-dependent integration checks")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skipf("opencode binary unavailable: %v", err)
	}
	t.Skip("real daemon integration scaffold guard in place; runtime assertions intentionally skipped in default CI")
}

func TestE2EMainWiringAssumptions(t *testing.T) {
	root := repoRoot(t)
	contents, err := os.ReadFile(filepath.Join(root, "main.go"))
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	text := string(contents)
	if !strings.Contains(text, "runRouter(cfg, projectPaths, logger)") {
		t.Fatalf("main.go missing runRouter decomposition marker")
	}

	appContents, err := os.ReadFile(filepath.Join(root, "app.go"))
	if err != nil {
		t.Fatalf("read app.go: %v", err)
	}
	appText := string(appContents)
	required := []string{
		"session.NewManager(session.ManagerConfig{",
		"api.NewRouter(api.RouterConfig{",
		"SessionManager:  sessionMgr",
		"SessionEventBus: eventBus",
		"AuthConfig:      auth.LoadFromEnv()",
	}
	for _, needle := range required {
		if !strings.Contains(appText, needle) {
			t.Fatalf("app.go missing wiring marker: %q", needle)
		}
	}

	routerText, err := os.ReadFile(filepath.Join(root, "internal", "api", "router.go"))
	if err != nil {
		t.Fatalf("read internal/api/router.go: %v", err)
	}
	rt := string(routerText)
	routes := []string{
		"NewSessionsHandler",
		"NewEventsHandler",
		"terminal.NewHandler",
		"auth.Middleware",
	}
	for _, needle := range routes {
		if !strings.Contains(rt, needle) {
			t.Fatalf("router.go missing mount marker: %q", needle)
		}
	}
}

func TestE2EErrorShapeOnMissingSession(t *testing.T) {
	mgr := newFakeSessionManager()
	srv := httptest.NewServer(api.NewRouter(api.RouterConfig{
		SessionManager:  mgr,
		SessionEventBus: session.NewEventBus(8),
		AuthConfig:      auth.Defaults(),
		Fallback:        http.NotFoundHandler(),
	}))
	defer srv.Close()

	resp := jsonRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/sessions/does-not-exist", nil)
	if resp.StatusCode != http.StatusNotFound {
		defer resp.Body.Close()
		t.Fatalf("status=%d want=%d", resp.StatusCode, http.StatusNotFound)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		_ = resp.Body.Close()
		t.Fatalf("decode error payload: %v", err)
	}
	_ = resp.Body.Close()
	if payload["code"] != "SESSION_NOT_FOUND" {
		t.Fatalf("code=%v want=%q", payload["code"], "SESSION_NOT_FOUND")
	}
	if _, ok := payload["error"]; !ok {
		t.Fatalf("missing error field in payload: %#v", payload)
	}
}

func TestE2EAttachDetachPathShape(t *testing.T) {
	mgr := newFakeSessionManager()
	ws := t.TempDir()
	h, err := mgr.Create(context.Background(), session.CreateOpts{WorkspacePath: ws})
	if err != nil {
		t.Fatalf("seed create: %v", err)
	}

	srv := httptest.NewServer(api.NewRouter(api.RouterConfig{
		SessionManager:  mgr,
		SessionEventBus: session.NewEventBus(8),
		AuthConfig:      auth.Defaults(),
		Fallback:        http.NotFoundHandler(),
	}))
	defer srv.Close()

	attach := jsonRequest(t, srv.Client(), http.MethodPost, srv.URL+"/api/sessions/"+h.ID+"/attach", nil)
	if attach.StatusCode != http.StatusOK {
		defer attach.Body.Close()
		t.Fatalf("attach status=%d want=%d", attach.StatusCode, http.StatusOK)
	}
	attached := decode[sessionView](t, attach.Body)
	_ = attach.Body.Close()

	detach := jsonRequest(t, srv.Client(), http.MethodPost, srv.URL+"/api/sessions/"+h.ID+"/detach", nil)
	if detach.StatusCode != http.StatusOK {
		defer detach.Body.Close()
		t.Fatalf("detach status=%d want=%d", detach.StatusCode, http.StatusOK)
	}
	detached := decode[sessionView](t, detach.Body)
	_ = detach.Body.Close()

	if attached.AttachedClients != 1 || detached.AttachedClients != 0 {
		t.Fatalf("unexpected attach/detach shape attached=%d detached=%d", attached.AttachedClients, detached.AttachedClients)
	}
}

func TestE2ECreateSessionPortExhaustionErrorPath(t *testing.T) {
	mgr := newFakeSessionManager()
	mgr.createErr = session.ErrNoAvailableSessionPorts

	srv := httptest.NewServer(api.NewRouter(api.RouterConfig{
		SessionManager:  mgr,
		SessionEventBus: session.NewEventBus(8),
		AuthConfig:      auth.Defaults(),
		Fallback:        http.NotFoundHandler(),
	}))
	defer srv.Close()

	resp := jsonRequest(t, srv.Client(), http.MethodPost, srv.URL+"/api/sessions", map[string]any{
		"workspacePath": t.TempDir(),
	})
	if resp.StatusCode != http.StatusServiceUnavailable {
		defer resp.Body.Close()
		t.Fatalf("status=%d want=%d", resp.StatusCode, http.StatusServiceUnavailable)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		_ = resp.Body.Close()
		t.Fatalf("decode response: %v", err)
	}
	_ = resp.Body.Close()
	if payload["code"] != "NO_AVAILABLE_SESSION_PORTS" {
		t.Fatalf("code=%v want=%q", payload["code"], "NO_AVAILABLE_SESSION_PORTS")
	}
	errText, _ := payload["error"].(string)
	if !strings.Contains(strings.ToLower(errText), "port") {
		t.Fatalf("error text=%q want descriptive port message", errText)
	}
}

func TestE2EHealthFailureEventPath(t *testing.T) {
	mgr := newFakeSessionManager()
	bus := session.NewEventBus(16)

	srv := httptest.NewServer(api.NewRouter(api.RouterConfig{
		SessionManager:  mgr,
		SessionEventBus: bus,
		AuthConfig:      auth.Defaults(),
		Fallback:        http.NotFoundHandler(),
	}))
	defer srv.Close()

	resp := jsonRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/events", nil)
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		t.Fatalf("events status=%d want=%d", resp.StatusCode, http.StatusOK)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	_ = readSSEUntil(t, reader, 2*time.Second, func(frame parsedSSEFrame) bool {
		return frame.Retry != ""
	})

	now := time.Now().UTC()
	handle := session.SessionHandle{
		ID:            "session-health-failure",
		DaemonPort:    32042,
		WorkspacePath: "/tmp/workspace",
		Status:        session.SessionStatusError,
		CreatedAt:     now,
		LastActivity:  now,
	}
	if err := bus.Publish(session.SessionHealthChanged{
		At:       now,
		Session:  handle,
		Previous: session.HealthStatus{State: session.HealthStateHealthy},
		Current:  session.HealthStatus{State: session.HealthStateUnhealthy, Error: "probe timeout"},
	}); err != nil {
		t.Fatalf("publish health event: %v", err)
	}

	frame := readSSEUntil(t, reader, 2*time.Second, func(frame parsedSSEFrame) bool {
		return frame.Event == "session.health" && len(frame.Data) > 0
	})

	var envelope map[string]any
	if err := json.Unmarshal([]byte(strings.Join(frame.Data, "\n")), &envelope); err != nil {
		t.Fatalf("decode session.health payload: %v", err)
	}
	if envelope["type"] != "session.health" {
		t.Fatalf("type=%v want=session.health", envelope["type"])
	}
	payload, _ := envelope["payload"].(map[string]any)
	current, _ := payload["Current"].(map[string]any)
	if current["State"] != string(session.HealthStateUnhealthy) {
		t.Fatalf("current state=%v want=%q", current["State"], session.HealthStateUnhealthy)
	}
	if current["Error"] != "probe timeout" {
		t.Fatalf("current error=%v want=%q", current["Error"], "probe timeout")
	}
}

func TestE2EOfflineIndicatorBehaviorContract(t *testing.T) {
	root := repoRoot(t)
	indexBytes, err := os.ReadFile(filepath.Join(root, "web", "index.html"))
	if err != nil {
		t.Fatalf("read web/index.html: %v", err)
	}
	appBytes, err := os.ReadFile(filepath.Join(root, "web", "js", "api.js"))
	if err != nil {
		t.Fatalf("read web/js/api.js: %v", err)
	}

	indexText := string(indexBytes)
	appText := string(appBytes)

	checks := []string{
		"● OFFLINE",
		"● DISCONNECTED",
		"● RECONNECTING",
		"setSSEIndicator('disconnected', 'bootstrap failed')",
	}

	if !strings.Contains(indexText, checks[0]) {
		t.Fatalf("index.html missing offline default indicator marker %q", checks[0])
	}
	if !strings.Contains(indexText, "<script type=\"module\" src=\"/js/main.js\"></script>") {
		t.Fatalf("index.html missing module entrypoint marker %q", "/js/main.js")
	}
	for _, marker := range checks[1:] {
		if !strings.Contains(appText, marker) {
			t.Fatalf("web/js/api.js missing offline/reconnect marker %q", marker)
		}
	}
}

func TestE2EMultiSessionIndependenceSanity(t *testing.T) {
	mgr := newFakeSessionManager()
	ws1 := t.TempDir()
	ws2 := t.TempDir()
	a, err := mgr.Create(context.Background(), session.CreateOpts{WorkspacePath: ws1})
	if err != nil {
		t.Fatalf("seed a: %v", err)
	}
	b, err := mgr.Create(context.Background(), session.CreateOpts{WorkspacePath: ws2})
	if err != nil {
		t.Fatalf("seed b: %v", err)
	}

	srv := httptest.NewServer(api.NewRouter(api.RouterConfig{
		SessionManager:  mgr,
		SessionEventBus: session.NewEventBus(8),
		AuthConfig:      auth.Defaults(),
		Fallback:        http.NotFoundHandler(),
	}))
	defer srv.Close()

	stopA := jsonRequest(t, srv.Client(), http.MethodPost, srv.URL+"/api/sessions/"+a.ID+"/stop", nil)
	if stopA.StatusCode != http.StatusOK {
		defer stopA.Body.Close()
		t.Fatalf("stop a status=%d want=%d", stopA.StatusCode, http.StatusOK)
	}
	_ = stopA.Body.Close()

	getA := jsonRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/sessions/"+a.ID, nil)
	getB := jsonRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/sessions/"+b.ID, nil)
	if getA.StatusCode != http.StatusOK || getB.StatusCode != http.StatusOK {
		defer getA.Body.Close()
		defer getB.Body.Close()
		t.Fatalf("get statuses a=%d b=%d", getA.StatusCode, getB.StatusCode)
	}
	stateA := decode[sessionView](t, getA.Body)
	stateB := decode[sessionView](t, getB.Body)
	_ = getA.Body.Close()
	_ = getB.Body.Close()

	if stateA.Status != session.SessionStatusStopped {
		t.Fatalf("session A status=%s want=%s", stateA.Status, session.SessionStatusStopped)
	}
	if stateB.Status != session.SessionStatusActive {
		t.Fatalf("session B status=%s want=%s", stateB.Status, session.SessionStatusActive)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

type parsedSSEFrame struct {
	ID       string
	Event    string
	Data     []string
	Comments []string
	Retry    string
}

func readSSEUntil(t *testing.T, reader *bufio.Reader, timeout time.Duration, match func(parsedSSEFrame) bool) parsedSSEFrame {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("timed out waiting for matching SSE frame after %s", timeout)
		}
		frame := readSSEFrame(t, reader, remaining)
		if match(frame) {
			return frame
		}
	}
}

func readSSEFrame(t *testing.T, reader *bufio.Reader, timeout time.Duration) parsedSSEFrame {
	t.Helper()
	type result struct {
		frame parsedSSEFrame
		err   error
	}
	resultCh := make(chan result, 1)

	go func() {
		var frame parsedSSEFrame
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				resultCh <- result{err: err}
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if frame.ID != "" || frame.Event != "" || frame.Retry != "" || len(frame.Data) > 0 || len(frame.Comments) > 0 {
					resultCh <- result{frame: frame}
					return
				}
				continue
			}

			switch {
			case strings.HasPrefix(line, "id:"):
				frame.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
			case strings.HasPrefix(line, "event:"):
				frame.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				frame.Data = append(frame.Data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			case strings.HasPrefix(line, "retry:"):
				frame.Retry = strings.TrimSpace(strings.TrimPrefix(line, "retry:"))
			case strings.HasPrefix(line, ":"):
				frame.Comments = append(frame.Comments, strings.TrimSpace(strings.TrimPrefix(line, ":")))
			}
		}
	}()

	select {
	case res := <-resultCh:
		if res.err != nil {
			if res.err == io.EOF {
				t.Fatal("unexpected EOF while reading SSE frame")
			}
			t.Fatalf("read SSE frame: %v", res.err)
		}
		if res.frame.ID != "" {
			if _, err := strconv.ParseInt(res.frame.ID, 10, 64); err != nil {
				t.Fatalf("invalid SSE id format %q: %v", res.frame.ID, err)
			}
		}
		return res.frame
	case <-time.After(timeout):
		t.Fatalf("timed out reading SSE frame after %s", timeout)
	}

	return parsedSSEFrame{}
}
