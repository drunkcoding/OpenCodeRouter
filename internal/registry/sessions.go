package registry

import (
	"sort"
	"strings"
	"time"
)

type SessionMetadata struct {
	ID              string    `json:"id"`
	Title           string    `json:"title,omitempty"`
	Directory       string    `json:"directory,omitempty"`
	Status          string    `json:"status,omitempty"`
	LastActivity    time.Time `json:"last_activity,omitempty"`
	CreatedAt       time.Time `json:"created_at,omitempty"`
	DaemonPort      int       `json:"daemon_port,omitempty"`
	AttachedClients int       `json:"attached_clients,omitempty"`
}

func (r *Registry) UpsertSession(backendSlug string, session SessionMetadata) bool {
	backendSlug = strings.TrimSpace(backendSlug)
	session.ID = strings.TrimSpace(session.ID)
	if backendSlug == "" || session.ID == "" {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.backends[backendSlug]; !ok {
		return false
	}

	backendSessions, ok := r.sessions[backendSlug]
	if !ok {
		backendSessions = make(map[string]SessionMetadata)
		r.sessions[backendSlug] = backendSessions
	}

	_, existed := backendSessions[session.ID]
	backendSessions[session.ID] = session
	return !existed
}

func (r *Registry) ReplaceSessions(backendSlug string, sessions []SessionMetadata) {
	backendSlug = strings.TrimSpace(backendSlug)
	if backendSlug == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.backends[backendSlug]; !ok {
		return
	}

	replacement := make(map[string]SessionMetadata, len(sessions))
	for _, session := range sessions {
		session.ID = strings.TrimSpace(session.ID)
		if session.ID == "" {
			continue
		}
		replacement[session.ID] = session
	}

	if len(replacement) == 0 {
		delete(r.sessions, backendSlug)
		return
	}

	r.sessions[backendSlug] = replacement
}

func (r *Registry) RemoveSession(backendSlug, sessionID string) bool {
	backendSlug = strings.TrimSpace(backendSlug)
	sessionID = strings.TrimSpace(sessionID)
	if backendSlug == "" || sessionID == "" {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	backendSessions, ok := r.sessions[backendSlug]
	if !ok {
		return false
	}
	if _, ok := backendSessions[sessionID]; !ok {
		return false
	}

	delete(backendSessions, sessionID)
	if len(backendSessions) == 0 {
		delete(r.sessions, backendSlug)
	}
	return true
}

func (r *Registry) ListSessions(backendSlug string) []SessionMetadata {
	backendSlug = strings.TrimSpace(backendSlug)
	if backendSlug == "" {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	backendSessions, ok := r.sessions[backendSlug]
	if !ok {
		return nil
	}

	result := make([]SessionMetadata, 0, len(backendSessions))
	for _, session := range backendSessions {
		result = append(result, session)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].ID < result[j].ID
	})
	return result
}

func (r *Registry) RemoveSessionsForBackend(backendSlug string) {
	backendSlug = strings.TrimSpace(backendSlug)
	if backendSlug == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, backendSlug)
}
