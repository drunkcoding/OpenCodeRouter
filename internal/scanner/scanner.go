package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
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
		// Healthy but can't get project — register with minimal info.
		s.registry.Upsert(port, fmt.Sprintf("port-%d", port), fmt.Sprintf("/unknown/port-%d", port), health.Version)
		return
	}

	projectPath := project.Path
	if projectPath == "" {
		projectPath = "/unknown/" + project.ID
	}
	// Use the last folder name as the display name so it matches the slug.
	projectName := filepath.Base(projectPath)

	s.registry.Upsert(port, projectName, projectPath, health.Version)
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
		io.Copy(io.Discard, resp.Body)
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
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("project endpoint returned %d", resp.StatusCode)
	}

	var p projectResponse
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("failed to decode project response: %w", err)
	}
	return &p, nil
}
