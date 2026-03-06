package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"opencoderouter/internal/registry"
)

type fakeProcess struct {
	mu      sync.Mutex
	pid     int
	signals int
	kills   int
	exitCh  chan error
	once    sync.Once
}

func newFakeProcess(pid int) *fakeProcess {
	return &fakeProcess{pid: pid, exitCh: make(chan error, 1)}
}

func (p *fakeProcess) PID() int {
	return p.pid
}

func (p *fakeProcess) Signal(_ os.Signal) error {
	p.mu.Lock()
	p.signals++
	p.mu.Unlock()
	p.exit(nil)
	return nil
}

func (p *fakeProcess) Kill() error {
	p.mu.Lock()
	p.kills++
	p.mu.Unlock()
	p.exit(nil)
	return nil
}

func (p *fakeProcess) Wait() error {
	err, ok := <-p.exitCh
	if !ok {
		return nil
	}
	return err
}

func (p *fakeProcess) exit(err error) {
	p.once.Do(func() {
		p.exitCh <- err
		close(p.exitCh)
	})
}

func (p *fakeProcess) signalCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.signals
}

type fakeStarter struct {
	mu      sync.Mutex
	nextPID int
	byPort  map[int]*fakeProcess
}

func newFakeStarter() *fakeStarter {
	return &fakeStarter{byPort: make(map[int]*fakeProcess)}
}

func (s *fakeStarter) start(_ string, _ string, port int, _ map[string]string) (sessionProcess, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextPID++
	proc := newFakeProcess(s.nextPID)
	s.byPort[port] = proc
	return proc, nil
}

func (s *fakeStarter) processByPort(port int) *fakeProcess {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byPort[port]
}

type fakeTerminalConn struct {
	mu     sync.Mutex
	closed bool
}

func (c *fakeTerminalConn) Read(_ []byte) (int, error) {
	return 0, io.EOF
}

func (c *fakeTerminalConn) Write(p []byte) (int, error) {
	return len(p), nil
}

func (c *fakeTerminalConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func (c *fakeTerminalConn) Resize(_, _ int) error {
	return nil
}

func testSessionLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newManagerForTest(t *testing.T, cfg ManagerConfig) *Manager {
	t.Helper()
	if cfg.Logger == nil {
		cfg.Logger = testSessionLogger()
	}
	manager := NewManager(cfg)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = manager.Shutdown(shutdownCtx)
	})
	return manager
}

func waitForEventType(t *testing.T, ch <-chan Event, eventType EventType, timeout time.Duration) Event {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case event, ok := <-ch:
			if !ok {
				t.Fatalf("subscriber channel closed while waiting for %q", eventType)
			}
			if event.Type() == eventType {
				return event
			}
		case <-deadline:
			t.Fatalf("timed out waiting for event %q", eventType)
		}
	}
}

func mustPortFromURL(t *testing.T, rawURL string) int {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	_, portStr, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("atoi port: %v", err)
	}
	return port
}

func TestManagerCreateStopAndRegistryIntegration(t *testing.T) {
	workspace := t.TempDir()
	starter := newFakeStarter()
	eventBus := NewEventBus(32)
	events, unsubscribe, err := eventBus.Subscribe(EventFilter{Types: []EventType{EventTypeSessionCreated, EventTypeSessionStopped}})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(unsubscribe)

	reg := registry.New(30*time.Second, testSessionLogger())

	manager := newManagerForTest(t, ManagerConfig{
		Registry:            reg,
		EventBus:            eventBus,
		ProcessStarter:      starter.start,
		PortStart:           35100,
		PortEnd:             35100,
		PortAvailable:       func(int) bool { return true },
		HealthCheckInterval: time.Hour,
	})

	handle, err := manager.Create(context.Background(), CreateOpts{WorkspacePath: workspace})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	createdEvent := waitForEventType(t, events, EventTypeSessionCreated, time.Second)
	created, ok := createdEvent.(SessionCreated)
	if !ok {
		t.Fatalf("created event type = %T", createdEvent)
	}
	if created.Session.ID != handle.ID {
		t.Fatalf("created event session id = %q, want %q", created.Session.ID, handle.ID)
	}

	slug := registry.Slugify(workspace)
	backend, ok := reg.Lookup(slug)
	if !ok {
		t.Fatalf("expected backend %q in registry", slug)
	}
	if backend.Port != handle.DaemonPort {
		t.Fatalf("registry port = %d, want %d", backend.Port, handle.DaemonPort)
	}
	if backend.ProjectPath != workspace {
		t.Fatalf("registry path = %q, want %q", backend.ProjectPath, workspace)
	}

	if err := manager.Stop(context.Background(), handle.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}

	stoppedEvent := waitForEventType(t, events, EventTypeSessionStopped, time.Second)
	stopped, ok := stoppedEvent.(SessionStopped)
	if !ok {
		t.Fatalf("stopped event type = %T", stoppedEvent)
	}
	if stopped.Session.ID != handle.ID {
		t.Fatalf("stopped event session id = %q, want %q", stopped.Session.ID, handle.ID)
	}

	got, err := manager.Get(handle.ID)
	if err != nil {
		t.Fatalf("get after stop: %v", err)
	}
	if got.Status != SessionStatusStopped {
		t.Fatalf("status after stop = %q, want %q", got.Status, SessionStatusStopped)
	}

	proc := starter.processByPort(handle.DaemonPort)
	if proc == nil {
		t.Fatal("missing process for created session")
	}
	if proc.signalCount() == 0 {
		t.Fatal("expected stop to signal process")
	}
}

