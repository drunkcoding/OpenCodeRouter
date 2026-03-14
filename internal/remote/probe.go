package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"opencoderouter/internal/model"
)

type cacheEntry struct {
	host      model.Host
	expiresAt time.Time
}

type CacheStore struct {
	mu      sync.RWMutex
	ttl     time.Duration
	nowFunc func() time.Time
	entries map[string]cacheEntry
}

func NewCacheStore(ttl time.Duration) *CacheStore {
	return &CacheStore{
		ttl:     ttl,
		nowFunc: time.Now,
		entries: make(map[string]cacheEntry),
	}
}

func (c *CacheStore) Get(key string) (model.Host, bool) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok {
		return model.Host{}, false
	}
	if c.nowFunc().After(entry.expiresAt) {
		c.mu.Lock()
		delete(c.entries, key)
		c.mu.Unlock()
		return model.Host{}, false
	}
	return entry.host, true
}

func (c *CacheStore) Set(key string, host model.Host) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheEntry{host: host, expiresAt: c.nowFunc().Add(c.ttl)}
}

func (c *CacheStore) PurgeExpired() int {
	now := c.nowFunc()
	removed := 0
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, entry := range c.entries {
		if now.After(entry.expiresAt) {
			delete(c.entries, key)
			removed++
		}
	}
	return removed
}

type ProbeService struct {
	opts   ProbeOptions
	runner Runner
	cache  *CacheStore
	nowFn  func() time.Time
	logger *slog.Logger
}

func NewProbeService(opts ProbeOptions, runner Runner, cache *CacheStore, logger *slog.Logger) *ProbeService {
	if runner == nil {
		runner = ExecRunner{}
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &ProbeService{
		opts:   opts,
		runner: runner,
		cache:  cache,
		nowFn:  time.Now,
		logger: logger,
	}
}

func (s *ProbeService) SetNowFunc(nowFn func() time.Time) {
	if nowFn == nil {
		s.nowFn = time.Now
		return
	}
	s.nowFn = nowFn
}

type probeJob struct {
	index int
	host  model.Host
}

type probeResult struct {
	index int
	host  model.Host
	err   error
}

const opencodeMissingSentinel = "__OCR_OPENCODE_MISSING__"

func (s *ProbeService) ProbeHosts(ctx context.Context, hosts []model.Host) ([]model.Host, error) {
	startedAt := time.Now()
	workerCount := s.opts.MaxParallel
	if workerCount < 1 {
		workerCount = 1
	}

	s.logger.Debug("probe hosts started",
		"host_count", len(hosts),
		"worker_count", workerCount,
	)

	if len(hosts) == 0 {
		s.logger.Debug("probe hosts completed",
			"host_count", 0,
			"result_count", 0,
			"error_count", 0,
			"duration_ms", time.Since(startedAt).Milliseconds(),
		)
		return nil, nil
	}

	if s.cache != nil {
		s.cache.PurgeExpired()
	}

	jumpProviders := jumpProviderSet(hosts)
	if len(jumpProviders) > 0 {
		s.transportPreflight(ctx, hosts, jumpProviders)
		propagateBlocked(s.logger, hosts)
	}

	updated := make([]model.Host, len(hosts))
	copy(updated, hosts)
	jobs := make(chan probeJob)
	results := make(chan probeResult)

	for i := 0; i < workerCount; i++ {
		go func() {
			for job := range jobs {
				jobCtx, cancel := s.hostProbeContext(ctx)
				h, err := s.probeHost(jobCtx, job.host)
				cancel()
				results <- probeResult{index: job.index, host: h, err: err}
			}
		}()
	}

	pending := 0
	for i, host := range hosts {
		if host.Transport == model.TransportBlocked {
			updated[i] = host
			s.logger.Debug("probe host skipped blocked",
				"host", host.Name,
				"blocked_by", host.BlockedBy,
			)
			continue
		}
		if s.cache != nil {
			if cached, ok := s.cache.Get(host.Name); ok {
				updated[i] = cached
				s.logger.Debug("probe cache hit", "host", host.Name)
				continue
			}
			s.logger.Debug("probe cache miss", "host", host.Name)
		}
		pending++
		jobs <- probeJob{index: i, host: host}
	}
	close(jobs)

	var probeErrs []error
	for i := 0; i < pending; i++ {
		select {
		case <-ctx.Done():
			err := fmt.Errorf("probe canceled: %w", ctx.Err())
			probeErrs = append(probeErrs, err)
			s.logger.Debug("probe host canceled",
				"err_kind", errorKind(err),
				"error", sanitizeErrorContext(err),
			)
		case res := <-results:
			updated[res.index] = res.host
			if res.err != nil {
				probeErrs = append(probeErrs, res.err)
			}
			if s.cache != nil {
				s.cache.Set(res.host.Name, res.host)
			}
		}
	}

	s.logger.Debug("probe hosts completed",
		"host_count", len(hosts),
		"result_count", len(updated),
		"error_count", len(probeErrs),
		"duration_ms", time.Since(startedAt).Milliseconds(),
	)

	if len(probeErrs) > 0 {
		return updated, errors.Join(probeErrs...)
	}
	return updated, nil
}

func (s *ProbeService) hostProbeContext(parent context.Context) (context.Context, context.CancelFunc) {
	if s.opts.SSH.ConnectTimeout <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, time.Duration(s.opts.SSH.ConnectTimeout)*time.Second)
}

