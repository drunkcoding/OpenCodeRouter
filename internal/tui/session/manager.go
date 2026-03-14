package session

import (
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"opencoderouter/internal/model"
	"opencoderouter/internal/tui/components"

	tea "charm.land/bubbletea/v2"
)

type Terminal interface {
	View() string
	WriteInput(data []byte) error
	Resize(width, height int) error
	Close() error
	IsClosed() bool
	Err() error
}

type terminalFactory func(host model.Host, session model.Session, width, height int, sendMsg func(tea.Msg), logger *slog.Logger, sshOpts []string) (Terminal, error)

type Manager struct {
	mu          sync.RWMutex
	sessions    map[string]Terminal
	sendMsg     func(tea.Msg)
	newTerminal terminalFactory
	logger      *slog.Logger
	sshOpts     []string
}

func NewManager(sendMsg func(tea.Msg), logger *slog.Logger, sshOpts []string) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		sessions:    make(map[string]Terminal),
		sendMsg:     sendMsg,
		newTerminal: defaultTerminalFactory,
		logger:      logger,
		sshOpts:     sshOpts,
	}
}

func defaultTerminalFactory(host model.Host, session model.Session, width, height int, sendMsg func(tea.Msg), logger *slog.Logger, sshOpts []string) (Terminal, error) {
	return components.NewSessionTerminal(host, session, width, height, sendMsg, logger, sshOpts)
}

func (m *Manager) Attach(host model.Host, session model.Session, width, height int) (Terminal, error) {
	if m == nil {
		return nil, fmt.Errorf("session manager is nil")
	}
	if session.ID == "" {
		return nil, fmt.Errorf("session id is required")
	}

	m.mu.RLock()
	existing := m.sessions[session.ID]
	m.mu.RUnlock()
	if existing != nil {
		m.logger.Debug("manager attach reusing existing", "session_id", session.ID)
		return existing, nil
	}

	m.logger.Debug("manager attach creating new", "session_id", session.ID)
	created, err := m.newTerminal(host, session, width, height, m.sendMsg, m.logger, m.sshOpts)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if existing = m.sessions[session.ID]; existing != nil {
		m.mu.Unlock()
		_ = created.Close()
		return existing, nil
	}
	m.sessions[session.ID] = created
	m.mu.Unlock()

	return created, nil
}

func (m *Manager) Get(sessionID string) Terminal {
	if m == nil {
		return nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[sessionID]
}

func (m *Manager) Has(sessionID string) bool {
	return m.Get(sessionID) != nil
}

func (m *Manager) Remove(sessionID string) {
	if m == nil {
		return
	}

	m.mu.Lock()
	t := m.sessions[sessionID]
	delete(m.sessions, sessionID)
	m.mu.Unlock()

	if t != nil {
		m.logger.Debug("manager remove", "session_id", sessionID)
		_ = t.Close()
	}
}

func (m *Manager) ActiveIDs() []string {
	if m == nil {
		return nil
	}

	m.mu.RLock()
	ids := make([]string, 0, len(m.sessions))
	for id, terminal := range m.sessions {
		if terminal != nil && !terminal.IsClosed() {
			ids = append(ids, id)
		}
	}
	m.mu.RUnlock()

	sort.Strings(ids)
	return ids
}

func (m *Manager) ResizeAll(width, height int) {
	if m == nil {
		return
	}

	m.mu.RLock()
	terminals := make([]Terminal, 0, len(m.sessions))
	for _, terminal := range m.sessions {
		if terminal != nil && !terminal.IsClosed() {
			terminals = append(terminals, terminal)
		}
	}
	m.mu.RUnlock()

	for _, terminal := range terminals {
		_ = terminal.Resize(width, height)
	}
}

func (m *Manager) Shutdown() {
	if m == nil {
		return
	}

	m.mu.Lock()
	terminals := make([]Terminal, 0, len(m.sessions))
	for _, terminal := range m.sessions {
		if terminal != nil {
			terminals = append(terminals, terminal)
		}
	}
	m.sessions = make(map[string]Terminal)
	m.mu.Unlock()

	for _, terminal := range terminals {
		_ = terminal.Close()
	}
}

func (m *Manager) CleanupClosed() {
	if m == nil {
		return
	}

	m.mu.Lock()
	removed := 0
	for id, terminal := range m.sessions {
		if terminal == nil || terminal.IsClosed() {
			delete(m.sessions, id)
			removed++
		}
	}
	m.mu.Unlock()

	if removed > 0 {
		m.logger.Debug("manager cleanup", "removed", removed)
	}
}