func TestManagerAttachDetachEvents(t *testing.T) {
	workspace := t.TempDir()
	starter := newFakeStarter()
	eventBus := NewEventBus(32)
	events, unsubscribe, err := eventBus.Subscribe(EventFilter{Types: []EventType{EventTypeSessionAttached, EventTypeSessionDetached}})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(unsubscribe)

	manager := newManagerForTest(t, ManagerConfig{
		EventBus:            eventBus,
		ProcessStarter:      starter.start,
		TerminalDialer:      func(context.Context, SessionHandle) (TerminalConn, error) { return &fakeTerminalConn{}, nil },
		PortStart:           35110,
		PortEnd:             35110,
		PortAvailable:       func(int) bool { return true },
		HealthCheckInterval: time.Hour,
	})

	handle, err := manager.Create(context.Background(), CreateOpts{WorkspacePath: workspace})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	conn, err := manager.AttachTerminal(context.Background(), handle.ID)
	if err != nil {
		t.Fatalf("attach terminal: %v", err)
	}

	attachedEvent := waitForEventType(t, events, EventTypeSessionAttached, time.Second)
	attached, ok := attachedEvent.(SessionAttached)
	if !ok {
		t.Fatalf("attached event type = %T", attachedEvent)
	}
	if attached.AttachedClients != 1 {
		t.Fatalf("attached clients = %d, want 1", attached.AttachedClients)
	}

	afterAttach, err := manager.Get(handle.ID)
	if err != nil {
		t.Fatalf("get after attach: %v", err)
	}
	if afterAttach.AttachedClients != 1 {
		t.Fatalf("attached clients in handle = %d, want 1", afterAttach.AttachedClients)
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("close terminal conn: %v", err)
	}

	detachedEvent := waitForEventType(t, events, EventTypeSessionDetached, time.Second)
	detached, ok := detachedEvent.(SessionDetached)
	if !ok {
		t.Fatalf("detached event type = %T", detachedEvent)
	}
	if detached.AttachedClients != 0 {
		t.Fatalf("detached clients = %d, want 0", detached.AttachedClients)
	}

	afterDetach, err := manager.Get(handle.ID)
	if err != nil {
		t.Fatalf("get after detach: %v", err)
	}
	if afterDetach.AttachedClients != 0 {
		t.Fatalf("attached clients in handle after detach = %d, want 0", afterDetach.AttachedClients)
	}
}

