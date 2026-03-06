package proxy

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"opencoderouter/internal/auth"
	"opencoderouter/internal/config"
	"opencoderouter/internal/registry"
)

// Router is the HTTP handler that proxies requests to discovered OpenCode backends.
// It supports two routing modes:
//  1. Host-based: "{slug}-{username}.local" → backend
//  2. Path-based: "/{slug}/..." → backend (prefix stripped)
//
// Unmatched requests get the dashboard.
type Router struct {
	registry  *registry.Registry
	cfg       config.Config
	logger    *slog.Logger
	handler   http.Handler
	uiHandler http.Handler

	wsMu           sync.Mutex
	wsConnections  map[string]string
	wsConnSeq      uint64
	wsPingInterval time.Duration
}

func writeJSONResponse(w http.ResponseWriter, payload any) {
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Default().Debug("failed to encode JSON response", "error", err)
	}
}

// New creates a new Router.
func New(reg *registry.Registry, cfg config.Config, logger *slog.Logger, uiHandler http.Handler) *Router {
	rt := &Router{
		registry:       reg,
		cfg:            cfg,
		logger:         logger,
		wsConnections:  make(map[string]string),
		wsPingInterval: defaultWSPingInterval,
		uiHandler:      uiHandler,
	}
	rt.handler = auth.Middleware(http.HandlerFunc(rt.routeRequest), auth.LoadFromEnv())
	return rt
}

// ServeHTTP implements http.Handler.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rt.handler.ServeHTTP(w, r)
}

func (rt *Router) routeRequest(w http.ResponseWriter, r *http.Request) {
	// Try host-based routing first.
	if slug := rt.slugFromHost(r.Host); slug != "" {
		if backend, ok := rt.registry.Lookup(slug); ok {
			rt.proxyTo(backend, w, r, "")
			return
		}
	}

	if rt.isWSRoute(r.URL.Path) {
		rt.handleWSProxy(w, r)
		return
	}

	// Try path-based routing: /{slug}/...
	if slug, remainder := rt.slugFromPath(r.URL.Path); slug != "" {
		if backend, ok := rt.registry.Lookup(slug); ok {
			rt.proxyTo(backend, w, r, remainder)
			return
		}
	}

	// API endpoints.
	switch r.URL.Path {
	case "/api/backends":
		rt.handleAPIBackends(w, r)
		return
	case "/api/health":
		rt.handleAPIHealth(w, r)
		return
	case "/api/resolve":
		rt.handleAPIResolve(w, r)
		return
	}

	// Dashboard.
	rt.handleDashboard(w, r)
}

// slugFromHost extracts the project slug from the Host header.
// Expected format: "{slug}-{username}.local" or "{slug}-{username}.local:port"
func (rt *Router) slugFromHost(host string) string {
	// Strip port if present.
	hostname := host
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		hostname = host[:idx]
	}

	// Check for ".local" suffix.
	if !strings.HasSuffix(hostname, ".local") {
		return ""
	}
	hostname = strings.TrimSuffix(hostname, ".local")

	// Check for "-{username}" suffix.
	suffix := "-" + rt.cfg.Username
	if !strings.HasSuffix(hostname, suffix) {
		return ""
	}

	slug := strings.TrimSuffix(hostname, suffix)
	if slug == "" {
		return ""
	}
	return slug
}

// slugFromPath extracts "slug" from "/{slug}/..." and returns the remainder path.
func (rt *Router) slugFromPath(path string) (slug, remainder string) {
	// Trim leading slash.
	trimmed := strings.TrimPrefix(path, "/")
	if trimmed == "" {
		return "", ""
	}

	parts := strings.SplitN(trimmed, "/", 2)
	slug = parts[0]
	if len(parts) > 1 {
		remainder = "/" + parts[1]
	} else {
		remainder = "/"
	}
	return slug, remainder
}