func (s *ProbeService) scanPathsForHost(host model.Host) []string {
	if override, ok := s.opts.Overrides[host.Name]; ok && len(override.ScanPaths) > 0 {
		return override.ScanPaths
	}
	if len(s.opts.SessionScanPaths) > 0 {
		return s.opts.SessionScanPaths
	}
	return []string{"~"}
}

func (s *ProbeService) buildRemoteCmd(host model.Host) string {
	paths := s.scanPathsForHost(host)
	pathList := strings.Join(paths, " ")

	bin := host.OpencodeBin
	if bin == "" {
		bin = "opencode"
	}

	remoteCmd := fmt.Sprintf(
		`OC=$(command -v %s 2>/dev/null || echo "$HOME/.opencode/bin/%s"); `+
			`if [ -x "$OC" ]; then `+
			`find %s -maxdepth 2 -name .opencode -type d 2>/dev/null | while IFS= read -r d; do `+
			`(cd "$(dirname "$d")" && "$OC" session list --format json 2>/dev/null); `+
			`done; else printf '%s\n'; fi`,
		bin, bin, pathList, opencodeMissingSentinel,
	)

	s.logger.Debug("probe remote command built",
		"host", host.Name,
		"cmd", sanitizeCommandForLog(remoteCmd, pathList),
	)

	return remoteCmd
}

func (s *ProbeService) probeHost(ctx context.Context, host model.Host) (model.Host, error) {
	startedAt := time.Now()
	s.logger.Debug("probe host started", "host", host.Name)

	remoteCmd := s.buildRemoteCmd(host)
	args := s.buildSSHArgs(host, remoteCmd)
	s.logger.Debug("probe ssh args built",
		"host", host.Name,
		"arg_count", len(args),
	)

	out, runErr := s.runner.Run(ctx, "ssh", args...)
	var sessions []model.Session
	var parseErr error
	if runErr == nil && strings.TrimSpace(string(out)) != opencodeMissingSentinel {
		sessions, parseErr = s.parseSessions(out, host.Name)
	}

	result := classifyProbeResult(
		host.Name,
		out,
		runErr,
		parseErr,
		runErr != nil && isAuthError(host.Name, runErr, s.logger),
	)
	if result.err != nil {
		host.Status = result.status
		host.LastError = result.lastError
		s.logger.Error("probe host failed",
			"host", host.Name,
			"status", host.Status,
			"err_kind", result.errKind,
			"error", result.logError,
			"duration_ms", time.Since(startedAt).Milliseconds(),
		)
		return host, result.err
	}

	if s.opts.MaxDisplay > 0 && len(sessions) > s.opts.MaxDisplay {
		sessions = sessions[:s.opts.MaxDisplay]
	}

	host.Projects = groupSessionsByProject(sessions)
	host.Status = result.status
	host.LastSeen = s.nowFn()
	host.LastError = ""
	s.logger.Debug("probe host completed",
		"host", host.Name,
		"status", host.Status,
		"sessions", len(sessions),
		"duration_ms", time.Since(startedAt).Milliseconds(),
	)

	return host, nil
}

type probeClassification struct {
	status    model.HostStatus
	lastError string
	err       error
	errKind   string
	logError  string
}

