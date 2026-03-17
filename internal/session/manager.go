package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"opencoderouter/internal/registry"
)

const (
	defaultSessionPortStart      = 30000
	defaultSessionPortEnd        = 31000
	defaultHealthCheckInterval   = 10 * time.Second
	defaultHealthCheckTimeout    = 2 * time.Second
	defaultHealthFailThreshold   = 3
	defaultHealthCircuitCooldown = 30 * time.Second
	defaultSessionStopTimeout    = 5 * time.Second
	defaultSessionOpenCodeBinary = "opencode"
)

var (
	ErrSessionNotFound         = errors.New("session not found")
	ErrSessionAlreadyExists    = errors.New("session already exists")
	ErrWorkspacePathRequired   = errors.New("workspace path is required")
	ErrWorkspacePathInvalid    = errors.New("workspace path is invalid")
	ErrNoAvailableSessionPorts = errors.New("no available session ports")
	ErrSessionStopped          = errors.New("session is stopped")
	ErrTerminalAttachDisabled  = errors.New("terminal attachment is not configured")
	errProcessWaitTimeout      = errors.New("timeout waiting for session process exit")
)

type processStarterFn func(binary, workspace string, port int, envVars map[string]string) (sessionProcess, error)
type healthCheckerFn func(ctx context.Context, port int) HealthStatus
type terminalDialerFn func(ctx context.Context, handle SessionHandle) (TerminalConn, error)

type ManagerConfig struct {
	Registry            *registry.Registry
	EventBus            EventBus
	Logger              *slog.Logger
	PortStart           int
	PortEnd             int
	HealthCheckInterval time.Duration
	HealthCheckTimeout  time.Duration
	HealthFailThreshold int
	HealthCircuitReset  time.Duration
	StopTimeout         time.Duration
	EventBuffer         int
	ProcessStarter      processStarterFn
	HealthChecker       healthCheckerFn
	TerminalDialer      terminalDialerFn
	PortAvailable       func(port int) bool
	Now                 func() time.Time
}

type Manager struct {
	mu                  sync.RWMutex
	sessions            map[string]*managedSession
	registry            *registry.Registry
	eventBus            EventBus
	logger              *slog.Logger
	portStart           int
	portEnd             int
	healthCheckInterval time.Duration
	healthCheckTimeout  time.Duration
	healthFailThreshold int
	healthCircuitReset  time.Duration
	stopTimeout         time.Duration
	processStarter      processStarterFn
	healthChecker       healthCheckerFn
	terminalDialer      terminalDialerFn
	portAvailable       func(port int) bool
	now                 func() time.Time
	nextSessionSeq      uint64
	nextClientSeq       uint64
	loopCancel          context.CancelFunc
	loopStopOnce        sync.Once
	wg                  sync.WaitGroup
}

type managedSession struct {
	handle       SessionHandle
	opts         CreateOpts
	process      sessionProcess
	health       HealthStatus
	healthFails  int
	nextProbeAt  time.Time
	expectedStop bool
	exitCh       chan error
}

type sessionProcess interface {
	PID() int
	Signal(sig os.Signal) error
	Kill() error
	Wait() error
}

type execProcess struct {
	cmd *exec.Cmd
}

func (p *execProcess) PID() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *execProcess) Signal(sig os.Signal) error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return os.ErrProcessDone
	}
	return p.cmd.Process.Signal(sig)
}

func (p *execProcess) Kill() error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return os.ErrProcessDone
	}
	return p.cmd.Process.Kill()
}

func (p *execProcess) Wait() error {
	if p == nil || p.cmd == nil {
		return nil
	}
	return p.cmd.Wait()
}

type managedTerminalConn struct {
	TerminalConn
	onClose func()
	once    sync.Once
}

func (c *managedTerminalConn) Close() error {
	var err error
	c.once.Do(func() {
		if c.TerminalConn != nil {
			err = c.TerminalConn.Close()
		}
		if c.onClose != nil {
			c.onClose()
		}
	})
	return err
}

