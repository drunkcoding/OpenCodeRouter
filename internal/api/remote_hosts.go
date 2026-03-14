package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"opencoderouter/internal/model"
	"opencoderouter/internal/remote"
	tuiconfig "opencoderouter/internal/tui/config"
)

const defaultRemoteHostsCacheTTL = 60 * time.Second

type remoteHostsDiscoverer interface {
	Discover(ctx context.Context) ([]model.Host, error)
}

type remoteHostsProber interface {
	ProbeHosts(ctx context.Context, hosts []model.Host) ([]model.Host, error)
}

type remoteHostsPathSetter interface {
	SetSSHConfigPath(path string)
}

type RemoteHostsHandlerConfig struct {
	DiscoveryOptions remote.DiscoveryOptions
	ProbeOptions     remote.ProbeOptions
	CacheTTL         time.Duration
	Runner           remote.Runner
	Logger           *slog.Logger

	DiscoveryService remoteHostsDiscoverer
	ProbeService     remoteHostsProber
}

type RemoteHostsHandler struct {
	discovery remoteHostsDiscoverer
	probe     remoteHostsProber
	logger    *slog.Logger
	cacheTTL  time.Duration

	mu             sync.RWMutex
	lastHosts      []model.Host
	lastScannedAt  time.Time
	lastPartial    bool
	lastWarnings   []string
	lastConfigPath string
}

type remoteHostsResponse struct {
	Hosts    []remoteHostView `json:"hosts"`
	Cached   bool             `json:"cached"`
	Stale    bool             `json:"stale"`
	Partial  bool             `json:"partial"`
	LastScan string           `json:"lastScan,omitempty"`
	Warnings []string         `json:"warnings,omitempty"`
}

type remoteHostView struct {
	Name           string              `json:"name"`
	Address        string              `json:"address"`
	User           string              `json:"user,omitempty"`
	Label          string              `json:"label"`
	Priority       int                 `json:"priority,omitempty"`
	Status         string              `json:"status"`
	LastSeen       string              `json:"lastSeen,omitempty"`
	LastError      string              `json:"lastError,omitempty"`
	OpencodeBin    string              `json:"opencodeBin,omitempty"`
	SessionCount   int                 `json:"sessionCount"`
	Projects       []remoteProjectView `json:"projects,omitempty"`
	ProxyKind      string              `json:"proxyKind,omitempty"`
	ProxyJumpRaw   string              `json:"proxyJumpRaw,omitempty"`
	ProxyCommand   string              `json:"proxyCommand,omitempty"`
	DependsOn      []string            `json:"dependsOn,omitempty"`
	Dependents     []string            `json:"dependents,omitempty"`
	BlockedBy      []string            `json:"blockedBy,omitempty"`
	Transport      string              `json:"transport,omitempty"`
	TransportError string              `json:"transportError,omitempty"`
}

type remoteProjectView struct {
	Name     string              `json:"name"`
	Sessions []remoteSessionView `json:"sessions,omitempty"`
}

type remoteSessionView struct {
	ID           string   `json:"id"`
	Project      string   `json:"project,omitempty"`
	Title        string   `json:"title,omitempty"`
	Directory    string   `json:"directory,omitempty"`
	LastActivity string   `json:"lastActivity,omitempty"`
	Status       string   `json:"status"`
	MessageCount int      `json:"messageCount,omitempty"`
	Agents       []string `json:"agents,omitempty"`
	Activity     string   `json:"activity,omitempty"`
}

func NewRemoteHostsHandler(cfg RemoteHostsHandlerConfig) *RemoteHostsHandler {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	ttl := cfg.CacheTTL
	if ttl <= 0 {
		ttl = defaultRemoteHostsCacheTTL
	}

	discoverySvc := cfg.DiscoveryService
	if discoverySvc == nil {
		discoverySvc = remote.NewDiscoveryService(normalizeDiscoveryOptions(cfg.DiscoveryOptions), cfg.Runner, logger)
	}

	probeSvc := cfg.ProbeService
	if probeSvc == nil {
		probeSvc = remote.NewProbeService(normalizeProbeOptions(cfg.ProbeOptions), cfg.Runner, remote.NewCacheStore(ttl), logger)
	}

	return &RemoteHostsHandler{
		discovery: discoverySvc,
		probe:     probeSvc,
		logger:    logger,
		cacheTTL:  ttl,
	}
}

