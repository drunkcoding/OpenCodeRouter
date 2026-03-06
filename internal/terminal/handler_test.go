package terminal

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"opencoderouter/internal/cache"
	"opencoderouter/internal/session"

	"github.com/gorilla/websocket"
)

type fakeSessionManager struct {
	mu          sync.Mutex
	getFn       func(id string) (*session.SessionHandle, error)
	healthFn    func(ctx context.Context, id string) (session.HealthStatus, error)
	attachFn    func(ctx context.Context, id string) (session.TerminalConn, error)
	getCalls    int
	healthCalls int
	attachCalls int
}

func (m *fakeSessionManager) Create(context.Context, session.CreateOpts) (*session.SessionHandle, error) {
	return nil, errors.New("not implemented")
}

func (m *fakeSessionManager) Get(id string) (*session.SessionHandle, error) {
	m.mu.Lock()
	m.getCalls++
	fn := m.getFn
	m.mu.Unlock()
	if fn == nil {
		return nil, session.ErrSessionNotFound
	}
	return fn(id)
}

func (m *fakeSessionManager) List(session.SessionListFilter) ([]session.SessionHandle, error) {
	return nil, errors.New("not implemented")
}

func (m *fakeSessionManager) Stop(context.Context, string) error {
	return errors.New("not implemented")
}

func (m *fakeSessionManager) Restart(context.Context, string) (*session.SessionHandle, error) {
	return nil, errors.New("not implemented")
}

func (m *fakeSessionManager) Delete(context.Context, string) error {
	return errors.New("not implemented")
}

func (m *fakeSessionManager) AttachTerminal(ctx context.Context, id string) (session.TerminalConn, error) {
	m.mu.Lock()
	m.attachCalls++
	fn := m.attachFn
	m.mu.Unlock()
	if fn == nil {
		return nil, session.ErrSessionNotFound
	}
	return fn(ctx, id)
}

func (m *fakeSessionManager) Health(ctx context.Context, id string) (session.HealthStatus, error) {
	m.mu.Lock()
	m.healthCalls++
	fn := m.healthFn
	m.mu.Unlock()
	if fn == nil {
		return session.HealthStatus{State: session.HealthStateUnknown}, nil
	}
	return fn(ctx, id)
}

func (m *fakeSessionManager) calls() (getCalls, healthCalls, attachCalls int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.getCalls, m.healthCalls, m.attachCalls
}

type terminalResizeCall struct {
	cols int
	rows int
}

type testTerminalConn struct {
	net.Conn

	mu          sync.Mutex
	resizeCalls []terminalResizeCall
	closeCalls  int
	closeOnce   sync.Once
	closeErr    error
}

type testScrollbackCache struct {
	mu      sync.Mutex
	entries map[string][]cache.Entry
}

func newTestScrollbackCache() *testScrollbackCache {
	return &testScrollbackCache{entries: make(map[string][]cache.Entry)}
}

func (c *testScrollbackCache) Append(sessionID string, entry cache.Entry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[sessionID] = append(c.entries[sessionID], entry)
	return nil
}

func (c *testScrollbackCache) Get(sessionID string, offset, limit int) ([]cache.Entry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.entries[sessionID]
	if offset < 0 {
		offset = 0
	}
	if offset >= len(s) {
		return []cache.Entry{}, nil
	}
	end := len(s)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	out := make([]cache.Entry, end-offset)
	copy(out, s[offset:end])
	return out, nil
}

func (c *testScrollbackCache) Trim(sessionID string, maxEntries int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.entries[sessionID]
	if maxEntries <= 0 {
		c.entries[sessionID] = []cache.Entry{}
		return nil
	}
	if len(s) <= maxEntries {
		return nil
	}
	c.entries[sessionID] = append([]cache.Entry(nil), s[len(s)-maxEntries:]...)
	return nil
}

func (c *testScrollbackCache) Clear(sessionID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, sessionID)
	return nil
}

func (c *testScrollbackCache) Close() error { return nil }

func newTerminalConnPair() (*testTerminalConn, net.Conn) {
	client, backend := net.Pipe()
	return &testTerminalConn{Conn: client}, backend
}

func (c *testTerminalConn) Resize(cols, rows int) error {
	c.mu.Lock()
	c.resizeCalls = append(c.resizeCalls, terminalResizeCall{cols: cols, rows: rows})
	c.mu.Unlock()
	return nil
}

func (c *testTerminalConn) Close() error {
	c.mu.Lock()
	c.closeCalls++
	c.mu.Unlock()
	c.closeOnce.Do(func() {
		c.closeErr = c.Conn.Close()
	})
	return c.closeErr
}

func (c *testTerminalConn) resizeSnapshot() []terminalResizeCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]terminalResizeCall, len(c.resizeCalls))
	copy(out, c.resizeCalls)
	return out
}

func (c *testTerminalConn) closeCallCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeCalls
}

func testTerminalLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func wsURL(serverURL, path string) string {
	u, _ := url.Parse(serverURL)
	u.Scheme = "ws"
	u.Path = path
	return u.String()
}

func waitForCondition(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func TestTerminalHandlerInvalidSession(t *testing.T) {
	mgr := &fakeSessionManager{
		getFn: func(id string) (*session.SessionHandle, error) {
			return nil, session.ErrSessionNotFound
		},
	}

	h := NewHandler(HandlerConfig{SessionManager: mgr, Logger: testTerminalLogger()})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ws/terminal/missing-session", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")

	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d", w.Code, http.StatusNotFound)
	}
}