func classifyProbeResult(hostName string, output []byte, runErr, parseErr error, authRequired bool) probeClassification {
	if runErr != nil {
		if authRequired {
			return probeClassification{
				status:    model.HostStatusAuthRequired,
				lastError: "password authentication required",
				err:       fmt.Errorf("probe host %q: auth required", hostName),
				errKind:   "auth",
				logError:  "authentication failed",
			}
		}
		return probeClassification{
			status:    model.HostStatusOffline,
			lastError: runErr.Error(),
			err:       fmt.Errorf("probe host %q: %w", hostName, runErr),
			errKind:   errorKind(runErr),
			logError:  sanitizeErrorContext(runErr),
		}
	}

	if strings.TrimSpace(string(output)) == opencodeMissingSentinel {
		err := fmt.Errorf("probe host %q: opencode binary not found", hostName)
		return probeClassification{
			status:    model.HostStatusOffline,
			lastError: "opencode binary not found",
			err:       err,
			errKind:   "opencode_missing",
			logError:  "opencode binary not found",
		}
	}

	if parseErr != nil {
		return probeClassification{
			status:    model.HostStatusError,
			lastError: parseErr.Error(),
			err:       fmt.Errorf("parse sessions for %q: %w", hostName, parseErr),
			errKind:   errorKind(parseErr),
			logError:  sanitizeErrorContext(parseErr),
		}
	}

	return probeClassification{status: model.HostStatusOnline}
}

func (s *ProbeService) buildSSHArgs(host model.Host, remoteCmd string) []string {
	args := make([]string, 0, 12)
	if s.opts.SSH.BatchMode {
		args = append(args, "-o", "BatchMode=yes")
	}
	if s.opts.SSH.ConnectTimeout > 0 {
		args = append(args, "-o", "ConnectTimeout="+strconv.Itoa(s.opts.SSH.ConnectTimeout))
	}
	if s.opts.SSH.ControlMaster != "" {
		args = append(args, "-o", "ControlMaster="+s.opts.SSH.ControlMaster)
	}
	if s.opts.SSH.ControlPersist > 0 {
		args = append(args, "-o", "ControlPersist="+strconv.Itoa(s.opts.SSH.ControlPersist))
	}
	if s.opts.SSH.ControlPath != "" {
		args = append(args, "-o", "ControlPath="+s.opts.SSH.ControlPath)
	}
	args = append(args, host.Name, remoteCmd)
	return args
}

type remoteSession struct {
	ID           string      `json:"id"`
	Project      string      `json:"project"`
	Title        string      `json:"title"`
	LastActivity string      `json:"last_activity"`
	Status       string      `json:"status"`
	MessageCount int         `json:"message_count"`
	Agents       []string    `json:"agents"`
	Updated      json.Number `json:"updated"`
	Created      json.Number `json:"created"`
	Directory    string      `json:"directory"`
	ProjectID    string      `json:"projectId"`
}

type remoteEnvelope struct {
	Sessions []remoteSession `json:"sessions"`
}

func (s *ProbeService) parseSessions(raw []byte, host string) ([]model.Session, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		s.logger.Debug("parse sessions decoded",
			"host", host,
			"records", 0,
			"sessions", 0,
			"raw_bytes", 0,
		)
		return nil, nil
	}

	var list []remoteSession

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	for dec.More() {
		var batch []remoteSession
		if err := dec.Decode(&batch); err != nil {
			var env remoteEnvelope
			if json.Unmarshal(trimmed, &env) == nil {
				list = env.Sessions
				break
			}
			s.logger.Error("parse sessions failed",
				"host", host,
				"err_kind", "parse",
				"error", "invalid session payload",
				"raw_bytes", len(trimmed),
			)
			return nil, err
		}
		list = append(list, batch...)
	}

	now := s.nowFn()
	thresholds := model.ActivityThresholds{
		Active: s.opts.ActiveThreshold,
		Idle:   s.opts.IdleThreshold,
	}

	sessions := make([]model.Session, 0, len(list))
	for _, rs := range list {
		status := mapSessionStatus(rs.Status)
		if status == model.SessionStatusArchived && !s.opts.ShowArchived {
			continue
		}
		lastActivity := resolveTimestamp(rs)
		project := resolveProject(rs)
		sessions = append(sessions, model.Session{
			ID:           rs.ID,
			Project:      project,
			Title:        rs.Title,
			Directory:    rs.Directory,
			LastActivity: lastActivity,
			Status:       status,
			MessageCount: rs.MessageCount,
			Agents:       append([]string(nil), rs.Agents...),
			Activity:     model.ResolveActivityState(lastActivity, now, thresholds),
		})
	}

	sortBy := strings.ToLower(strings.TrimSpace(s.opts.SortBy))
	if sortBy == "last_activity" {
		sort.SliceStable(sessions, func(i, j int) bool {
			return sessions[i].LastActivity.After(sessions[j].LastActivity)
		})
	}

	s.logger.Debug("parse sessions decoded",
		"host", host,
		"records", len(list),
		"sessions", len(sessions),
		"raw_bytes", len(trimmed),
	)
	return sessions, nil
}