func (h *RemoteHostsHandler) Register(mux *http.ServeMux) {
	if h == nil || mux == nil {
		return
	}
	mux.HandleFunc("/api/remote/hosts", h.handleList)
}

func (h *RemoteHostsHandler) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	if h.discovery == nil || h.probe == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "remote host services unavailable", "REMOTE_HOSTS_UNAVAILABLE")
		return
	}

	refresh, err := parseBoolQuery(r, "refresh")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "INVALID_QUERY")
		return
	}

	sshConfigPath := strings.TrimSpace(r.URL.Query().Get("sshConfigPath"))
	if setter, ok := h.discovery.(remoteHostsPathSetter); ok {
		setter.SetSSHConfigPath(sshConfigPath)
	} else if sshConfigPath != "" {
		writeAPIError(w, http.StatusBadRequest, "sshConfigPath override unsupported", "SSH_CONFIG_OVERRIDE_UNSUPPORTED")
		return
	}

	if !refresh {
		if hosts, scannedAt, partial, warnings, ok := h.snapshotIfFresh(sshConfigPath); ok {
			writeJSON(w, http.StatusOK, remoteHostsResponse{
				Hosts:    toRemoteHostViews(hosts),
				Cached:   true,
				Stale:    false,
				Partial:  partial,
				LastScan: formatOptionalTime(scannedAt),
				Warnings: append([]string(nil), warnings...),
			})
			return
		}
	}

	hosts, warnings, partial, scanErr := h.scan(r.Context())
	if scanErr != nil {
		h.logger.Warn("remote host scan completed with errors", "error", remote.SanitizeLogError(scanErr), "host_count", len(hosts))
	}

	if len(hosts) == 0 && scanErr != nil {
		if cachedHosts, scannedAt, cachedPartial, cachedWarnings, ok := h.latestSnapshot(sshConfigPath); ok {
			warnings = append(warnings, cachedWarnings...)
			partial = partial || cachedPartial
			writeJSON(w, http.StatusOK, remoteHostsResponse{
				Hosts:    toRemoteHostViews(cachedHosts),
				Cached:   true,
				Stale:    true,
				Partial:  partial,
				LastScan: formatOptionalTime(scannedAt),
				Warnings: uniqueStrings(warnings),
			})
			return
		}

		writeAPIError(w, http.StatusServiceUnavailable, "remote host scan failed", "REMOTE_HOST_SCAN_FAILED")
		return
	}

	h.storeSnapshot(sshConfigPath, hosts, partial, warnings)
	writeJSON(w, http.StatusOK, remoteHostsResponse{
		Hosts:    toRemoteHostViews(hosts),
		Cached:   false,
		Stale:    false,
		Partial:  partial,
		LastScan: formatOptionalTime(time.Now().UTC()),
		Warnings: warnings,
	})
}

func parseBoolQuery(r *http.Request, key string) (bool, error) {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, errors.New("invalid query boolean: " + key)
	}
	return parsed, nil
}

func (h *RemoteHostsHandler) scan(ctx context.Context) ([]model.Host, []string, bool, error) {
	hosts, discoverErr := h.discovery.Discover(ctx)
	warnings := make([]string, 0)
	partial := false

	if discoverErr != nil {
		partial = true
		warnings = append(warnings, "discovery: "+remote.SanitizeLogError(discoverErr))
	}

	var probeErr error
	if len(hosts) > 0 {
		hosts, probeErr = h.probe.ProbeHosts(ctx, hosts)
		if probeErr != nil {
			partial = true
			warnings = append(warnings, "probe: "+remote.SanitizeLogError(probeErr))
		}
	}

	warnings = uniqueStrings(warnings)
	return hosts, warnings, partial, errors.Join(discoverErr, probeErr)
}

