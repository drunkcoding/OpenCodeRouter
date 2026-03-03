package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
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

// ProbeHosts probes hosts in parallel and returns updated host structures.
func (s *ProbeService) ProbeHosts(ctx context.Context, hosts []model.Host) ([]model.Host, error) {
	if len(hosts) == 0 {
		return nil, nil
	}

	if s.cache != nil {
		s.cache.PurgeExpired()
	}

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

// probeHost executes one SSH command and parses returned session JSON.
func (s *ProbeService) probeHost(ctx context.Context, host model.Host) (model.Host, error) {
	remoteCmd := "command -v opencode && opencode session list --format json"
	if host.OpencodeBin != "" {
		remoteCmd = fmt.Sprintf("command -v %s && %s session list --format json", host.OpencodeBin, host.OpencodeBin)
	}

	args := s.buildSSHArgs(host, remoteCmd)
	out, err := s.runner.Run(ctx, "ssh", args...)
	if err != nil {
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

	// TODO: enrich sessions with metadata from local DB when enabled.
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
}

type remoteEnvelope struct {
	Sessions []remoteSession `json:"sessions"`
}

// parseSessions decodes opencode JSON output into domain sessions.
func (s *ProbeService) parseSessions(raw []byte) ([]model.Session, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, nil
	}

	list := make([]remoteSession, 0)
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &list); err != nil {
			return nil, err
		}
	} else {
		var env remoteEnvelope
		if err := json.Unmarshal([]byte(trimmed), &env); err != nil {
			return nil, err
		}
		list = env.Sessions
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
		lastActivity := parseTimestamp(rs.LastActivity)
		sessions = append(sessions, model.Session{
			ID:           rs.ID,
			Project:      rs.Project,
			Title:        rs.Title,
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