// proxyTo forwards the request to the given backend.
func (rt *Router) proxyTo(backend *registry.Backend, w http.ResponseWriter, r *http.Request, pathOverride string) {
	target, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", backend.Port))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
			if pathOverride != "" {
				pr.Out.URL.Path = pathOverride
				pr.Out.URL.RawPath = ""
			}
			pr.Out.Host = target.Host
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			rt.logger.Error("proxy error",
				"slug", backend.Slug,
				"target", target.String(),
				"error", err,
			)
			http.Error(w, fmt.Sprintf("backend %q unavailable: %v", backend.Slug, err), http.StatusBadGateway)
		},
		// Flush immediately for SSE/streaming.
		FlushInterval: -1,
	}

	rt.logger.Debug("proxying request",
		"slug", backend.Slug,
		"method", r.Method,
		"path", r.URL.Path,
		"target", fmt.Sprintf("%s%s", target.String(), pathOverride),
	)

	proxy.ServeHTTP(w, r)
}

// handleAPIBackends returns a JSON list of all backends.
func (rt *Router) handleAPIBackends(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	backends := rt.registry.All()
	type backendInfo struct {
		Slug        string    `json:"slug"`
		ProjectName string    `json:"project_name"`
		ProjectPath string    `json:"project_path"`
		Port        int       `json:"port"`
		Version     string    `json:"version"`
		Domain      string    `json:"domain"`
		PathPrefix  string    `json:"path_prefix"`
		URL         string    `json:"url"`
		LastSeen    time.Time `json:"last_seen"`
	}

	items := make([]backendInfo, 0, len(backends))
	for _, b := range backends {
		items = append(items, backendInfo{
			Slug:        b.Slug,
			ProjectName: b.ProjectName,
			ProjectPath: b.ProjectPath,
			Port:        b.Port,
			Version:     b.Version,
			Domain:      rt.cfg.DomainFor(b.Slug),
			PathPrefix:  fmt.Sprintf("/%s/", b.Slug),
			URL:         fmt.Sprintf("http://localhost:%d/%s/", rt.cfg.ListenPort, b.Slug),
			LastSeen:    b.LastSeen,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSONResponse(w, items)
}

// handleAPIHealth returns the router's own health status.
func (rt *Router) handleAPIHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	writeJSONResponse(w, map[string]interface{}{
		"healthy":  true,
		"username": rt.cfg.Username,
		"backends": rt.registry.Len(),
	})
}

// handleAPIResolve resolves a project path or name to its routing info.
// External agents use this to discover the correct URL for a project.
//
//	GET /api/resolve?path=/home/alice/myproject
//	GET /api/resolve?name=myproject
func (rt *Router) handleAPIResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	projectPath := r.URL.Query().Get("path")
	projectName := r.URL.Query().Get("name")

	if projectPath == "" && projectName == "" {
		http.Error(w, `missing "path" or "name" query parameter`, http.StatusBadRequest)
		return
	}

	var backend *registry.Backend
	var ok bool

	if projectPath != "" {
		backend, ok = rt.registry.LookupByPath(projectPath)
	} else {
		// Bare name lookup: slugify and look up directly.
		backend, ok = rt.registry.Lookup(registry.Slugify(projectName))
	}

	if !ok {
		query := projectPath
		if query == "" {
			query = projectName
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		writeJSONResponse(w, map[string]interface{}{
			"error":  "not_found",
			"query":  query,
			"detail": "no backend found for this project",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	writeJSONResponse(w, map[string]interface{}{
		"slug":         backend.Slug,
		"project_name": backend.ProjectName,
		"project_path": backend.ProjectPath,
		"port":         backend.Port,
		"version":      backend.Version,
		"domain":       rt.cfg.DomainFor(backend.Slug),
		"path_prefix":  fmt.Sprintf("/%s/", backend.Slug),
		"url":          fmt.Sprintf("http://localhost:%d/%s/", rt.cfg.ListenPort, backend.Slug),
		"last_seen":    backend.LastSeen,
	})
}

// handleDashboard serves the dashboard UI.
func (rt *Router) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if rt.uiHandler != nil {
		rt.uiHandler.ServeHTTP(w, r)
	} else {
		http.NotFound(w, r)
	}
}