func (h *RemoteHostsHandler) snapshotIfFresh(configPath string) ([]model.Host, time.Time, bool, []string, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if len(h.lastHosts) == 0 || h.lastScannedAt.IsZero() {
		return nil, time.Time{}, false, nil, false
	}
	if h.lastConfigPath != configPath {
		return nil, time.Time{}, false, nil, false
	}
	if h.cacheTTL > 0 && time.Since(h.lastScannedAt) > h.cacheTTL {
		return nil, time.Time{}, false, nil, false
	}

	return cloneHosts(h.lastHosts), h.lastScannedAt, h.lastPartial, append([]string(nil), h.lastWarnings...), true
}

func (h *RemoteHostsHandler) latestSnapshot(configPath string) ([]model.Host, time.Time, bool, []string, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if len(h.lastHosts) == 0 || h.lastScannedAt.IsZero() {
		return nil, time.Time{}, false, nil, false
	}
	if h.lastConfigPath != configPath {
		return nil, time.Time{}, false, nil, false
	}

	return cloneHosts(h.lastHosts), h.lastScannedAt, h.lastPartial, append([]string(nil), h.lastWarnings...), true
}

func (h *RemoteHostsHandler) storeSnapshot(configPath string, hosts []model.Host, partial bool, warnings []string) {
	h.mu.Lock()
	h.lastHosts = cloneHosts(hosts)
	h.lastScannedAt = time.Now().UTC()
	h.lastPartial = partial
	h.lastWarnings = append([]string(nil), warnings...)
	h.lastConfigPath = configPath
	h.mu.Unlock()
}

func cloneHosts(hosts []model.Host) []model.Host {
	if len(hosts) == 0 {
		return nil
	}
	cloned := make([]model.Host, 0, len(hosts))
	for _, host := range hosts {
		cloned = append(cloned, cloneHost(host))
	}
	return cloned
}

func cloneHost(host model.Host) model.Host {
	cloned := host
	cloned.Projects = cloneProjects(host.Projects)
	cloned.JumpChain = append([]model.JumpHop(nil), host.JumpChain...)
	cloned.DependsOn = append([]string(nil), host.DependsOn...)
	cloned.Dependents = append([]string(nil), host.Dependents...)
	cloned.BlockedBy = append([]string(nil), host.BlockedBy...)
	return cloned
}

func cloneProjects(projects []model.Project) []model.Project {
	if len(projects) == 0 {
		return nil
	}
	cloned := make([]model.Project, 0, len(projects))
	for _, project := range projects {
		copied := model.Project{
			Name:     project.Name,
			Sessions: append([]model.Session(nil), project.Sessions...),
		}
		for i := range copied.Sessions {
			copied.Sessions[i].Agents = append([]string(nil), copied.Sessions[i].Agents...)
		}
		cloned = append(cloned, copied)
	}
	return cloned
}

func toRemoteHostViews(hosts []model.Host) []remoteHostView {
	if len(hosts) == 0 {
		return []remoteHostView{}
	}
	views := make([]remoteHostView, 0, len(hosts))
	for _, host := range hosts {
		views = append(views, remoteHostView{
			Name:           host.Name,
			Address:        host.Address,
			User:           host.User,
			Label:          host.Label,
			Priority:       host.Priority,
			Status:         string(host.Status),
			LastSeen:       formatOptionalTime(host.LastSeen),
			LastError:      host.LastError,
			OpencodeBin:    host.OpencodeBin,
			SessionCount:   host.SessionCount(),
			Projects:       toRemoteProjectViews(host.Projects),
			ProxyKind:      string(host.ProxyKind),
			ProxyJumpRaw:   host.ProxyJumpRaw,
			ProxyCommand:   host.ProxyCommand,
			DependsOn:      append([]string(nil), host.DependsOn...),
			Dependents:     append([]string(nil), host.Dependents...),
			BlockedBy:      append([]string(nil), host.BlockedBy...),
			Transport:      string(host.Transport),
			TransportError: host.TransportError,
		})
	}
	return views
}

