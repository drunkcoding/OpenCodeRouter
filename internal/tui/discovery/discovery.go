package discovery

import (
	"context"
	"io"
	"log/slog"

	"opencoderouter/internal/model"
	"opencoderouter/internal/remote"
	"opencoderouter/internal/tui/config"
)

type Runner = remote.Runner

type ExecRunner = remote.ExecRunner

type DiscoveryService struct {
	cfg           config.Config
	runner        Runner
	sshConfigPath string
	logger        *slog.Logger
	inner         *remote.DiscoveryService
}

func NewDiscoveryService(cfg config.Config, runner Runner, logger *slog.Logger) *DiscoveryService {
	if runner == nil {
		runner = ExecRunner{}
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	opts := discoveryOptionsFromConfig(cfg)
	inner := remote.NewDiscoveryService(opts, runner, logger)

	return &DiscoveryService{
		cfg:           cfg,
		runner:        runner,
		sshConfigPath: opts.SSHConfigPath,
		logger:        logger,
		inner:         inner,
	}
}

func (s *DiscoveryService) Discover(ctx context.Context) ([]model.Host, error) {
	s.ensureInner()
	s.inner.SetSSHConfigPath(s.sshConfigPath)
	return s.inner.Discover(ctx)
}

func (s *DiscoveryService) ensureInner() {
	if s.inner != nil {
		return
	}
	s.inner = remote.NewDiscoveryService(discoveryOptionsFromConfig(s.cfg), s.runner, s.logger)
}

func parseSSHConfigHosts(content string) []string {
	return remote.ParseSSHConfigHosts(content)
}

func BuildDependencyGraph(hosts []model.Host) {
	remote.BuildDependencyGraph(hosts)
}

func discoveryOptionsFromConfig(cfg config.Config) remote.DiscoveryOptions {
	return remote.DiscoveryOptions{
		Include:       append([]string(nil), cfg.Hosts.Include...),
		Ignore:        append([]string(nil), cfg.Hosts.Ignore...),
		Overrides:     hostOverridesFromConfig(cfg.Hosts.Overrides),
		SSHConfigPath: "",
	}
}

func hostOverridesFromConfig(overrides map[string]config.HostOverride) map[string]remote.HostOverride {
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