func TestManagerGetListRestartDelete(t *testing.T) {
	starter := newFakeStarter()
	manager := newManagerForTest(t, ManagerConfig{
		ProcessStarter:      starter.start,
		PortStart:           35120,
		PortEnd:             35130,
		PortAvailable:       func(int) bool { return true },
		HealthCheckInterval: time.Hour,
	})

	workspaceA := t.TempDir()
	workspaceB := t.TempDir()

	a, err := manager.Create(context.Background(), CreateOpts{WorkspacePath: workspaceA, Labels: map[string]string{"team": "alpha"}})
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	b, err := manager.Create(context.Background(), CreateOpts{WorkspacePath: workspaceB, Labels: map[string]string{"team": "beta"}})
	if err != nil {
		t.Fatalf("create B: %v", err)
	}

	gotA, err := manager.Get(a.ID)
	if err != nil {
		t.Fatalf("get A: %v", err)
	}
	if gotA.WorkspacePath != workspaceA {
		t.Fatalf("workspace A = %q, want %q", gotA.WorkspacePath, workspaceA)
	}

	list, err := manager.List(SessionListFilter{LabelSelector: map[string]string{"team": "alpha"}, Status: SessionStatusActive})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != a.ID {
		t.Fatalf("filtered list = %#v, want only %q", list, a.ID)
	}

	restarted, err := manager.Restart(context.Background(), a.ID)
	if err != nil {
		t.Fatalf("restart A: %v", err)
	}
	if restarted.ID != a.ID {
		t.Fatalf("restart id = %q, want %q", restarted.ID, a.ID)
	}
	if restarted.DaemonPort != a.DaemonPort {
		t.Fatalf("restart port = %d, want %d", restarted.DaemonPort, a.DaemonPort)
	}

	if err := manager.Delete(context.Background(), b.ID); err != nil {
		t.Fatalf("delete B: %v", err)
	}

	if _, err := manager.Get(b.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound after delete, got %v", err)
	}
}