func NewManager(cfg ManagerConfig) *Manager {
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = time.Now
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	portStart := cfg.PortStart
	portEnd := cfg.PortEnd
	if portStart <= 0 {
		portStart = defaultSessionPortStart
	}
	if portEnd <= 0 {
		portEnd = defaultSessionPortEnd
	}
	if portEnd < portStart {
		portStart = defaultSessionPortStart
		portEnd = defaultSessionPortEnd
	}

	healthCheckInterval := cfg.HealthCheckInterval
	if healthCheckInterval <= 0 {
		healthCheckInterval = defaultHealthCheckInterval
	}

	healthCheckTimeout := cfg.HealthCheckTimeout
	if healthCheckTimeout <= 0 {
		healthCheckTimeout = defaultHealthCheckTimeout
	}

	stopTimeout := cfg.StopTimeout
	if stopTimeout <= 0 {
		stopTimeout = defaultSessionStopTimeout
	}

	healthFailThreshold := cfg.HealthFailThreshold
	if healthFailThreshold <= 0 {
		healthFailThreshold = defaultHealthFailThreshold
	}

	healthCircuitReset := cfg.HealthCircuitReset
	if healthCircuitReset <= 0 {
		healthCircuitReset = defaultHealthCircuitCooldown
	}

	eventBus := cfg.EventBus
	if eventBus == nil {
		eventBus = NewEventBus(cfg.EventBuffer)
	}

	processStarter := cfg.ProcessStarter
	if processStarter == nil {
		processStarter = defaultProcessStarter
	}

	portAvailable := cfg.PortAvailable
	if portAvailable == nil {
		portAvailable = defaultPortAvailable
	}

	healthChecker := cfg.HealthChecker
	if healthChecker == nil {
		healthChecker = defaultHealthChecker(&http.Client{}, nowFn)
	}

	terminalDialer := cfg.TerminalDialer
	if terminalDialer == nil {
		terminalDialer = defaultTerminalDialer
	}

	ctx, cancel := context.WithCancel(context.Background())

	m := &Manager{
		sessions:            make(map[string]*managedSession),
		registry:            cfg.Registry,
		eventBus:            eventBus,
		logger:              logger,
		portStart:           portStart,
		portEnd:             portEnd,
		healthCheckInterval: healthCheckInterval,
		healthCheckTimeout:  healthCheckTimeout,
		healthFailThreshold: healthFailThreshold,
		healthCircuitReset:  healthCircuitReset,
		stopTimeout:         stopTimeout,
		processStarter:      processStarter,
		healthChecker:       healthChecker,
		terminalDialer:      terminalDialer,
		portAvailable:       portAvailable,
		now:                 nowFn,
		loopCancel:          cancel,
	}

	m.wg.Add(1)
	go m.healthLoop(ctx)

	return m
}

func (m *Manager) Create(ctx context.Context, opts CreateOpts) (*SessionHandle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := m.nextSessionID()
	return m.createWithID(ctx, id, opts, 0, false)
}

func (m *Manager) Get(id string) (*SessionHandle, error) {
	if m == nil {
		return nil, ErrSessionNotFound
	}

	m.mu.RLock()
	rec, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		m.syncFromRegistry()
		m.mu.RLock()
		rec, ok = m.sessions[id]
		if !ok {
			m.mu.RUnlock()
			return nil, ErrSessionNotFound
		}
		handle := cloneSessionHandle(rec.handle)
		m.mu.RUnlock()
		return &handle, nil
	}
	handle := cloneSessionHandle(rec.handle)

	return &handle, nil
}

func (m *Manager) List(filter SessionListFilter) ([]SessionHandle, error) {
	if m == nil {
		return nil, nil
	}

	m.syncFromRegistry()

	m.mu.RLock()
	result := make([]SessionHandle, 0, len(m.sessions))
	for _, rec := range m.sessions {
		handle := cloneSessionHandle(rec.handle)
		if !matchesSessionFilter(handle, filter) {
			continue
		}
		result = append(result, handle)
	}
	m.mu.RUnlock()

	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})

	return result, nil
}

