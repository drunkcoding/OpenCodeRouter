package discovery

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path"
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

// Run executes a command, propagating stderr when available.
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

// DiscoveryService finds SSH hosts and resolves host metadata via `ssh -G`.
type DiscoveryService struct {
	cfg           config.Config
	runner        Runner
	sshConfigPath string
	logger        *slog.Logger
}

const maxSanitizedLogErrorRunes = 320

// NewDiscoveryService builds a discovery service for SSH host inventory.
func NewDiscoveryService(cfg config.Config, runner Runner, logger *slog.Logger) *DiscoveryService {
	if runner == nil {
		runner = ExecRunner{}
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &DiscoveryService{
		cfg:           cfg,
		runner:        runner,
		sshConfigPath: defaultSSHConfigPath(),
		logger:        logger,
	}
}

// Discover returns filtered hosts, with address/user resolved from ssh config.
func (s *DiscoveryService) Discover(ctx context.Context) ([]model.Host, error) {
	startedAt := time.Now()
	s.logger.Debug("starting host discovery",
		"ssh_config_path", s.sshConfigPath,
		"include_patterns_count", len(s.cfg.Hosts.Include),
		"ignore_patterns_count", len(s.cfg.Hosts.Ignore),
	)

	aliases, err := s.loadHostAliases()
	if err != nil {
		s.logger.Error("host discovery failed",
			"stage", "load_host_aliases",
			"error", sanitizeLogError(err),
		)
		return nil, err
	}
	s.logger.Debug("loaded host aliases", "alias_count", len(aliases))

	filtered := filterAliasesWithLogger(aliases, s.cfg.Hosts.Include, s.cfg.Hosts.Ignore, s.logger)
	s.logger.Debug("discovery aliases after filtering", "filtered_count", len(filtered))

	hosts := make([]model.Host, 0, len(filtered))
	var probeErrs []error

	for _, alias := range filtered {
		select {
		case <-ctx.Done():
			err := fmt.Errorf("discover canceled: %w", ctx.Err())
			s.logger.Error("host discovery failed",
				"stage", "context_canceled",
				"processed_hosts", len(hosts),
				"error", sanitizeLogError(err),
			)
			return hosts, err
		default:
		}

		h, resolveErr := s.resolveHost(ctx, alias)
		if resolveErr != nil {
			h = model.Host{
				Name:      alias,
				Label:     alias,
				Status:    model.HostStatusError,
				LastError: resolveErr.Error(),
			}
			probeErrs = append(probeErrs, fmt.Errorf("resolve host %q: %w", alias, resolveErr))
		}

		if override, ok := s.cfg.Hosts.Overrides[alias]; ok {
			if override.Label != "" {
				h.Label = override.Label
			}
			h.Priority = override.Priority
			if override.OpencodePath != "" {
				h.OpencodeBin = override.OpencodePath
			}
		}
		if h.Label == "" {
			h.Label = h.Name
		}

		hosts = append(hosts, h)
	}

	sort.Slice(hosts, func(i, j int) bool {
		if hosts[i].Priority != hosts[j].Priority {
			return hosts[i].Priority > hosts[j].Priority
		}
		return hosts[i].Name < hosts[j].Name
	})

	buildDependencyGraphWithLogger(hosts, s.logger)

	if len(probeErrs) > 0 {
		joinedErr := errors.Join(probeErrs...)
		s.logger.Error("host discovery failed",
			"stage", "resolve_hosts",
			"host_count", len(hosts),
			"failure_count", len(probeErrs),
			"duration", time.Since(startedAt),
			"error", sanitizeLogError(joinedErr),
		)
		return hosts, joinedErr
	}

	s.logger.Debug("host discovery complete",
		"host_count", len(hosts),
		"duration", time.Since(startedAt),
	)

	return hosts, nil
}

// loadHostAliases reads ~/.ssh/config and extracts concrete Host aliases.
func (s *DiscoveryService) loadHostAliases() ([]string, error) {
	s.logger.Debug("reading ssh config for host aliases", "path", s.sshConfigPath)

	b, err := os.ReadFile(s.sshConfigPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.logger.Debug("ssh config file not found", "path", s.sshConfigPath, "alias_count", 0)
			return nil, nil
		}
		s.logger.Error("failed to read ssh config", "path", s.sshConfigPath, "error", sanitizeLogError(err))
		return nil, fmt.Errorf("read ssh config %q: %w", s.sshConfigPath, err)
	}

	// TODO: support Include directives and multi-file merge semantics from OpenSSH.
	aliases := parseSSHConfigHostsWithLogger(string(b), s.logger)
	s.logger.Debug("loaded host aliases from ssh config", "path", s.sshConfigPath, "alias_count", len(aliases))
	return aliases, nil
}