func TestManagerPeriodicHealthChecks(t *testing.T) {
	workspace := t.TempDir()
	starter := newFakeStarter()
	eventBus := NewEventBus(64)
	events, unsubscribe, err := eventBus.Subscribe(EventFilter{Types: []EventType{EventTypeSessionHealthChanged}})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(unsubscribe)

	var healthy atomic.Bool
	healthy.Store(true)

	mux := http.NewServeMux()
	mux.HandleFunc("/global/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"healthy": healthy.Load()})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	port := mustPortFromURL(t, server.URL)

	manager := newManagerForTest(t, ManagerConfig{
		EventBus:            eventBus,
		ProcessStarter:      starter.start,
		PortStart:           port,
		PortEnd:             port,
		PortAvailable:       func(int) bool { return true },
		HealthCheckInterval: 30 * time.Millisecond,
		HealthCheckTimeout:  500 * time.Millisecond,
	})

	handle, err := manager.Create(context.Background(), CreateOpts{WorkspacePath: workspace})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	first := waitForEventType(t, events, EventTypeSessionHealthChanged, 2*time.Second)
	healthEvent, ok := first.(SessionHealthChanged)
	if !ok {
		t.Fatalf("health event type = %T", first)
	}
	if healthEvent.Session.ID != handle.ID {
		t.Fatalf("health event session id = %q, want %q", healthEvent.Session.ID, handle.ID)
	}
	if healthEvent.Current.State != HealthStateHealthy {
		t.Fatalf("first health state = %q, want %q", healthEvent.Current.State, HealthStateHealthy)
	}

	healthy.Store(false)

	second := waitForEventType(t, events, EventTypeSessionHealthChanged, 2*time.Second)
	healthEvent2, ok := second.(SessionHealthChanged)
	if !ok {
		t.Fatalf("health event type = %T", second)
	}
	if healthEvent2.Current.State != HealthStateUnhealthy {
		t.Fatalf("second health state = %q, want %q", healthEvent2.Current.State, HealthStateUnhealthy)
	}
}

func TestManagerHealthCircuitBreakerSkipsProbesUntilCooldownAndResets(t *testing.T) {
	workspace := t.TempDir()
	starter := newFakeStarter()

	now := time.Date(2026, 3, 6, 12, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return now }

	var checks atomic.Int32
	healthState := HealthStateUnhealthy

	manager := newManagerForTest(t, ManagerConfig{
		ProcessStarter:      starter.start,
		PortStart:           35135,
		PortEnd:             35135,
		PortAvailable:       func(int) bool { return true },
		HealthCheckInterval: time.Hour,
		HealthFailThreshold: 3,
		HealthCircuitReset:  30 * time.Second,
		Now:                 nowFn,
		HealthChecker: func(_ context.Context, _ int) HealthStatus {
			checks.Add(1)
			if healthState == HealthStateHealthy {
				return HealthStatus{State: HealthStateHealthy, LastCheck: nowFn()}
			}
			return HealthStatus{State: HealthStateUnhealthy, LastCheck: nowFn(), Error: "daemon unhealthy"}
		},
	})

	handle, err := manager.Create(context.Background(), CreateOpts{WorkspacePath: workspace})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	for i := 0; i < 3; i++ {
		health, err := manager.Health(context.Background(), handle.ID)
		if err != nil {
			t.Fatalf("health probe %d: %v", i+1, err)
		}
		if health.State != HealthStateUnhealthy {
			t.Fatalf("health state probe %d = %q, want %q", i+1, health.State, HealthStateUnhealthy)
		}
	}
	if got := checks.Load(); got != 3 {
		t.Fatalf("health checker calls after threshold = %d, want 3", got)
	}

	if _, err := manager.Health(context.Background(), handle.ID); err != nil {
		t.Fatalf("health probe while circuit open: %v", err)
	}
	if got := checks.Load(); got != 3 {
		t.Fatalf("health checker calls while circuit open = %d, want 3", got)
	}

	now = now.Add(20 * time.Second)
	if _, err := manager.Health(context.Background(), handle.ID); err != nil {
		t.Fatalf("health probe before cooldown expires: %v", err)
	}
	if got := checks.Load(); got != 3 {
		t.Fatalf("health checker calls before cooldown expires = %d, want 3", got)
	}

	now = now.Add(15 * time.Second)
	if _, err := manager.Health(context.Background(), handle.ID); err != nil {
		t.Fatalf("health probe after cooldown expires: %v", err)
	}
	if got := checks.Load(); got != 4 {
		t.Fatalf("health checker calls after cooldown expires = %d, want 4", got)
	}

	healthState = HealthStateHealthy
	now = now.Add(31 * time.Second)
	health, err := manager.Health(context.Background(), handle.ID)
	if err != nil {
		t.Fatalf("health recovery probe: %v", err)
	}
	if health.State != HealthStateHealthy {
		t.Fatalf("health state after recovery = %q, want %q", health.State, HealthStateHealthy)
	}

	healthState = HealthStateUnhealthy
	for i := 0; i < 3; i++ {
		now = now.Add(time.Second)
		if _, err := manager.Health(context.Background(), handle.ID); err != nil {
			t.Fatalf("re-trip health probe %d: %v", i+1, err)
		}
	}
	trippedCalls := checks.Load()

	if _, err := manager.Health(context.Background(), handle.ID); err != nil {
		t.Fatalf("health probe while re-tripped circuit open: %v", err)
	}
	if got := checks.Load(); got != trippedCalls {
		t.Fatalf("health checker calls during re-tripped open circuit = %d, want %d", got, trippedCalls)
	}

	now = now.Add(5 * time.Second)
	if _, err := manager.Restart(context.Background(), handle.ID); err != nil {
		t.Fatalf("restart: %v", err)
	}

	if _, err := manager.Health(context.Background(), handle.ID); err != nil {
		t.Fatalf("health after manual restart: %v", err)
	}
	if got := checks.Load(); got != trippedCalls+1 {
		t.Fatalf("health checker calls after restart = %d, want %d", got, trippedCalls+1)
	}
}

func TestManagerCrashDetectionEmitsHealthEvent(t *testing.T) {
	workspace := t.TempDir()
	starter := newFakeStarter()
	eventBus := NewEventBus(32)
	events, unsubscribe, err := eventBus.Subscribe(EventFilter{Types: []EventType{EventTypeSessionHealthChanged}})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(unsubscribe)

	manager := newManagerForTest(t, ManagerConfig{
		EventBus:            eventBus,
		ProcessStarter:      starter.start,
		PortStart:           35140,
		PortEnd:             35140,
		PortAvailable:       func(int) bool { return true },
		HealthCheckInterval: time.Hour,
	})

	handle, err := manager.Create(context.Background(), CreateOpts{WorkspacePath: workspace})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	proc := starter.processByPort(handle.DaemonPort)
	if proc == nil {
		t.Fatal("missing fake process")
	}
	proc.exit(errors.New("boom"))

	event := waitForEventType(t, events, EventTypeSessionHealthChanged, 2*time.Second)
	healthEvent, ok := event.(SessionHealthChanged)
	if !ok {
		t.Fatalf("health event type = %T", event)
	}
	if healthEvent.Current.State != HealthStateUnhealthy {
		t.Fatalf("health state = %q, want %q", healthEvent.Current.State, HealthStateUnhealthy)
	}
	if !strings.Contains(healthEvent.Current.Error, "boom") {
		t.Fatalf("health error = %q, want boom substring", healthEvent.Current.Error)
	}

	got, err := manager.Get(handle.ID)
	if err != nil {
		t.Fatalf("get after crash: %v", err)
	}
	if got.Status != SessionStatusError {
		t.Fatalf("status after crash = %q, want %q", got.Status, SessionStatusError)
	}
}
