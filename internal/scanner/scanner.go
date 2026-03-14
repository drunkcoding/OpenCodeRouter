package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"opencoderouter/internal/registry"
)

// healthResponse is the shape of GET /global/health
type healthResponse struct {
	Healthy bool   `json:"healthy"`
	Version string `json:"version"`
}

// projectResponse is the shape of GET /project/current
type projectResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// Scanner periodically probes a port range on localhost for OpenCode serve instances.
type Scanner struct {
	registry    *registry.Registry
	portStart   int
	portEnd     int
	interval    time.Duration
	concurrency int
	client      *http.Client
	logger      *slog.Logger
}

// New creates a new Scanner.
func New(
	reg *registry.Registry,
	portStart, portEnd int,
	interval time.Duration,
	concurrency int,
	probeTimeout time.Duration,
	logger *slog.Logger,
) *Scanner {
	return &Scanner{
		registry:    reg,
		portStart:   portStart,
		portEnd:     portEnd,
		interval:    interval,
		concurrency: concurrency,
		client: &http.Client{
			Timeout: probeTimeout,
		},
		logger: logger,
	}
}

// Run starts the scan loop. Blocks until ctx is cancelled.
func (s *Scanner) Run(ctx context.Context) {
	s.logger.Info("scanner started",
		"port_range", fmt.Sprintf("%d-%d", s.portStart, s.portEnd),
		"interval", s.interval,
		"concurrency", s.concurrency,
	)

	// Run immediately on start, then on ticker.
	s.scan(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("scanner stopped")
			return
		case <-ticker.C:
			s.scan(ctx)
		}
	}
}

// scan probes all ports in the range concurrently.
func (s *Scanner) scan(ctx context.Context) {
	sem := make(chan struct{}, s.concurrency)
	var wg sync.WaitGroup

	for port := s.portStart; port <= s.portEnd; port++ {
		select {
		case <-ctx.Done():
			return
		default:
		}

		wg.Add(1)
		sem <- struct{}{} // acquire semaphore slot
		go func(p int) {
			defer wg.Done()
			defer func() { <-sem }() // release slot
			s.probePort(ctx, p)
		}(port)
	}

	wg.Wait()

	// Prune stale backends that haven't been seen recently.
	removed := s.registry.Prune()
	if len(removed) > 0 {
		s.logger.Info("pruned stale backends", "count", len(removed), "slugs", removed)
	}
}

// probePort checks if an OpenCode instance is running on the given port.
func (s *Scanner) probePort(ctx context.Context, port int) {
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	// Step 1: Health check.
	health, err := s.getHealth(ctx, baseURL)
	if err != nil {
		return // port not serving OpenCode (or down) — silent
	}
	if !health.Healthy {
		return
	}

	// Step 2: Get project info.
	project, err := s.getProject(ctx, baseURL)
	if err != nil {
		project = &projectResponse{
			ID:   fmt.Sprintf("port-%d", port),
			Name: fmt.Sprintf("port-%d", port),
			Path: fmt.Sprintf("/unknown/port-%d", port),
		}
	}

	projectPath := strings.TrimSpace(project.Path)
	if projectPath == "" {
		fallbackID := strings.TrimSpace(project.ID)
		if fallbackID == "" {
			fallbackID = fmt.Sprintf("port-%d", port)
		}
		projectPath = "/unknown/" + fallbackID
	}
	projectName := strings.TrimSpace(project.Name)
	if projectName == "" {
		projectName = filepath.Base(projectPath)
	}

	s.registry.Upsert(port, projectName, projectPath, health.Version)

	backend, ok := s.registry.LookupByPort(port)
	if !ok {
		return
	}

	sessions, err := s.getSessions(ctx, baseURL)
	if err != nil {
		s.logger.Debug("session probe failed", "port", port, "error", err)
		return
	}
	s.registry.ReplaceSessions(backend.Slug, sessions)
}

// getHealth calls GET /global/health on the target.
func (s *Scanner) getHealth(ctx context.Context, baseURL string) (*healthResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/global/health", nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if _, copyErr := io.Copy(io.Discard, resp.Body); copyErr != nil {
			s.logger.Debug("health response drain failed", "error", copyErr)
		}
		return nil, fmt.Errorf("health check returned %d", resp.StatusCode)
	}

	var h healthResponse
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return nil, fmt.Errorf("failed to decode health response: %w", err)
	}
	return &h, nil
}

// getProject calls GET /project/current on the target.
func (s *Scanner) getProject(ctx context.Context, baseURL string) (*projectResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/project/current", nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if _, copyErr := io.Copy(io.Discard, resp.Body); copyErr != nil {
			s.logger.Debug("project response drain failed", "error", copyErr)
		}
		return nil, fmt.Errorf("project endpoint returned %d", resp.StatusCode)
	}

	var payload map[string]interface{}
	decoder := json.NewDecoder(resp.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("failed to decode project response: %w", err)
	}

	return parseProjectPayload(payload), nil
}

func (s *Scanner) getSessions(ctx context.Context, baseURL string) ([]registry.SessionMetadata, error) {
	endpoints := []string{"/session", "/sessions"}
	var lastErr error

	for _, endpoint := range endpoints {
		sessions, status, err := s.getSessionsFromEndpoint(ctx, baseURL+endpoint)
		if err == nil {
			return sessions, nil
		}
		lastErr = err
		if status != http.StatusNotFound {
			return nil, err
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("session endpoint unavailable")
	}
	return nil, lastErr
}

func (s *Scanner) getSessionsFromEndpoint(ctx context.Context, endpointURL string) ([]registry.SessionMetadata, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpointURL, nil)
	if err != nil {
		return nil, 0, err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if _, copyErr := io.Copy(io.Discard, resp.Body); copyErr != nil {
			s.logger.Debug("session response drain failed", "error", copyErr)
		}
		return nil, resp.StatusCode, fmt.Errorf("session endpoint returned %d", resp.StatusCode)
	}

	var payload interface{}
	decoder := json.NewDecoder(resp.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to decode session response: %w", err)
	}

	return parseSessionPayload(payload), resp.StatusCode, nil
}