// resolveHost runs `ssh -G <alias>` and extracts hostname/user values.
func (s *DiscoveryService) resolveHost(ctx context.Context, alias string) (model.Host, error) {
	s.logger.Debug("resolving host", "alias", alias)
	s.logger.Debug("executing ssh -G", "alias", alias)

	out, err := s.runner.Run(ctx, "ssh", "-G", alias)
	if err != nil {
		s.logger.Error("failed to resolve host",
			"alias", alias,
			"error", sanitizeLogError(err),
		)
		return model.Host{}, err
	}
	s.logger.Debug("ssh -G completed", "alias", alias, "output_bytes", len(out))

	host := model.Host{
		Name:    alias,
		Address: alias,
		User:    currentUserName(),
		Label:   alias,
		Status:  model.HostStatusUnknown,
	}

	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		key := strings.ToLower(parts[0])
		value := strings.Join(parts[1:], " ")
		switch key {
		case "hostname":
			host.Address = value
		case "user":
			host.User = value
		case "proxyjump":
			if value != "" && value != "none" {
				host.ProxyJumpRaw = value
				host.ProxyKind = model.ProxyKindJump
				host.JumpChain = parseProxyJumpWithLogger(value, alias, s.logger)
			}
		case "proxycommand":
			if value != "" && value != "none" {
				host.ProxyCommand = value
				if host.ProxyKind == "" || host.ProxyKind == model.ProxyKindNone {
					host.ProxyKind = model.ProxyKindCommand
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		wrappedErr := fmt.Errorf("parse ssh -G output for %q: %w", alias, err)
		s.logger.Error("failed to parse ssh -G output",
			"alias", alias,
			"error", sanitizeLogError(wrappedErr),
		)
		return model.Host{}, wrappedErr
	}

	s.logger.Debug("resolved host metadata",
		"alias", alias,
		"proxy_kind", host.ProxyKind,
		"jump_hop_count", len(host.JumpChain),
		"has_proxy_command", host.ProxyCommand != "",
	)

	return host, nil
}

// parseSSHConfigHosts extracts non-wildcard `Host` aliases from config text.
func parseSSHConfigHosts(content string) []string {
	return parseSSHConfigHostsWithLogger(content, nil)
}

func parseSSHConfigHostsWithLogger(content string, logger *slog.Logger) []string {
	if logger != nil {
		logger.Debug("starting ssh config host parse", "content_bytes", len(content))
	}

	seen := make(map[string]struct{})
	aliases := make([]string, 0)

	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "host") {
			continue
		}

		for _, candidate := range fields[1:] {
			if strings.HasPrefix(candidate, "!") {
				continue
			}
			if strings.ContainsAny(candidate, "*?") {
				continue
			}
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			aliases = append(aliases, candidate)
		}
	}

	if logger != nil {
		logger.Debug("completed ssh config host parse", "alias_count", len(aliases))
	}

	return aliases
}

// filterAliases applies include/ignore glob lists.
func filterAliases(aliases, includes, ignores []string) []string {
	return filterAliasesWithLogger(aliases, includes, ignores, nil)
}

func filterAliasesWithLogger(aliases, includes, ignores []string, logger *slog.Logger) []string {
	if logger != nil {
		logger.Debug("filtering host aliases",
			"before_count", len(aliases),
			"include_patterns_count", len(includes),
			"ignore_patterns_count", len(ignores),
		)
	}

	if len(includes) == 0 {
		includes = []string{"*"}
	}

	filtered := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		if !matchesAnyGlob(alias, includes) {
			continue
		}
		if matchesAnyGlob(alias, ignores) {
			continue
		}
		filtered = append(filtered, alias)
	}

	if logger != nil {
		logger.Debug("host alias filtering complete",
			"before_count", len(aliases),
			"after_count", len(filtered),
		)
	}

	return filtered
}

// matchesAnyGlob returns true if candidate matches at least one pattern.
func matchesAnyGlob(candidate string, patterns []string) bool {
	for _, pattern := range patterns {
		matched, err := path.Match(pattern, candidate)
		if err != nil {
			if pattern == candidate {
				return true
			}
			continue
		}
		if matched {
			return true
		}
	}
	return false
}

// defaultSSHConfigPath resolves ~/.ssh/config.
func defaultSSHConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".ssh/config"
	}
	return filepath.Join(home, ".ssh", "config")
}

// currentUserName returns current username when available.
func currentUserName() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return u.Username
}

// parseProxyJump splits a comma-separated ProxyJump value into JumpHop structs.
// Each hop can be: alias, user@host, host:port, user@host:port, or ssh://user@host:port.
func parseProxyJump(raw string) []model.JumpHop {
	return parseProxyJumpWithLogger(raw, "", nil)
}

