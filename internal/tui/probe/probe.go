package probe

import (
	"context"
	"io"
	"log/slog"
	"time"

	"opencoderouter/internal/model"
	"opencoderouter/internal/remote"
	"opencoderouter/internal/tui/config"
)

type Runner = remote.Runner

type ExecRunner = remote.ExecRunner

type ProbeService struct {
	cfg    config.Config
	runner Runner
	cache  *CacheStore
	nowFn  func() time.Time
	logger *slog.Logger
	inner  *remote.ProbeService
}

func NewProbeService(cfg config.Config, runner Runner, cache *CacheStore, logger *slog.Logger) *ProbeService {
	if runner == nil {
		runner = ExecRunner{}
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	inner := remote.NewProbeService(probeOptionsFromConfig(cfg), runner, cache, logger)

	return &ProbeService{
		cfg:    cfg,
		runner: runner,
		cache:  cache,
		nowFn:  time.Now,
		logger: logger,
		inner:  inner,
	}
}

func (s *ProbeService) ProbeHosts(ctx context.Context, hosts []model.Host) ([]model.Host, error) {
	s.ensureInner()
	s.inner.SetNowFunc(s.nowFn)
	return s.inner.ProbeHosts(ctx, hosts)
}

func (s *ProbeService) AuthBootstrapCmd(host model.Host) string {
	s.ensureInner()
	return s.inner.AuthBootstrapCmd(host)
}

func (s *ProbeService) MultiHopBootstrapCmds(host model.Host, allHosts []model.Host) []string {
	s.ensureInner()
	return s.inner.MultiHopBootstrapCmds(host, allHosts)
}

func (s *ProbeService) ensureInner() {
	if s.inner != nil {
		return
	}
	s.inner = remote.NewProbeService(probeOptionsFromConfig(s.cfg), s.runner, s.cache, s.logger)
	if s.nowFn != nil {
		s.inner.SetNowFunc(s.nowFn)
	}
}

func probeOptionsFromConfig(cfg config.Config) remote.ProbeOptions {
	return remote.ProbeOptions{
		MaxParallel:      cfg.Polling.MaxParallel,
		SessionScanPaths: append([]string(nil), cfg.Sessions.ScanPaths...),
		Overrides:        hostOverridesFromConfig(cfg.Hosts.Overrides),
		SSH: remote.SSHOptions{
			ControlMaster:  cfg.SSH.ControlMaster,
			ControlPersist: cfg.SSH.ControlPersist,
			ControlPath:    cfg.SSH.ControlPath,
			BatchMode:      cfg.SSH.BatchMode,
			ConnectTimeout: cfg.SSH.ConnectTimeout,
		},
		SortBy:          cfg.Sessions.SortBy,
		ShowArchived:    cfg.Sessions.ShowArchived,
		MaxDisplay:      cfg.Sessions.MaxDisplay,
		ActiveThreshold: cfg.Display.ActiveThreshold,
		IdleThreshold:   cfg.Display.IdleThreshold,
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
