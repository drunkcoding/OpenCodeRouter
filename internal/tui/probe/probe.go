package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"opencoderouter/internal/tui/config"
	"opencoderouter/internal/tui/model"
)

// Runner executes external commands and returns stdout bytes.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner is a Runner backed by os/exec.
type ExecRunner struct{}

// Run executes a command and preserves stderr details in failures.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		stderr := strings.TrimSpace(string(exitErr.Stderr))
		if stderr != "" {
			return nil, fmt.Errorf("run %s %v: %w: %s", name, args, err, stderr)
		}
	}
	return nil, fmt.Errorf("run %s %v: %w", name, args, err)
}

// ProbeService executes per-host SSH probes and converts output to domain models.
type ProbeService struct {
	cfg    config.Config
	runner Runner
	cache  *CacheStore
	nowFn  func() time.Time
}

// NewProbeService creates a probe service with worker-pool execution.
func NewProbeService(cfg config.Config, runner Runner, cache *CacheStore) *ProbeService {
	if runner == nil {
		runner = ExecRunner{}
	}
	return &ProbeService{
		cfg:    cfg,
		runner: runner,
		cache:  cache,
		nowFn:  time.Now,
	}
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

// ProbeHosts runs transport preflight for jump providers, then probes all hosts.
// Hosts with unresolved jump dependencies are marked TransportBlocked.
func (s *ProbeService) ProbeHosts(ctx context.Context, hosts []model.Host) ([]model.Host, error) {
	if len(hosts) == 0 {
		return nil, nil
	}

	if s.cache != nil {
		s.cache.PurgeExpired()
	}

	// Phase 1: Transport preflight for jump providers
	jumpProviders := jumpProviderSet(hosts)
	if len(jumpProviders) > 0 {
		s.transportPreflight(ctx, hosts, jumpProviders)
		propagateBlocked(hosts)
	}

	// Phase 2: Session probe (skip blocked hosts)
	updated := make([]model.Host, len(hosts))
	jobs := make(chan probeJob)
	results := make(chan probeResult)

	workerCount := s.cfg.Polling.MaxParallel
	if workerCount < 1 {
		workerCount = 1
	}

	for i := 0; i < workerCount; i++ {
		go func() {
			for job := range jobs {
				h, err := s.probeHost(ctx, job.host)
				results <- probeResult{index: job.index, host: h, err: err}
			}
		}()
	}

	pending := 0
	for i, host := range hosts {
		// Skip blocked hosts
		if host.Transport == model.TransportBlocked {
			updated[i] = host
			continue
		}
		if s.cache != nil {
			if cached, ok := s.cache.Get(host.Name); ok {
				updated[i] = cached
				continue
			}
		}
		pending++
		jobs <- probeJob{index: i, host: host}
	}
	close(jobs)

	var probeErrs []error
	for i := 0; i < pending; i++ {
		select {
		case <-ctx.Done():
			probeErrs = append(probeErrs, fmt.Errorf("probe canceled: %w", ctx.Err()))
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

	if len(probeErrs) > 0 {
		return updated, errors.Join(probeErrs...)
	}
	return updated, nil
}

func (s *ProbeService) scanPathsForHost(host model.Host) []string {
	if override, ok := s.cfg.Hosts.Overrides[host.Name]; ok && len(override.ScanPaths) > 0 {
		return override.ScanPaths
	}
	if len(s.cfg.Sessions.ScanPaths) > 0 {
		return s.cfg.Sessions.ScanPaths
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

	return fmt.Sprintf(
		`OC=$(command -v %s 2>/dev/null || echo "$HOME/.opencode/bin/%s"); `+
			`if [ -x "$OC" ]; then `+
			`find %s -maxdepth 2 -name .opencode -type d 2>/dev/null | while IFS= read -r d; do `+
			`(cd "$(dirname "$d")" && "$OC" session list --format json 2>/dev/null); `+
			`done; fi`,
		bin, bin, pathList,
	)
}

func (s *ProbeService) probeHost(ctx context.Context, host model.Host) (model.Host, error) {
	remoteCmd := s.buildRemoteCmd(host)

	args := s.buildSSHArgs(host, remoteCmd)
	out, err := s.runner.Run(ctx, "ssh", args...)
	if err != nil {
		if isAuthError(err) {
			host.Status = model.HostStatusAuthRequired
			host.LastError = "password authentication required"
			return host, fmt.Errorf("probe host %q: auth required", host.Name)
		}
		host.Status = model.HostStatusOffline
		host.LastError = err.Error()
		return host, fmt.Errorf("probe host %q: %w", host.Name, err)
	}

	sessions, parseErr := s.parseSessions(out)
	if parseErr != nil {
		host.Status = model.HostStatusError
		host.LastError = parseErr.Error()
		return host, fmt.Errorf("parse sessions for %q: %w", host.Name, parseErr)
	}

	if s.cfg.Sessions.MaxDisplay > 0 && len(sessions) > s.cfg.Sessions.MaxDisplay {
		sessions = sessions[:s.cfg.Sessions.MaxDisplay]
	}

	host.Projects = groupSessionsByProject(sessions)
	host.Status = model.HostStatusOnline
	host.LastSeen = s.nowFn()
	host.LastError = ""

	return host, nil
}

// buildSSHArgs returns ssh options and target command for a probe call.
func (s *ProbeService) buildSSHArgs(host model.Host, remoteCmd string) []string {
	args := make([]string, 0, 12)
	if s.cfg.SSH.BatchMode {
		args = append(args, "-o", "BatchMode=yes")
	}
	if s.cfg.SSH.ConnectTimeout > 0 {
		args = append(args, "-o", "ConnectTimeout="+strconv.Itoa(s.cfg.SSH.ConnectTimeout))
	}
	if s.cfg.SSH.ControlMaster != "" {
		args = append(args, "-o", "ControlMaster="+s.cfg.SSH.ControlMaster)
	}
	if s.cfg.SSH.ControlPersist > 0 {
		args = append(args, "-o", "ControlPersist="+strconv.Itoa(s.cfg.SSH.ControlPersist))
	}
	if s.cfg.SSH.ControlPath != "" {
		args = append(args, "-o", "ControlPath="+s.cfg.SSH.ControlPath)
	}
	args = append(args, host.Name, remoteCmd)
	return args
}

type remoteSession struct {
	ID           string   `json:"id"`
	Project      string   `json:"project"`
	Title        string   `json:"title"`
	LastActivity string   `json:"last_activity"`
	Status       string   `json:"status"`
	MessageCount int      `json:"message_count"`
	Agents       []string `json:"agents"`
	// opencode native fields
	Updated   json.Number `json:"updated"`
	Created   json.Number `json:"created"`
	Directory string      `json:"directory"`
	ProjectID string      `json:"projectId"`
}

type remoteEnvelope struct {
	Sessions []remoteSession `json:"sessions"`
}

func (s *ProbeService) parseSessions(raw []byte) ([]model.Session, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
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
			return nil, err
		}
		list = append(list, batch...)
	}

	now := s.nowFn()
	thresholds := model.ActivityThresholds{
		Active: s.cfg.Display.ActiveThreshold,
		Idle:   s.cfg.Display.IdleThreshold,
	}

	sessions := make([]model.Session, 0, len(list))
	for _, rs := range list {
		status := mapSessionStatus(rs.Status)
		if status == model.SessionStatusArchived && !s.cfg.Sessions.ShowArchived {
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

	if s.cfg.Sessions.SortBy == "last_activity" {
		sort.SliceStable(sessions, func(i, j int) bool {
			return sessions[i].LastActivity.After(sessions[j].LastActivity)
		})
	}

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

// groupSessionsByProject folds sessions into Project buckets.
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

// mapSessionStatus converts wire status strings into typed status.
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

// parseTimestamp parses RFC3339 timestamps and falls back to zero time.
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

// isAuthError checks whether an SSH error indicates authentication failure
// (as opposed to network unreachability). BatchMode=yes causes ssh to exit with
// specific error messages when password auth is the only option.
func isAuthError(err error) bool {
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
			return true
		}
	}
	return false
}

// AuthBootstrapCmd returns the SSH command a user should run to establish a
// ControlMaster connection for a password-protected host. The resulting socket
// is reused by subsequent BatchMode=yes probes.
func (s *ProbeService) AuthBootstrapCmd(host model.Host) string {
	controlPath := s.cfg.SSH.ControlPath
	if controlPath == "" {
		controlPath = "~/.ssh/ocr-%C"
	}
	persist := s.cfg.SSH.ControlPersist
	if persist <= 0 {
		persist = 600
	}
	timeout := s.cfg.SSH.ConnectTimeout
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

// jumpProviderSet returns the set of alias names that serve as jump hosts.
func jumpProviderSet(hosts []model.Host) map[string]bool {
	providers := make(map[string]bool)
	for _, h := range hosts {
		for _, dep := range h.DependsOn {
			providers[dep] = true
		}
	}
	return providers
}

// transportPreflight probes jump providers with a lightweight `ssh <alias> true`
// to check reachability before running full session probes.
func (s *ProbeService) transportPreflight(ctx context.Context, hosts []model.Host, providers map[string]bool) {
	type preflightResult struct {
		idx    int
		status model.TransportStatus
		err    error
	}

	results := make(chan preflightResult)
	count := 0
	for i, h := range hosts {
		if !providers[h.Name] {
			continue
		}
		count++
		go func(idx int, host model.Host) {
			args := s.buildSSHArgs(host, "true")
			_, err := s.runner.Run(ctx, "ssh", args...)
			if err == nil {
				results <- preflightResult{idx: idx, status: model.TransportReady}
				return
			}
			if isAuthError(err) {
				results <- preflightResult{idx: idx, status: model.TransportAuthRequired, err: err}
				return
			}
			results <- preflightResult{idx: idx, status: model.TransportUnreachable, err: err}
		}(i, h)
	}

	for j := 0; j < count; j++ {
		res := <-results
		hosts[res.idx].Transport = res.status
		if res.err != nil {
			hosts[res.idx].TransportError = res.err.Error()
		}
	}
}

// propagateBlocked marks hosts whose jump dependencies are not ready as TransportBlocked.
func propagateBlocked(hosts []model.Host) {
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
		}
	}
}

// MultiHopBootstrapCmds returns ordered ControlMaster bootstrap commands for a
// host and all its unresolved jump dependencies.
func (s *ProbeService) MultiHopBootstrapCmds(host model.Host, allHosts []model.Host) []string {
	aliasIndex := make(map[string]int, len(allHosts))
	for i, h := range allHosts {
		aliasIndex[h.Name] = i
	}

	var cmds []string

	// First, generate commands for each hop that needs auth (in order)
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

	// Then the target host itself if it needs auth
	if host.Status == model.HostStatusAuthRequired || host.Transport == model.TransportAuthRequired {
		cmds = append(cmds, s.AuthBootstrapCmd(host))
	}

	return cmds
}
