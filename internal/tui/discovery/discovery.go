package discovery

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	"sort"
	"strings"

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
}

// NewDiscoveryService builds a discovery service for SSH host inventory.
func NewDiscoveryService(cfg config.Config, runner Runner) *DiscoveryService {
	if runner == nil {
		runner = ExecRunner{}
	}
	return &DiscoveryService{
		cfg:           cfg,
		runner:        runner,
		sshConfigPath: defaultSSHConfigPath(),
	}
}

// Discover returns filtered hosts, with address/user resolved from ssh config.
func (s *DiscoveryService) Discover(ctx context.Context) ([]model.Host, error) {
	aliases, err := s.loadHostAliases()
	if err != nil {
		return nil, err
	}

	filtered := filterAliases(aliases, s.cfg.Hosts.Include, s.cfg.Hosts.Ignore)
	hosts := make([]model.Host, 0, len(filtered))
	var probeErrs []error

	for _, alias := range filtered {
		select {
		case <-ctx.Done():
			return hosts, fmt.Errorf("discover canceled: %w", ctx.Err())
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

	if len(probeErrs) > 0 {
		return hosts, errors.Join(probeErrs...)
	}
	return hosts, nil
}

// loadHostAliases reads ~/.ssh/config and extracts concrete Host aliases.
func (s *DiscoveryService) loadHostAliases() ([]string, error) {
	b, err := os.ReadFile(s.sshConfigPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read ssh config %q: %w", s.sshConfigPath, err)
	}

	// TODO: support Include directives and multi-file merge semantics from OpenSSH.
	return parseSSHConfigHosts(string(b)), nil
}

// resolveHost runs `ssh -G <alias>` and extracts hostname/user values.
func (s *DiscoveryService) resolveHost(ctx context.Context, alias string) (model.Host, error) {
	out, err := s.runner.Run(ctx, "ssh", "-G", alias)
	if err != nil {
		return model.Host{}, err
	}

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
		}
	}

	if err := scanner.Err(); err != nil {
		return model.Host{}, fmt.Errorf("parse ssh -G output for %q: %w", alias, err)
	}

	return host, nil
}

// parseSSHConfigHosts extracts non-wildcard `Host` aliases from config text.
func parseSSHConfigHosts(content string) []string {
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

	return aliases
}

// filterAliases applies include/ignore glob lists.
func filterAliases(aliases, includes, ignores []string) []string {
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