func resolveTimestamp(rs remoteSession) time.Time {
	if rs.LastActivity != "" {
		return parseTimestamp(rs.LastActivity)
	}
	if rs.Updated.String() != "" {
		if ms, err := rs.Updated.Int64(); err == nil && ms > 0 {
			return time.UnixMilli(ms)
		}
	}
	if rs.Created.String() != "" {
		if ms, err := rs.Created.Int64(); err == nil && ms > 0 {
			return time.UnixMilli(ms)
		}
	}
	return time.Time{}
}

func resolveProject(rs remoteSession) string {
	if rs.Project != "" {
		return rs.Project
	}
	if rs.Directory != "" {
		return filepath.Base(rs.Directory)
	}
	return ""
}

func groupSessionsByProject(sessions []model.Session) []model.Project {
	byName := make(map[string][]model.Session)
	for _, session := range sessions {
		projectName := session.Project
		if strings.TrimSpace(projectName) == "" {
			projectName = "(unknown)"
		}
		byName[projectName] = append(byName[projectName], session)
	}

	projects := make([]model.Project, 0, len(byName))
	for name, grouped := range byName {
		projects = append(projects, model.Project{Name: name, Sessions: grouped})
	}
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].Name < projects[j].Name
	})
	return projects
}

func mapSessionStatus(status string) model.SessionStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "active", "running":
		return model.SessionStatusActive
	case "idle":
		return model.SessionStatusIdle
	case "archived", "closed", "done":
		return model.SessionStatusArchived
	default:
		return model.SessionStatusUnknown
	}
}

func parseTimestamp(value string) time.Time {
	if strings.TrimSpace(value) == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return t
}

func isAuthError(host string, err error, logger *slog.Logger) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	authIndicators := []string{
		"permission denied",
		"no more authentication methods",
		"publickey,password",
		"keyboard-interactive",
		"too many authentication failures",
		"authentication failed",
	}
	for _, indicator := range authIndicators {
		if strings.Contains(msg, indicator) {
			if logger != nil {
				logger.Error("probe auth indicator detected",
					"host", host,
					"err_kind", "auth",
					"error", "authentication failed",
				)
			}
			return true
		}
	}
	return false
}

func (s *ProbeService) AuthBootstrapCmd(host model.Host) string {
	controlPath := s.opts.SSH.ControlPath
	if controlPath == "" {
		controlPath = "~/.ssh/ocr-%C"
	}
	persist := s.opts.SSH.ControlPersist
	if persist <= 0 {
		persist = 600
	}
	timeout := s.opts.SSH.ConnectTimeout
	if timeout <= 0 {
		timeout = 10
	}

	cmd := fmt.Sprintf(
		"ssh -o ControlMaster=yes -o ControlPath=%s -o ControlPersist=%d -o ConnectTimeout=%d -Nf %s",
		controlPath,
		persist,
		timeout,
		host.Name,
	)
	return cmd
}

func jumpProviderSet(hosts []model.Host) map[string]bool {
	providers := make(map[string]bool)
	for _, h := range hosts {
		for _, dep := range h.DependsOn {
			providers[dep] = true
		}
	}
	return providers
}

func (s *ProbeService) transportPreflight(ctx context.Context, hosts []model.Host, providers map[string]bool) {
	startedAt := time.Now()
	s.logger.Debug("transport preflight started", "provider_count", len(providers))

	type preflightResult struct {
		idx    int
		status model.TransportStatus
		err    error
		dur    time.Duration
	}

	results := make(chan preflightResult)
	count := 0
	for i, h := range hosts {
		if !providers[h.Name] {
			continue
		}
		count++
		go func(idx int, host model.Host) {
			hostStarted := time.Now()
			s.logger.Debug("transport preflight host started", "host", host.Name)
			args := s.buildSSHArgs(host, "true")
			_, err := s.runner.Run(ctx, "ssh", args...)
			if err == nil {
				s.logger.Debug("transport preflight host result",
					"host", host.Name,
					"status", model.TransportReady,
					"duration_ms", time.Since(hostStarted).Milliseconds(),
				)
				results <- preflightResult{idx: idx, status: model.TransportReady, dur: time.Since(hostStarted)}
				return
			}
			if isAuthError(host.Name, err, s.logger) {
				s.logger.Debug("transport preflight host result",
					"host", host.Name,
					"status", model.TransportAuthRequired,
					"err_kind", "auth",
					"duration_ms", time.Since(hostStarted).Milliseconds(),
				)
				results <- preflightResult{idx: idx, status: model.TransportAuthRequired, err: err, dur: time.Since(hostStarted)}
				return
			}
			s.logger.Debug("transport preflight host result",
				"host", host.Name,
				"status", model.TransportUnreachable,
				"err_kind", errorKind(err),
				"duration_ms", time.Since(hostStarted).Milliseconds(),
			)
			results <- preflightResult{idx: idx, status: model.TransportUnreachable, err: err, dur: time.Since(hostStarted)}
		}(i, h)
	}

	readyCount := 0
	failureCount := 0
	for j := 0; j < count; j++ {
		res := <-results
		hosts[res.idx].Transport = res.status
		if res.err != nil {
			hosts[res.idx].TransportError = res.err.Error()
			failureCount++
		} else {
			readyCount++
		}
	}
	s.logger.Debug("transport preflight completed",
		"provider_count", count,
		"ready_count", readyCount,
		"failure_count", failureCount,
		"duration_ms", time.Since(startedAt).Milliseconds(),
	)
}

