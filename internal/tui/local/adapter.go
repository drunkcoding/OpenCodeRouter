package local

import (
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"opencoderouter/internal/model"
	"opencoderouter/internal/registry"
)

const (
	LocalHostName    = "localhost"
	LocalHostLabel   = "localhost (local)"
	localHostAddress = "127.0.0.1"
)

var ErrNoLocalBackends = errors.New("no local backends discovered")

type Adapter struct {
	registry *registry.Registry
	nowFn    func() time.Time

	activeThreshold time.Duration
	idleThreshold   time.Duration
}

func NewAdapter(reg *registry.Registry, activeThreshold, idleThreshold time.Duration) *Adapter {
	if activeThreshold <= 0 {
		activeThreshold = 10 * time.Minute
	}
	if idleThreshold <= 0 {
		idleThreshold = 24 * time.Hour
	}
	if activeThreshold > idleThreshold {
		activeThreshold = idleThreshold
	}

	return &Adapter{
		registry:        reg,
		nowFn:           time.Now,
		activeThreshold: activeThreshold,
		idleThreshold:   idleThreshold,
	}
}

func (a *Adapter) GetLocalHost() (model.Host, error) {
	if a == nil || a.registry == nil {
		return model.Host{}, errors.New("local registry is not configured")
	}

	backends := a.registry.All()
	if len(backends) == 0 {
		return model.Host{}, ErrNoLocalBackends
	}

	now := a.now()
	thresholds := model.ActivityThresholds{Active: a.activeThreshold, Idle: a.idleThreshold}

	projects := make([]model.Project, 0, len(backends))
	latestSeen := time.Time{}

	for _, backend := range backends {
		if backend == nil {
			continue
		}

		if backend.LastSeen.After(latestSeen) {
			latestSeen = backend.LastSeen
		}

		projectName := projectNameFromBackend(backend)
		metadata := a.registry.ListSessions(backend.Slug)

		sessions := make([]model.Session, 0, len(metadata))
		for _, sessionMeta := range metadata {
			sessionID := strings.TrimSpace(sessionMeta.ID)
			if sessionID == "" {
				continue
			}

			directory := strings.TrimSpace(sessionMeta.Directory)
			if directory == "" {
				directory = strings.TrimSpace(backend.ProjectPath)
			}

			lastActivity := sessionMeta.LastActivity
			if lastActivity.IsZero() {
				lastActivity = backend.LastSeen
			}

			title := strings.TrimSpace(sessionMeta.Title)
			if title == "" {
				title = sessionID
			}

			sessions = append(sessions, model.Session{
				ID:           sessionID,
				Project:      projectName,
				Title:        title,
				Directory:    directory,
				LastActivity: lastActivity,
				Status:       mapSessionStatus(sessionMeta.Status),
				Activity:     model.ResolveActivityState(lastActivity, now, thresholds),
			})
		}

		sort.SliceStable(sessions, func(i, j int) bool {
			if sessions[i].LastActivity.Equal(sessions[j].LastActivity) {
				return sessions[i].ID < sessions[j].ID
			}
			return sessions[i].LastActivity.After(sessions[j].LastActivity)
		})

		projects = append(projects, model.Project{Name: projectName, Sessions: sessions})
	}

	if len(projects) == 0 {
		return model.Host{}, ErrNoLocalBackends
	}

	sort.Slice(projects, func(i, j int) bool {
		return projects[i].Name < projects[j].Name
	})

	return model.Host{
		Name:     LocalHostName,
		Address:  localHostAddress,
		Label:    LocalHostLabel,
		Status:   model.HostStatusOnline,
		LastSeen: latestSeen,
		Projects: projects,
	}, nil
}

func (a *Adapter) now() time.Time {
	if a != nil && a.nowFn != nil {
		return a.nowFn()
	}
	return time.Now()
}

func projectNameFromBackend(backend *registry.Backend) string {
	if backend == nil {
		return "(unknown)"
	}

	if name := strings.TrimSpace(backend.ProjectName); name != "" {
		return name
	}

	if projectPath := strings.TrimSpace(backend.ProjectPath); projectPath != "" {
		base := strings.TrimSpace(filepath.Base(projectPath))
		if base != "" && base != "." && base != string(filepath.Separator) {
			return base
		}
	}

	if slug := strings.TrimSpace(backend.Slug); slug != "" {
		return slug
	}

	return "(unknown)"
}

func mapSessionStatus(status string) model.SessionStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "active", "running", "online", "ready":
		return model.SessionStatusActive
	case "idle", "paused":
		return model.SessionStatusIdle
	case "archived", "closed", "done", "stopped", "terminated", "offline":
		return model.SessionStatusArchived
	default:
		return model.SessionStatusUnknown
	}
}