func parseProxyJumpWithLogger(raw, alias string, logger *slog.Logger) []model.JumpHop {
	parts := strings.Split(raw, ",")
	hops := make([]model.JumpHop, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		hop := parseOneHop(part)
		hops = append(hops, hop)
	}

	if logger != nil {
		if alias != "" {
			logger.Debug("parsed proxy jump chain",
				"alias", alias,
				"hop_count", len(hops),
			)
		} else {
			logger.Debug("parsed proxy jump chain", "hop_count", len(hops))
		}
	}

	return hops
}

// parseOneHop parses a single ProxyJump hop string into a JumpHop.
func parseOneHop(hop string) model.JumpHop {
	j := model.JumpHop{Raw: hop}

	// Handle ssh:// URI scheme
	if strings.HasPrefix(hop, "ssh://") {
		u, err := url.Parse(hop)
		if err == nil {
			j.Host = u.Hostname()
			j.User = u.User.Username()
			if p := u.Port(); p != "" {
				j.Port, _ = strconv.Atoi(p)
			}
			return j
		}
	}

	// Handle user@host:port, user@host, host:port, or bare alias
	userHost := hop
	if at := strings.LastIndex(hop, "@"); at >= 0 {
		j.User = hop[:at]
		userHost = hop[at+1:]
	}

	host, portStr, err := net.SplitHostPort(userHost)
	if err == nil {
		j.Host = host
		j.Port, _ = strconv.Atoi(portStr)
	} else {
		j.Host = userHost
	}

	return j
}

// BuildDependencyGraph populates DependsOn/Dependents/AliasRef fields across hosts.
// It maps JumpChain hops to known aliases and builds the reverse index.
func BuildDependencyGraph(hosts []model.Host) {
	buildDependencyGraphWithLogger(hosts, nil)
}

func buildDependencyGraphWithLogger(hosts []model.Host, logger *slog.Logger) {
	startedAt := time.Now()
	if logger != nil {
		logger.Debug("building dependency graph", "host_count", len(hosts))
	}

	// Build alias lookup
	aliasIndex := make(map[string]int, len(hosts))
	addressIndex := make(map[string]int, len(hosts))
	for i, h := range hosts {
		aliasIndex[h.Name] = i
		if h.Address != "" {
			addressIndex[h.Address] = i
		}
	}

	// Resolve hops and build edges
	for i := range hosts {
		if hosts[i].ProxyKind != model.ProxyKindJump || len(hosts[i].JumpChain) == 0 {
			continue
		}

		seen := make(map[string]bool)
		for hi := range hosts[i].JumpChain {
			hop := &hosts[i].JumpChain[hi]
			alias := resolveHopAlias(hop.Host, aliasIndex, addressIndex)
			if alias == "" {
				hop.External = true
				continue
			}
			hop.AliasRef = alias
			if !seen[alias] {
				seen[alias] = true
				hosts[i].DependsOn = append(hosts[i].DependsOn, alias)
			}
		}
	}

	edgeCount := 0
	for i := range hosts {
		edgeCount += len(hosts[i].DependsOn)
	}
	if logger != nil {
		logger.Debug("dependency graph edges resolved", "edge_count", edgeCount)
	}

	// Build reverse index (Dependents)
	for i := range hosts {
		for _, dep := range hosts[i].DependsOn {
			if idx, ok := aliasIndex[dep]; ok {
				hosts[idx].Dependents = appendUnique(hosts[idx].Dependents, hosts[i].Name)
			}
		}
	}

	if logger != nil {
		logger.Debug("dependency graph build complete",
			"host_count", len(hosts),
			"edge_count", edgeCount,
			"duration", time.Since(startedAt),
		)
	}
}

// resolveHopAlias tries to match a hop host to a known SSH alias.
func resolveHopAlias(hopHost string, aliasIndex, addressIndex map[string]int) string {
	// Direct alias match
	if _, ok := aliasIndex[hopHost]; ok {
		return hopHost
	}
	// Address match (hostname resolved)
	if idx, ok := addressIndex[hopHost]; ok {
		for alias, i := range aliasIndex {
			if i == idx {
				return alias
			}
		}
	}
	return ""
}

// appendUnique appends s to slice only if not already present.
func appendUnique(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}

func sanitizeLogError(err error) string {
	if err == nil {
		return ""
	}

	msg := strings.TrimSpace(err.Error())
	msg = strings.NewReplacer("\r", " ", "\n", " ").Replace(msg)
	msg = strings.Join(strings.Fields(msg), " ")

	lower := strings.ToLower(msg)
	if idx := strings.Index(lower, "stderr:"); idx >= 0 {
		msg = strings.TrimSpace(msg[:idx]) + " stderr: [redacted]"
	}
	if idx := strings.Index(strings.ToLower(msg), "stdout:"); idx >= 0 {
		msg = strings.TrimSpace(msg[:idx]) + " stdout: [redacted]"
	}

	runes := []rune(msg)
	if len(runes) > maxSanitizedLogErrorRunes {
		msg = strings.TrimSpace(string(runes[:maxSanitizedLogErrorRunes-1])) + "…"
	}

	return msg
}
