package registry

import (
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Backend represents a discovered OpenCode serve instance.
type Backend struct {
	Port        int       `json:"port"`
	ProjectName string    `json:"project_name"`
	ProjectPath string    `json:"project_path"`
	Slug        string    `json:"slug"`
	Version     string    `json:"version"`
	LastSeen    time.Time `json:"last_seen"`
}

// Healthy returns true if the backend was seen recently.
func (b *Backend) Healthy(staleAfter time.Duration) bool {
	return time.Since(b.LastSeen) < staleAfter
}

// Registry is a thread-safe store of discovered OpenCode backends.
type Registry struct {
	mu         sync.RWMutex
	backends   map[string]*Backend // slug → backend
	byPort     map[int]string      // port → slug (for fast dedup)
	staleAfter time.Duration
	logger     *slog.Logger
}

// New creates a new Registry.
func New(staleAfter time.Duration, logger *slog.Logger) *Registry {
	return &Registry{
		backends:   make(map[string]*Backend),
		byPort:     make(map[int]string),
		staleAfter: staleAfter,
		logger:     logger,
	}
}

// Upsert adds or updates a backend. Returns true if this is a new entry.
func (r *Registry) Upsert(port int, projectName, projectPath, version string) bool {
	slug := Slugify(projectPath)

	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if this port was previously registered under a different slug.
	if oldSlug, ok := r.byPort[port]; ok && oldSlug != slug {
		delete(r.backends, oldSlug)
		r.logger.Info("backend project changed", "port", port, "old_slug", oldSlug, "new_slug", slug)
	}

	existing, isUpdate := r.backends[slug]
	if isUpdate {
		// If a different port now serves this slug, remove the old port mapping.
		if existing.Port != port {
			delete(r.byPort, existing.Port)
		}
		existing.Port = port
		existing.ProjectName = projectName
		existing.ProjectPath = projectPath
		existing.Version = version
		existing.LastSeen = time.Now()
		r.byPort[port] = slug
		return false
	}

	// Handle slug collision: two different projects produce the same slug.
	// Disambiguate by appending the port.
	if _, collision := r.backends[slug]; collision {
		slug = slug + "-" + strings.TrimLeft(string(rune(port)), "0")
	}

	r.backends[slug] = &Backend{
		Port:        port,
		ProjectName: projectName,
		ProjectPath: projectPath,
		Slug:        slug,
		Version:     version,
		LastSeen:    time.Now(),
	}
	r.byPort[port] = slug
	r.logger.Info("backend registered", "slug", slug, "port", port, "project", projectName)
	return true
}

// MarkUnseen marks ports NOT in the seen set as potentially stale.
// Returns slugs that were removed because they exceeded staleAfter.
func (r *Registry) Prune() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var removed []string
	for slug, b := range r.backends {
		if time.Since(b.LastSeen) > r.staleAfter {
			delete(r.backends, slug)
			delete(r.byPort, b.Port)
			r.logger.Info("backend removed (stale)", "slug", slug, "port", b.Port)
			removed = append(removed, slug)
		}
	}
	return removed
}

// Lookup finds a backend by slug.
func (r *Registry) Lookup(slug string) (*Backend, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.backends[slug]
	if !ok {
		return nil, false
	}
	// Return a copy to avoid races.
	copy := *b
	return &copy, true
}

// LookupByPort finds a backend by its port.
func (r *Registry) LookupByPort(port int) (*Backend, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	slug, ok := r.byPort[port]
	if !ok {
		return nil, false
	}
	b := r.backends[slug]
	copy := *b
	return &copy, true
}

// All returns a snapshot of all backends.
func (r *Registry) All() []*Backend {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*Backend, 0, len(r.backends))
	for _, b := range r.backends {
		copy := *b
		result = append(result, &copy)
	}
	return result
}

// Slugs returns all registered slugs.
func (r *Registry) Slugs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]string, 0, len(r.backends))
	for slug := range r.backends {
		result = append(result, slug)
	}
	return result
}

// Len returns the number of registered backends.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.backends)
}

// Slugify converts a project path to a hostname-safe slug.
// "/home/alice/projects/My Awesome Project" → "my-awesome-project"
func Slugify(projectPath string) string {
	base := filepath.Base(projectPath)
	slug := strings.ToLower(base)
	slug = nonAlphaNum.ReplaceAllString(slug, "-")
	slug = multiHyphen.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "default"
	}
	return slug
}

var (
	nonAlphaNum = regexp.MustCompile(`[^a-z0-9-]`)
	multiHyphen = regexp.MustCompile(`-+`)
)
