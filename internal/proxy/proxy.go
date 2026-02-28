package proxy

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

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
	registry *registry.Registry
	cfg      config.Config
	logger   *slog.Logger
}

// New creates a new Router.
func New(reg *registry.Registry, cfg config.Config, logger *slog.Logger) *Router {
	return &Router{
		registry: reg,
		cfg:      cfg,
		logger:   logger,
	}
}

// ServeHTTP implements http.Handler.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Try host-based routing first.
	if slug := rt.slugFromHost(r.Host); slug != "" {
		if backend, ok := rt.registry.Lookup(slug); ok {
			rt.proxyTo(backend, w, r, "")
			return
		}
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
	json.NewEncoder(w).Encode(items)
}

// handleAPIHealth returns the router's own health status.
func (rt *Router) handleAPIHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
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
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":  "not_found",
			"query":  query,
			"detail": "no backend found for this project",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
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

// handleDashboard renders an HTML page listing all discovered backends.
func (rt *Router) handleDashboard(w http.ResponseWriter, r *http.Request) {
	backends := rt.registry.All()

	type entry struct {
		Slug        string
		ProjectName string
		ProjectPath string
		Port        int
		Version     string
		Domain      string
		PathURL     string
		LastSeen    string
		Healthy     bool
	}

	entries := make([]entry, 0, len(backends))
	for _, b := range backends {
		entries = append(entries, entry{
			Slug:        b.Slug,
			ProjectName: b.ProjectName,
			ProjectPath: b.ProjectPath,
			Port:        b.Port,
			Version:     b.Version,
			Domain:      rt.cfg.DomainFor(b.Slug),
			PathURL:     fmt.Sprintf("/%s/", b.Slug),
			LastSeen:    b.LastSeen.Format(time.RFC3339),
			Healthy:     b.Healthy(rt.cfg.StaleAfter),
		})
	}

	data := struct {
		Username string
		Entries  []entry
		MDNS     bool
	}{
		Username: rt.cfg.Username,
		Entries:  entries,
		MDNS:     rt.cfg.EnableMDNS,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := dashboardTmpl.Execute(w, data); err != nil {
		rt.logger.Error("dashboard render error", "error", err)
	}
}

var dashboardTmpl = template.Must(template.New("dashboard").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>OpenCode Router — {{.Username}}</title>
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
         background: #0f1117; color: #e1e4e8; padding: 2rem; }
  h1 { font-size: 1.5rem; margin-bottom: 0.5rem; color: #58a6ff; }
  .sub { color: #8b949e; margin-bottom: 2rem; font-size: 0.9rem; }
  table { width: 100%; border-collapse: collapse; }
  th, td { text-align: left; padding: 0.7rem 1rem; border-bottom: 1px solid #21262d; }
  th { color: #8b949e; font-weight: 600; font-size: 0.8rem; text-transform: uppercase; letter-spacing: 0.05em; }
  td { font-size: 0.9rem; }
  a { color: #58a6ff; text-decoration: none; }
  a:hover { text-decoration: underline; }
  .dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 6px; }
  .dot.healthy { background: #3fb950; }
  .dot.stale { background: #f85149; }
  .empty { color: #8b949e; text-align: center; padding: 3rem; }
  code { background: #161b22; padding: 2px 6px; border-radius: 4px; font-size: 0.85rem; }
  .footer { margin-top: 2rem; color: #484f58; font-size: 0.8rem; }
</style>
</head>
<body>
<h1>OpenCode Router</h1>
<p class="sub">User: <strong>{{.Username}}</strong> · mDNS: {{if .MDNS}}enabled{{else}}disabled{{end}} · <a href="/api/backends">JSON API</a></p>

{{if .Entries}}
<table>
<thead>
<tr><th>Status</th><th>Project</th><th>Slug</th><th>Backend</th><th>Domain</th><th>Path</th><th>Version</th><th>Last Seen</th></tr>
</thead>
<tbody>
{{range .Entries}}
<tr>
  <td><span class="dot {{if .Healthy}}healthy{{else}}stale{{end}}"></span>{{if .Healthy}}Healthy{{else}}Stale{{end}}</td>
  <td>{{.ProjectName}}</td>
  <td><code>{{.Slug}}</code></td>
  <td><code>127.0.0.1:{{.Port}}</code></td>
  <td><a href="http://{{.Domain}}">{{.Domain}}</a></td>
  <td><a href="{{.PathURL}}">{{.PathURL}}</a></td>
  <td>{{.Version}}</td>
  <td>{{.LastSeen}}</td>
</tr>
{{end}}
</tbody>
</table>
{{else}}
<p class="empty">No OpenCode instances discovered yet. Scanning ports…</p>
{{end}}

<p class="footer">Scan range: {{.Username}} · Auto-refreshes every scan interval</p>
</body>
</html>
`))