func TestTerminalHandlerUnhealthySession(t *testing.T) {
	mgr := &fakeSessionManager{
		getFn: func(id string) (*session.SessionHandle, error) {
			return &session.SessionHandle{ID: id, Status: session.SessionStatusActive}, nil
		},
		healthFn: func(context.Context, string) (session.HealthStatus, error) {
			return session.HealthStatus{State: session.HealthStateUnhealthy, Error: "daemon not reachable"}, nil
		},
	}

	h := NewHandler(HandlerConfig{SessionManager: mgr, Logger: testTerminalLogger()})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ws/terminal/s-1", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")

	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=%d", w.Code, http.StatusServiceUnavailable)
	}
	_, _, attachCalls := mgr.calls()
	if attachCalls != 0 {
		t.Fatalf("attach calls=%d want=0", attachCalls)
	}
}

func TestTerminalHandlerRequiresUpgrade(t *testing.T) {
	h := NewHandler(HandlerConfig{SessionManager: &fakeSessionManager{}, Logger: testTerminalLogger()})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ws/terminal/s-1", nil)

	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want=%d", w.Code, http.StatusBadRequest)
	}
}

func TestTerminalHandlerInvalidRoute(t *testing.T) {
	h := NewHandler(HandlerConfig{SessionManager: &fakeSessionManager{}, Logger: testTerminalLogger()})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ws/terminal/session-1/extra", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")

	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want=%d", w.Code, http.StatusBadRequest)
	}
}

func TestTerminalHandlerStreamingAndResize(t *testing.T) {
	terminalConn, backendConn := newTerminalConnPair()
	defer backendConn.Close()
	scrollback := newTestScrollbackCache()

	mgr := &fakeSessionManager{
		getFn: func(id string) (*session.SessionHandle, error) {
			return &session.SessionHandle{ID: id, Status: session.SessionStatusActive}, nil
		},
		healthFn: func(context.Context, string) (session.HealthStatus, error) {
			return session.HealthStatus{State: session.HealthStateHealthy}, nil
		},
		attachFn: func(context.Context, string) (session.TerminalConn, error) {
			return terminalConn, nil
		},
	}

	bridge := NewBridge(BridgeConfig{Logger: testTerminalLogger(), PingInterval: time.Hour, ScrollbackCache: scrollback})
	h := NewHandler(HandlerConfig{SessionManager: mgr, Bridge: bridge, Logger: testTerminalLogger()})

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, resp, err := websocket.DefaultDialer.Dial(wsURL(srv.URL, "/ws/terminal/session-1"), nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial websocket failed: %v (status=%d)", err, status)
	}
	defer client.Close()

	inbound := []byte("ls -la\n")
	if err := client.WriteMessage(websocket.BinaryMessage, inbound); err != nil {
		t.Fatalf("write binary websocket message: %v", err)
	}

	if err := backendConn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set backend read deadline: %v", err)
	}
	gotInbound := make([]byte, len(inbound))
	if _, err := io.ReadFull(backendConn, gotInbound); err != nil {
		t.Fatalf("read backend inbound data: %v", err)
	}
	if !bytes.Equal(gotInbound, inbound) {
		t.Fatalf("backend inbound=%q want=%q", gotInbound, inbound)
	}

	if err := client.WriteMessage(websocket.TextMessage, []byte(`{"type":"resize","cols":120,"rows":40}`)); err != nil {
		t.Fatalf("write resize control message: %v", err)
	}

	waitForCondition(t, time.Second, func() bool {
		calls := terminalConn.resizeSnapshot()
		if len(calls) != 1 {
			return false
		}
		return calls[0].cols == 120 && calls[0].rows == 40
	})

	backendPayload := []byte("\u001b[32mready\u001b[0m\r\n")
	if err := backendConn.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set backend write deadline: %v", err)
	}
	if _, err := backendConn.Write(backendPayload); err != nil {
		t.Fatalf("write backend payload: %v", err)
	}

	msgType, msgPayload, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("read websocket payload: %v", err)
	}
	if msgType != websocket.BinaryMessage {
		t.Fatalf("message type=%d want=%d", msgType, websocket.BinaryMessage)
	}
	if !bytes.Equal(msgPayload, backendPayload) {
		t.Fatalf("websocket payload=%q want=%q", msgPayload, backendPayload)
	}

	waitForCondition(t, 2*time.Second, func() bool {
		entries, err := scrollback.Get("session-1", 0, 0)
		if err != nil || len(entries) == 0 {
			return false
		}
		last := entries[len(entries)-1]
		return last.Type == cache.EntryTypeTerminalOutput && bytes.Equal(last.Content, backendPayload)
	})

	if err := client.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")); err != nil {
		t.Fatalf("send close frame: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close websocket client: %v", err)
	}

	waitForCondition(t, 2*time.Second, func() bool {
		return terminalConn.closeCallCount() > 0
	})

	getCalls, healthCalls, attachCalls := mgr.calls()
	if getCalls == 0 || healthCalls == 0 || attachCalls == 0 {
		t.Fatalf("expected manager calls >0, got get=%d health=%d attach=%d", getCalls, healthCalls, attachCalls)
	}
}