func (m *Manager) Stop(ctx context.Context, id string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	proc, exitCh, alreadyStopped, err := m.prepareStop(id)
	if err != nil {
		return err
	}
	if alreadyStopped {
		return nil
	}

	if proc != nil {
		if signalErr := proc.Signal(syscall.SIGTERM); signalErr != nil && !isProcessAlreadyDone(signalErr) {
			m.logger.Debug("session stop signal error", "session_id", id, "error", signalErr)
		}

		waitErr := m.waitForExit(ctx, exitCh, m.stopTimeout)
		if errors.Is(waitErr, errProcessWaitTimeout) {
			if killErr := proc.Kill(); killErr != nil && !isProcessAlreadyDone(killErr) {
				m.logger.Debug("session stop kill error", "session_id", id, "error", killErr)
			}
			waitErr = m.waitForExit(ctx, exitCh, m.stopTimeout)
		}

		if waitErr != nil && !isExpectedExitError(waitErr) {
			if errors.Is(waitErr, context.Canceled) || errors.Is(waitErr, context.DeadlineExceeded) {
				return waitErr
			}
			return waitErr
		}
	}

	now := m.now()
	var snapshot SessionHandle
	var publish bool

	m.mu.Lock()
	rec, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return ErrSessionNotFound
	}
	publish = rec.handle.Status != SessionStatusStopped
	rec.handle.Status = SessionStatusStopped
	rec.handle.LastActivity = now
	rec.handle.AttachedClients = 0
	rec.health = HealthStatus{State: HealthStateUnknown, LastCheck: now}
	rec.healthFails = 0
	rec.nextProbeAt = time.Time{}
	rec.process = nil
	rec.expectedStop = true
	snapshot = cloneSessionHandle(rec.handle)
	m.mu.Unlock()

	if publish {
		m.publishEvent(SessionStopped{At: now, Session: snapshot, Reason: "stop"})
	}

	return nil
}

func (m *Manager) Restart(ctx context.Context, id string) (*SessionHandle, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.RLock()
	rec, ok := m.sessions[id]
	if !ok {
		m.mu.RUnlock()
		return nil, ErrSessionNotFound
	}
	opts := cloneCreateOpts(rec.opts)
	if opts.WorkspacePath == "" {
		opts.WorkspacePath = rec.handle.WorkspacePath
	}
	if len(opts.Labels) == 0 {
		opts.Labels = cloneStringMap(rec.handle.Labels)
	}
	preferredPort := rec.handle.DaemonPort
	m.mu.RUnlock()

	if err := m.Stop(ctx, id); err != nil {
		return nil, err
	}

	return m.createWithID(ctx, id, opts, preferredPort, true)
}

func (m *Manager) Delete(ctx context.Context, id string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if err := m.Stop(ctx, id); err != nil {
		return err
	}

	m.mu.Lock()
	if _, ok := m.sessions[id]; !ok {
		m.mu.Unlock()
		return ErrSessionNotFound
	}
	delete(m.sessions, id)
	m.mu.Unlock()

	return nil
}