func toRemoteProjectViews(projects []model.Project) []remoteProjectView {
	if len(projects) == 0 {
		return []remoteProjectView{}
	}
	views := make([]remoteProjectView, 0, len(projects))
	for _, project := range projects {
		views = append(views, remoteProjectView{
			Name:     project.Name,
			Sessions: toRemoteSessionViews(project.Sessions),
		})
	}
	return views
}

func toRemoteSessionViews(sessions []model.Session) []remoteSessionView {
	if len(sessions) == 0 {
		return []remoteSessionView{}
	}
	views := make([]remoteSessionView, 0, len(sessions))
	for _, session := range sessions {
		views = append(views, remoteSessionView{
			ID:           session.ID,
			Project:      session.Project,
			Title:        session.Title,
			Directory:    session.Directory,
			LastActivity: formatOptionalTime(session.LastActivity),
			Status:       string(session.Status),
			MessageCount: session.MessageCount,
			Agents:       append([]string(nil), session.Agents...),
			Activity:     string(session.Activity),
		})
	}
	return views
}

func formatOptionalTime(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(timeLayoutRFC3339Nano)
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeDiscoveryOptions(options remote.DiscoveryOptions) remote.DiscoveryOptions {
	defaults := tuiconfig.DefaultConfig()

	if len(options.Include) == 0 {
		options.Include = append([]string(nil), defaults.Hosts.Include...)
	}
	if len(options.Ignore) == 0 {
		options.Ignore = append([]string(nil), defaults.Hosts.Ignore...)
	}
	if len(options.Overrides) == 0 {
		options.Overrides = hostOverridesFromTUI(defaults.Hosts.Overrides)
	}

	return options
}

func normalizeProbeOptions(options remote.ProbeOptions) remote.ProbeOptions {
	defaults := tuiconfig.DefaultConfig()

	if options.MaxParallel <= 0 {
		options.MaxParallel = defaults.Polling.MaxParallel
	}
	if len(options.SessionScanPaths) == 0 {
		options.SessionScanPaths = append([]string(nil), defaults.Sessions.ScanPaths...)
	}
	if len(options.Overrides) == 0 {
		options.Overrides = hostOverridesFromTUI(defaults.Hosts.Overrides)
	}

	if strings.TrimSpace(options.SSH.ControlMaster) == "" {
		options.SSH.ControlMaster = defaults.SSH.ControlMaster
	}
	if options.SSH.ControlPersist <= 0 {
		options.SSH.ControlPersist = defaults.SSH.ControlPersist
	}
	if strings.TrimSpace(options.SSH.ControlPath) == "" {
		options.SSH.ControlPath = defaults.SSH.ControlPath
	}
	if options.SSH.ConnectTimeout <= 0 {
		options.SSH.ConnectTimeout = defaults.SSH.ConnectTimeout
	}

	if strings.TrimSpace(options.SortBy) == "" {
		options.SortBy = defaults.Sessions.SortBy
	}
	if options.MaxDisplay <= 0 {
		options.MaxDisplay = defaults.Sessions.MaxDisplay
	}
	if options.ActiveThreshold <= 0 {
		options.ActiveThreshold = defaults.Display.ActiveThreshold
	}
	if options.IdleThreshold <= 0 {
		options.IdleThreshold = defaults.Display.IdleThreshold
	}
	if options.IdleThreshold < options.ActiveThreshold {
		options.IdleThreshold = options.ActiveThreshold
	}

	return options
}

func hostOverridesFromTUI(overrides map[string]tuiconfig.HostOverride) map[string]remote.HostOverride {
	if len(overrides) == 0 {
		return nil
	}
	converted := make(map[string]remote.HostOverride, len(overrides))
	for alias, override := range overrides {
		converted[alias] = remote.HostOverride{
			Label:        override.Label,
			Priority:     override.Priority,
			OpencodePath: override.OpencodePath,
			ScanPaths:    append([]string(nil), override.ScanPaths...),
		}
	}
	return converted
}
