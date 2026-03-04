package session

import (
	"fmt"
	"sort"
	"sync"

	"opencoderouter/internal/tui/components"
	"opencoderouter/internal/tui/model"

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

type terminalFactory func(host model.Host, session model.Session, width, height int, sendMsg func(tea.Msg)) (Terminal, error)

type Manager struct {
	mu          sync.RWMutex
	sessions    map[string]Terminal
	sendMsg     func(tea.Msg)
	newTerminal terminalFactory
}

func NewManager(sendMsg func(tea.Msg)) *Manager {
	return &Manager{
		sessions:    make(map[string]Terminal),
		sendMsg:     sendMsg,
		newTerminal: defaultTerminalFactory,
	}
}

func defaultTerminalFactory(host model.Host, session model.Session, width, height int, sendMsg func(tea.Msg)) (Terminal, error) {
	return components.NewSessionTerminal(host, session, width, height, sendMsg)
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
		return existing, nil
	}

	created, err := m.newTerminal(host, session, width, height, m.sendMsg)
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
	for id, terminal := range m.sessions {
		if terminal == nil || terminal.IsClosed() {
			delete(m.sessions, id)
		}
	}
	m.mu.Unlock()
}