func (m *Manager) AttachTerminal(ctx context.Context, id string) (TerminalConn, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.RLock()
	rec, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		m.syncFromRegistry()
		m.mu.RLock()
		rec, ok = m.sessions[id]
		if !ok {
			m.mu.RUnlock()
			return nil, ErrSessionNotFound
		}
		handle := cloneSessionHandle(rec.handle)
		if rec.handle.Status == SessionStatusStopped {
			m.mu.RUnlock()
			return nil, ErrSessionStopped
		}
		m.mu.RUnlock()

		conn, err := m.terminalDialer(ctx, handle)
		if err != nil {
			return nil, err
		}

		clientID := m.nextClientID()
		now := m.now()

		var attached int
		var snapshot SessionHandle

		m.mu.Lock()
		rec, ok = m.sessions[id]
		if !ok {
			m.mu.Unlock()
			if closeErr := conn.Close(); closeErr != nil {
				m.logger.Debug("failed to close terminal connection after missing session", "session_id", id, "error", closeErr)
			}
			return nil, ErrSessionNotFound
		}
		rec.handle.AttachedClients++
		rec.handle.LastActivity = now
		attached = rec.handle.AttachedClients
		snapshot = cloneSessionHandle(rec.handle)
		m.mu.Unlock()

		m.publishEvent(SessionAttached{At: now, Session: snapshot, AttachedClients: attached, ClientID: clientID})

		wrapped := &managedTerminalConn{
			TerminalConn: conn,
			onClose: func() {
				m.onTerminalDetached(id, clientID)
			},
		}

		return wrapped, nil
	}

	if !ok {
		return nil, ErrSessionNotFound
	}
	if rec.handle.Status == SessionStatusStopped {
		return nil, ErrSessionStopped
	}
	handle := cloneSessionHandle(rec.handle)

	conn, err := m.terminalDialer(ctx, handle)
	if err != nil {
		return nil, err
	}

	clientID := m.nextClientID()
	now := m.now()

	var attached int
	var snapshot SessionHandle

	m.mu.Lock()
	rec, ok = m.sessions[id]
	if !ok {
		m.mu.Unlock()
		if closeErr := conn.Close(); closeErr != nil {
			m.logger.Debug("failed to close terminal connection after missing session", "session_id", id, "error", closeErr)
		}
		return nil, ErrSessionNotFound
	}
	rec.handle.AttachedClients++
	rec.handle.LastActivity = now
	attached = rec.handle.AttachedClients
	snapshot = cloneSessionHandle(rec.handle)
	m.mu.Unlock()

	m.publishEvent(SessionAttached{At: now, Session: snapshot, AttachedClients: attached, ClientID: clientID})

	wrapped := &managedTerminalConn{
		TerminalConn: conn,
		onClose: func() {
			m.onTerminalDetached(id, clientID)
		},
	}

	return wrapped, nil
}

func (m *Manager) Health(ctx context.Context, id string) (HealthStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.RLock()
	rec, ok := m.sessions[id]
	if !ok {
		m.mu.RUnlock()
		m.syncFromRegistry()
		m.mu.RLock()
		rec, ok = m.sessions[id]
		if !ok {
			m.mu.RUnlock()
			return HealthStatus{}, ErrSessionNotFound
		}
	}
	now := m.now()
	current := rec.health
	status := rec.handle.Status
	port := rec.handle.DaemonPort
	nextProbeAt := rec.nextProbeAt
	m.mu.RUnlock()

	if status == SessionStatusStopped || port <= 0 {
		if current.LastCheck.IsZero() {
			current.LastCheck = now
		}
		return current, nil
	}

	if !nextProbeAt.IsZero() && now.Before(nextProbeAt) {
		if current.LastCheck.IsZero() {
			current.LastCheck = now
		}
		return current, nil
	}

	next := m.healthChecker(ctx, port)
	if next.LastCheck.IsZero() {
		next.LastCheck = m.now()
	}

	return m.storeHealth(id, next)
}

func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.stopBackgroundLoop()
	return m.waitForBackground(ctx)
}

func (m *Manager) Shutdown(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	m.stopBackgroundLoop()

	ids := m.sessionIDsSnapshot()
	for _, id := range ids {
		if err := m.Stop(ctx, id); err != nil && !errors.Is(err, ErrSessionNotFound) {
			return err
		}
	}

	return m.waitForBackground(ctx)
}