func propagateBlocked(logger *slog.Logger, hosts []model.Host) {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	startedAt := time.Now()
	blockedCount := 0

	aliasIndex := make(map[string]int, len(hosts))
	for i, h := range hosts {
		aliasIndex[h.Name] = i
	}

	for i := range hosts {
		if len(hosts[i].DependsOn) == 0 {
			continue
		}
		var blockers []string
		for _, dep := range hosts[i].DependsOn {
			if idx, ok := aliasIndex[dep]; ok {
				if hosts[idx].Transport != model.TransportReady && hosts[idx].Transport != model.TransportUnknown {
					blockers = append(blockers, dep)
				}
			}
		}
		if len(blockers) > 0 {
			hosts[i].Transport = model.TransportBlocked
			hosts[i].BlockedBy = blockers
			hosts[i].TransportError = fmt.Sprintf("blocked by: %s", strings.Join(blockers, ", "))
			blockedCount++
			logger.Debug("host transport blocked by dependency",
				"host", hosts[i].Name,
				"blocked_by", blockers,
			)
		}
	}
	logger.Debug("dependency block propagation completed",
		"host_count", len(hosts),
		"blocked_count", blockedCount,
		"duration_ms", time.Since(startedAt).Milliseconds(),
	)
}

func sanitizeCommandForLog(cmd, pathList string) string {
	sanitized := cmd
	if strings.TrimSpace(pathList) != "" {
		sanitized = strings.ReplaceAll(sanitized, pathList, "<scan_paths>")
	}
	sanitized = strings.Join(strings.Fields(sanitized), " ")
	if len(sanitized) > 240 {
		return sanitized[:240] + "..."
	}
	return sanitized
}

func errorKind(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}

	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "permission denied"),
		strings.Contains(msg, "no more authentication methods"),
		strings.Contains(msg, "publickey,password"),
		strings.Contains(msg, "keyboard-interactive"),
		strings.Contains(msg, "too many authentication failures"),
		strings.Contains(msg, "authentication failed"):
		return "auth"
	case strings.Contains(msg, "could not resolve hostname"):
		return "dns"
	case strings.Contains(msg, "connection refused"):
		return "connection_refused"
	case strings.Contains(msg, "no route to host"):
		return "no_route"
	case strings.Contains(msg, "timed out"):
		return "timeout"
	case strings.Contains(msg, "invalid character"), strings.Contains(msg, "cannot unmarshal"):
		return "parse"
	default:
		return "probe"
	}
}

func sanitizeErrorContext(err error) string {
	switch errorKind(err) {
	case "auth":
		return "authentication failed"
	case "dns":
		return "hostname resolution failed"
	case "connection_refused":
		return "connection refused"
	case "no_route":
		return "no route to host"
	case "timeout":
		return "connection timeout"
	case "canceled":
		return "operation canceled"
	case "parse":
		return "invalid session payload"
	default:
		return "probe command failed"
	}
}

func (s *ProbeService) MultiHopBootstrapCmds(host model.Host, allHosts []model.Host) []string {
	aliasIndex := make(map[string]int, len(allHosts))
	for i, h := range allHosts {
		aliasIndex[h.Name] = i
	}

	var cmds []string

	for _, hop := range host.JumpChain {
		if hop.External || hop.AliasRef == "" {
			continue
		}
		if idx, ok := aliasIndex[hop.AliasRef]; ok {
			jumpHost := allHosts[idx]
			if jumpHost.Transport == model.TransportAuthRequired || jumpHost.Status == model.HostStatusAuthRequired {
				cmds = append(cmds, s.AuthBootstrapCmd(jumpHost))
			}
		}
	}

	if host.Status == model.HostStatusAuthRequired || host.Transport == model.TransportAuthRequired {
		cmds = append(cmds, s.AuthBootstrapCmd(host))
	}

	return cmds
}