func parseProjectPayload(payload map[string]interface{}) *projectResponse {
	p := &projectResponse{
		ID:   firstString(payload, "id", "project_id", "projectId"),
		Name: firstString(payload, "name", "project_name", "projectName"),
		Path: firstString(payload, "path", "directory", "workspace_path", "workspacePath", "cwd"),
	}

	if p.Path == "" {
		if v, ok := payload["worktree"]; ok {
			p.Path = extractPath(v)
			if p.Name == "" {
				p.Name = extractName(v)
			}
		}
	}

	if p.Path == "" {
		if v, ok := payload["sandboxes"]; ok {
			p.Path, p.Name = extractFromSandboxes(v)
		}
	}

	if p.Name == "" && p.Path != "" {
		p.Name = filepath.Base(p.Path)
	}
	if p.ID == "" {
		switch {
		case p.Name != "":
			p.ID = p.Name
		case p.Path != "":
			p.ID = filepath.Base(p.Path)
		}
	}

	return p
}

func extractFromSandboxes(value interface{}) (string, string) {
	items, ok := value.([]interface{})
	if !ok {
		return "", ""
	}

	for _, item := range items {
		path := extractPath(item)
		if path == "" {
			continue
		}
		name := extractName(item)
		if name == "" {
			name = filepath.Base(path)
		}
		return path, name
	}

	return "", ""
}

func extractPath(value interface{}) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]interface{}:
		return firstString(v, "path", "directory", "cwd", "workspace_path", "workspacePath", "root")
	default:
		return ""
	}
}

func extractName(value interface{}) string {
	switch v := value.(type) {
	case map[string]interface{}:
		return firstString(v, "name", "project", "project_name", "projectName", "id")
	default:
		return ""
	}
}

func parseSessionPayload(payload interface{}) []registry.SessionMetadata {
	var entries []interface{}

	switch v := payload.(type) {
	case []interface{}:
		entries = v
	case map[string]interface{}:
		if list, ok := v["sessions"].([]interface{}); ok {
			entries = list
		} else if data, ok := v["data"].(map[string]interface{}); ok {
			if list, ok := data["sessions"].([]interface{}); ok {
				entries = list
			}
		} else if firstString(v, "id", "session_id", "sessionId", "sessionID") != "" {
			entries = []interface{}{v}
		}
	}

	if len(entries) == 0 {
		return nil
	}

	result := make([]registry.SessionMetadata, 0, len(entries))
	for _, entry := range entries {
		obj, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}
		session := parseSessionEntry(obj)
		if session.ID == "" {
			continue
		}
		result = append(result, session)
	}

	return result
}

func parseSessionEntry(payload map[string]interface{}) registry.SessionMetadata {
	return registry.SessionMetadata{
		ID:              firstString(payload, "id", "session_id", "sessionId", "sessionID"),
		Title:           firstString(payload, "title", "name"),
		Directory:       firstString(payload, "directory", "worktree", "cwd", "workspace_path", "workspacePath"),
		Status:          firstString(payload, "status", "state"),
		LastActivity:    firstTime(payload, "last_activity", "lastActivity", "updated", "updated_at", "updatedAt"),
		CreatedAt:       firstTime(payload, "created_at", "createdAt", "created", "time"),
		DaemonPort:      firstInt(payload, "daemon_port", "daemonPort", "port"),
		AttachedClients: firstInt(payload, "attached_clients", "attachedClients"),
	}
}

func firstString(payload map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case json.Number:
			if s := strings.TrimSpace(v.String()); s != "" {
				return s
			}
		case float64:
			return strconv.FormatInt(int64(v), 10)
		case int:
			return strconv.Itoa(v)
		case int64:
			return strconv.FormatInt(v, 10)
		}
	}
	return ""
}

func firstInt(payload map[string]interface{}, keys ...string) int {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case json.Number:
			if n, err := v.Int64(); err == nil {
				return int(n)
			}
			if f, err := v.Float64(); err == nil {
				return int(f)
			}
		case float64:
			return int(v)
		case float32:
			return int(v)
		case int:
			return v
		case int64:
			return int(v)
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				return n
			}
		}
	}
	return 0
}

func firstTime(payload map[string]interface{}, keys ...string) time.Time {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok {
			continue
		}
		t := parseFlexibleTime(value)
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

func parseFlexibleTime(value interface{}) time.Time {
	switch v := value.(type) {
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return time.Time{}
		}
		if ts, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return ts
		}
		if ts, err := time.Parse(time.RFC3339, s); err == nil {
			return ts
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return unixMaybeMillis(n)
		}
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return unixMaybeMillis(n)
		}
		if f, err := v.Float64(); err == nil {
			return unixMaybeMillis(int64(f))
		}
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return time.Time{}
		}
		return unixMaybeMillis(int64(v))
	case int64:
		return unixMaybeMillis(v)
	case int:
		return unixMaybeMillis(int64(v))
	}
	return time.Time{}
}

func unixMaybeMillis(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	if value > 1_000_000_000_000 {
		return time.UnixMilli(value)
	}
	return time.Unix(value, 0)
}