func (m *Manager) createWithID(ctx context.Context, id string, opts CreateOpts, preferredPort int, replace bool) (*SessionHandle, error) {
	validatedOpts, err := validateCreateOpts(opts)
	if err != nil {
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	port, err := m.allocatePort(preferredPort, id)
	if err != nil {
		return nil, err
	}

	proc, err := m.processStarter(validatedOpts.OpenCodeBinary, validatedOpts.WorkspacePath, port, validatedOpts.EnvVars)
	if err != nil {
		return nil, fmt.Errorf("start session process: %w", err)
	}

	now := m.now()
	initialHandle := SessionHandle{
		ID:            id,
		DaemonPort:    port,
		WorkspacePath: validatedOpts.WorkspacePath,
		Status:        SessionStatusActive,
		CreatedAt:     now,
		LastActivity:  now,
		Labels:        cloneStringMap(validatedOpts.Labels),
	}
	record := &managedSession{
		handle: initialHandle,
		opts:    cloneCreateOpts(validatedOpts),
		process: proc,
		health: HealthStatus{
			State:     HealthStateUnknown,
			LastCheck: now,
		},
		exitCh: make(chan error, 1),
	}

	m.mu.Lock()
	if existing, ok := m.sessions[id]; ok {
		if !replace {
			m.mu.Unlock()
			cleanupProcess(proc)
			return nil, ErrSessionAlreadyExists
		}
		if existing.process != nil {
			m.mu.Unlock()
			cleanupProcess(proc)
			return nil, fmt.Errorf("cannot replace running session %q", id)
		}
	}
	m.sessions[id] = record
	m.mu.Unlock()

	m.wg.Add(1)
	go m.watchProcess(id, proc, record.exitCh)

	if m.registry != nil {
		projectName := filepath.Base(validatedOpts.WorkspacePath)
		m.registry.Upsert(port, projectName, validatedOpts.WorkspacePath, "")
	}

	snapshot := cloneSessionHandle(initialHandle)
	m.publishEvent(SessionCreated{At: now, Session: snapshot})

	return &snapshot, nil
}

func (m *Manager) prepareStop(id string) (sessionProcess, <-chan error, bool, error) {
	if m == nil {
		return nil, nil, false, ErrSessionNotFound
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	rec, ok := m.sessions[id]
	if !ok {
		return nil, nil, false, ErrSessionNotFound
	}

	if rec.handle.Status == SessionStatusStopped && rec.process == nil {
		return nil, nil, true, nil
	}

	rec.expectedStop = true
	return rec.process, rec.exitCh, false, nil
}

func (m *Manager) onTerminalDetached(id string, clientID string) {
	now := m.now()

	var attached int
	var snapshot SessionHandle
	var publish bool

	m.mu.Lock()
	rec, ok := m.sessions[id]
	if ok {
		if rec.handle.AttachedClients > 0 {
			rec.handle.AttachedClients--
		}
		rec.handle.LastActivity = now
		attached = rec.handle.AttachedClients
		snapshot = cloneSessionHandle(rec.handle)
		publish = true
	}
	m.mu.Unlock()

	if publish {
		m.publishEvent(SessionDetached{At: now, Session: snapshot, AttachedClients: attached, ClientID: clientID})
	}
}

func (m *Manager) watchProcess(id string, proc sessionProcess, exitCh chan error) {
	defer m.wg.Done()

	err := proc.Wait()

	select {
	case exitCh <- err:
	default:
	}
	close(exitCh)

	m.handleProcessExit(id, err)
}

func (m *Manager) handleProcessExit(id string, waitErr error) {
	now := m.now()

	var prev HealthStatus
	var current HealthStatus
	var snapshot SessionHandle
	var publish bool

	m.mu.Lock()
	rec, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return
	}

	rec.process = nil
	rec.handle.LastActivity = now

	if rec.expectedStop || rec.handle.Status == SessionStatusStopped {
		rec.health = HealthStatus{State: HealthStateUnknown, LastCheck: now}
		m.mu.Unlock()
		return
	}

	prev = rec.health
	rec.handle.Status = SessionStatusError
	rec.health = HealthStatus{
		State:     HealthStateUnhealthy,
		LastCheck: now,
		Error:     processExitMessage(waitErr),
	}
	current = rec.health
	snapshot = cloneSessionHandle(rec.handle)
	publish = prev.State != current.State || prev.Error != current.Error
	m.mu.Unlock()

	if publish {
		m.publishEvent(SessionHealthChanged{At: now, Session: snapshot, Previous: prev, Current: current})
	}
}

func (m *Manager) storeHealth(id string, next HealthStatus) (HealthStatus, error) {
	if next.LastCheck.IsZero() {
		next.LastCheck = m.now()
	}

	var prev HealthStatus
	var snapshot SessionHandle
	var publish bool

	m.mu.Lock()
	rec, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return HealthStatus{}, ErrSessionNotFound
	}

	prev = rec.health
	if next.State == HealthStateHealthy {
		rec.healthFails = 0
		rec.nextProbeAt = time.Time{}
	} else if next.State == HealthStateUnhealthy {
		rec.healthFails++
		if rec.healthFails >= m.healthFailThreshold {
			rec.nextProbeAt = next.LastCheck.Add(m.healthCircuitReset)
		}
	}
	rec.health = next
	rec.handle.Status = statusFromHealth(rec.handle.Status, next.State)
	snapshot = cloneSessionHandle(rec.handle)
	publish = prev.State != next.State || prev.Error != next.Error
	m.mu.Unlock()

	if publish {
		m.publishEvent(SessionHealthChanged{At: next.LastCheck, Session: snapshot, Previous: prev, Current: next})
	}

	return next, nil
}

func (m *Manager) healthLoop(ctx context.Context) {
	defer m.wg.Done()

	ticker := time.NewTicker(m.healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		ids := m.healthCheckSessionIDs()
		for _, id := range ids {
			probeCtx, cancel := context.WithTimeout(ctx, m.healthCheckTimeout)
			_, err := m.Health(probeCtx, id)
			cancel()
			if err != nil && !errors.Is(err, ErrSessionNotFound) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				m.logger.Debug("session health check error", "session_id", id, "error", err)
			}
		}
	}
}

func (m *Manager) healthCheckSessionIDs() []string {
	m.syncFromRegistry()

	m.mu.RLock()
	ids := make([]string, 0, len(m.sessions))
	for id, rec := range m.sessions {
		if rec.handle.Status == SessionStatusStopped || rec.handle.DaemonPort <= 0 {
			continue
		}
		ids = append(ids, id)
	}
	m.mu.RUnlock()

	sort.Strings(ids)
	return ids
}

func (m *Manager) sessionIDsSnapshot() []string {
	m.mu.RLock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.RUnlock()
	sort.Strings(ids)
	return ids
}

func (m *Manager) stopBackgroundLoop() {
	m.loopStopOnce.Do(func() {
		if m.loopCancel != nil {
			m.loopCancel()
		}
	})
}

func (m *Manager) syncFromRegistry() {
	if m == nil || m.registry == nil {
		return
	}

	backends := m.registry.All()
	if len(backends) == 0 {
		return
	}

	now := m.now()
	discovered := make(map[string]struct{}, len(backends)*4)

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, backend := range backends {
		if backend == nil {
			continue
		}

		backendSessions := m.registry.ListSessions(backend.Slug)
		for _, sessionMeta := range backendSessions {
			sessionID := strings.TrimSpace(sessionMeta.ID)
			if sessionID == "" {
				continue
			}

			discovered[sessionID] = struct{}{}

			workspacePath := strings.TrimSpace(sessionMeta.Directory)
			if workspacePath == "" {
				workspacePath = backend.ProjectPath
			}

			daemonPort := sessionMeta.DaemonPort
			if daemonPort <= 0 {
				daemonPort = backend.Port
			}
			if daemonPort <= 0 {
				continue
			}

			createdAt := sessionMeta.CreatedAt
			if createdAt.IsZero() {
				createdAt = now
			}

			lastActivity := sessionMeta.LastActivity
			if lastActivity.IsZero() {
				lastActivity = createdAt
			}

			status := sessionStatusFromRegistry(sessionMeta.Status)

			labels := map[string]string{
				"source":       "registry",
				"backend_slug": backend.Slug,
			}
			if backend.ProjectPath != "" {
				labels["backend_path"] = backend.ProjectPath
			}

			rec, ok := m.sessions[sessionID]
			if ok && rec.process != nil {
				continue
			}
			if ok && !isRegistrySession(rec) {
				continue
			}

			health := HealthStatus{State: HealthStateUnknown, LastCheck: now}
			if ok {
				health = rec.health
				if health.LastCheck.IsZero() {
					health.LastCheck = now
				}
			}

			m.sessions[sessionID] = &managedSession{
				handle: SessionHandle{
					ID:              sessionID,
					DaemonPort:      daemonPort,
					WorkspacePath:   workspacePath,
					Status:          status,
					CreatedAt:       createdAt,
					LastActivity:    lastActivity,
					AttachedClients: sessionMeta.AttachedClients,
					Labels:          cloneStringMap(labels),
				},
				opts: CreateOpts{
					WorkspacePath: workspacePath,
					Labels:        cloneStringMap(labels),
				},
				health:       health,
				healthFails:  0,
				nextProbeAt:  time.Time{},
				expectedStop: true,
				exitCh:       nil,
			}
		}
	}

	for sessionID, rec := range m.sessions {
		if rec.process != nil {
			continue
		}
		if !isRegistrySession(rec) {
			continue
		}
		if _, ok := discovered[sessionID]; !ok {
			delete(m.sessions, sessionID)
		}
	}
}

func isRegistrySession(rec *managedSession) bool {
	if rec == nil {
		return false
	}
	if rec.opts.Labels == nil {
		return false
	}
	return rec.opts.Labels["source"] == "registry"
}

func sessionStatusFromRegistry(raw string) SessionStatus {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "active", "running", "online", "ready":
		return SessionStatusActive
	case "idle", "paused":
		return SessionStatusIdle
	case "stopped", "offline", "terminated":
		return SessionStatusStopped
	case "error", "failed", "unhealthy":
		return SessionStatusError
	default:
		return SessionStatusUnknown
	}
}

func (m *Manager) waitForBackground(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) waitForExit(ctx context.Context, exitCh <-chan error, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = defaultSessionStopTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errProcessWaitTimeout
	case err, ok := <-exitCh:
		if !ok {
			return nil
		}
		return err
	}
}

func (m *Manager) allocatePort(preferredPort int, sessionID string) (int, error) {
	used := m.activePorts(sessionID)

	if preferredPort >= m.portStart && preferredPort <= m.portEnd {
		if _, inUse := used[preferredPort]; !inUse && m.portAvailable(preferredPort) {
			return preferredPort, nil
		}
	}

	for port := m.portStart; port <= m.portEnd; port++ {
		if _, inUse := used[port]; inUse {
			continue
		}
		if !m.portAvailable(port) {
			continue
		}
		return port, nil
	}

	return 0, ErrNoAvailableSessionPorts
}

func (m *Manager) activePorts(ignoreSessionID string) map[int]struct{} {
	m.mu.RLock()
	used := make(map[int]struct{}, len(m.sessions))
	for id, rec := range m.sessions {
		if id == ignoreSessionID {
			continue
		}
		if rec.process == nil || rec.handle.Status == SessionStatusStopped {
			continue
		}
		used[rec.handle.DaemonPort] = struct{}{}
	}
	m.mu.RUnlock()
	return used
}

func (m *Manager) publishEvent(event Event) {
	if m == nil || m.eventBus == nil || event == nil {
		return
	}
	if err := m.eventBus.Publish(event); err != nil && !errors.Is(err, ErrEventBusClosed) {
		m.logger.Debug("session event publish error", "type", event.Type(), "session_id", event.SessionID(), "error", err)
	}
}

func (m *Manager) nextSessionID() string {
	n := atomic.AddUint64(&m.nextSessionSeq, 1)
	return "session-" + strconv.FormatUint(n, 10)
}

func (m *Manager) nextClientID() string {
	n := atomic.AddUint64(&m.nextClientSeq, 1)
	return "client-" + strconv.FormatUint(n, 10)
}

func validateCreateOpts(opts CreateOpts) (CreateOpts, error) {
	if opts.WorkspacePath == "" {
		return CreateOpts{}, ErrWorkspacePathRequired
	}

	absPath, err := filepath.Abs(opts.WorkspacePath)
	if err != nil {
		return CreateOpts{}, fmt.Errorf("%w: %v", ErrWorkspacePathInvalid, err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return CreateOpts{}, fmt.Errorf("%w: %v", ErrWorkspacePathInvalid, err)
	}
	if !info.IsDir() {
		return CreateOpts{}, ErrWorkspacePathInvalid
	}

	normalized := cloneCreateOpts(opts)
	normalized.WorkspacePath = absPath
	if normalized.OpenCodeBinary == "" {
		normalized.OpenCodeBinary = defaultSessionOpenCodeBinary
	}

	return normalized, nil
}

func defaultProcessStarter(binary, workspace string, port int, envVars map[string]string) (sessionProcess, error) {
	cmd := exec.Command(binary, "serve", "--port", strconv.Itoa(port))
	cmd.Dir = workspace
	cmd.Stdout = nil
	cmd.Stderr = nil

	if len(envVars) > 0 {
		env := append([]string{}, os.Environ()...)
		env = append(env, envMapToPairs(envVars)...)
		cmd.Env = env
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	return &execProcess{cmd: cmd}, nil
}

func defaultHealthChecker(client *http.Client, now func() time.Time) healthCheckerFn {
	if client == nil {
		client = &http.Client{}
	}
	if now == nil {
		now = time.Now
	}

	return func(ctx context.Context, port int) HealthStatus {
		status := HealthStatus{State: HealthStateUnknown, LastCheck: now()}

		url := fmt.Sprintf("http://127.0.0.1:%d/global/health", port)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			status.State = HealthStateUnhealthy
			status.Error = err.Error()
			return status
		}

		resp, err := client.Do(req)
		if err != nil {
			status.State = HealthStateUnhealthy
			status.Error = err.Error()
			return status
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			if _, discardErr := io.Copy(io.Discard, resp.Body); discardErr != nil {
				slog.Default().Debug("failed to drain health probe response body", "port", port, "status", resp.StatusCode, "error", discardErr)
			}
			status.State = HealthStateUnhealthy
			status.Error = fmt.Sprintf("status %d", resp.StatusCode)
			return status
		}

		var payload struct {
			Healthy bool `json:"healthy"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			status.State = HealthStateUnhealthy
			status.Error = err.Error()
			return status
		}

		if payload.Healthy {
			status.State = HealthStateHealthy
			status.Error = ""
			return status
		}

		status.State = HealthStateUnhealthy
		status.Error = "daemon reported unhealthy"
		return status
	}
}

func defaultTerminalDialer(_ context.Context, _ SessionHandle) (TerminalConn, error) {
	return nil, ErrTerminalAttachDisabled
}

func defaultPortAvailable(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return true
	}
	if closeErr := conn.Close(); closeErr != nil {
		slog.Default().Debug("port probe close failed", "port", port, "error", closeErr)
	}
	return false
}

func isExpectedExitError(err error) bool {
	if err == nil {
		return true
	}
	if isProcessAlreadyDone(err) {
		return true
	}
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}

func isProcessAlreadyDone(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrProcessDone) {
		return true
	}
	if errors.Is(err, syscall.ESRCH) {
		return true
	}
	return false
}

func processExitMessage(err error) string {
	if err == nil {
		return "session process exited"
	}
	return err.Error()
}

func statusFromHealth(current SessionStatus, state HealthState) SessionStatus {
	switch state {
	case HealthStateUnhealthy:
		if current != SessionStatusStopped {
			return SessionStatusError
		}
	case HealthStateHealthy:
		if current == SessionStatusError || current == SessionStatusUnknown {
			return SessionStatusActive
		}
	}
	return current
}

func matchesSessionFilter(handle SessionHandle, filter SessionListFilter) bool {
	if filter.WorkspacePath != "" && handle.WorkspacePath != filter.WorkspacePath {
		return false
	}
	if filter.Status != "" && handle.Status != filter.Status {
		return false
	}
	if !labelsContain(handle.Labels, filter.LabelSelector) {
		return false
	}
	return true
}

func labelsContain(labels map[string]string, selector map[string]string) bool {
	if len(selector) == 0 {
		return true
	}
	if len(labels) == 0 {
		return false
	}
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func cloneSessionHandle(in SessionHandle) SessionHandle {
	out := in
	out.Labels = cloneStringMap(in.Labels)
	return out
}

func cloneCreateOpts(in CreateOpts) CreateOpts {
	out := in
	out.EnvVars = cloneStringMap(in.EnvVars)
	out.Labels = cloneStringMap(in.Labels)
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func envMapToPairs(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+env[key])
	}
	return pairs
}

func cleanupProcess(proc sessionProcess) {
	if proc == nil {
		return
	}
	_ = proc.Kill()
	go func() {
		_ = proc.Wait()
	}()
}
