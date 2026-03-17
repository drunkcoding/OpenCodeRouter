package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"opencoderouter/internal/api"
	"opencoderouter/internal/auth"
	"opencoderouter/internal/cache"
	"opencoderouter/internal/config"
	"opencoderouter/internal/discovery"
	"opencoderouter/internal/launcher"
	"opencoderouter/internal/proxy"
	"opencoderouter/internal/registry"
	"opencoderouter/internal/scanner"
	"opencoderouter/internal/session"
	"opencoderouter/internal/terminal"
)

func runRouter(cfg config.Config, projectPaths []string, logger *slog.Logger) error {
	var lnch *launcher.Launcher
	if len(projectPaths) > 0 {
		lnch = launcher.New(cfg.ScanPortStart, cfg.ScanPortEnd, logger.With("component", "launcher"))
		if err := lnch.Launch(projectPaths); err != nil {
			return fmt.Errorf("launcher error: %w", err)
		}
	}

	reg := registry.New(cfg.StaleAfter, logger.With("component", "registry"))
	sc := scanner.New(
		reg,
		cfg.ScanPortStart,
		cfg.ScanPortEnd,
		cfg.ScanInterval,
		cfg.ScanConcurrency,
		cfg.ProbeTimeout,
		logger.With("component", "scanner"),
	)
	uiHandler := http.FileServer(getWebFS())
	rt := proxy.New(reg, cfg, logger.With("component", "proxy"), uiHandler)

	eventBus := session.NewEventBus(100)
	scrollbackCache, err := cache.NewJSONLCache(cache.CacheConfig{})
	if err != nil {
		return fmt.Errorf("failed to initialize scrollback cache: %w", err)
	}
	defer func() {
		if closeErr := scrollbackCache.Close(); closeErr != nil {
			logger.Warn("failed to close scrollback cache", "error", closeErr)
		}
	}()

	sessionMgr := session.NewManager(session.ManagerConfig{
		Registry:            reg,
		EventBus:            eventBus,
		Logger:              logger.With("component", "session"),
		PortStart:           cfg.SessionPortStart,
		PortEnd:             cfg.SessionPortEnd,
		HealthCheckInterval: 10 * time.Second,
		HealthCheckTimeout:  2 * time.Second,
		StopTimeout:         5 * time.Second,
		EventBuffer:         100,
		TerminalDialer: terminal.NewSessionDialer(terminal.SessionDialerConfig{
			Logger: logger.With("component", "terminal-dialer"),
		}),
	})

	apiRouter := api.NewRouter(api.RouterConfig{
		SessionManager:  sessionMgr,
		SessionEventBus: eventBus,
		AuthConfig:      auth.LoadFromEnv(),
		ScrollbackCache: scrollbackCache,
		Fallback:        rt,
	})

	var adv *discovery.Advertiser
	if cfg.EnableMDNS {
		adv = discovery.New(cfg, logger.With("component", "mdns"))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go sc.Run(ctx)
	if adv != nil {
		go runMDNSSyncLoop(ctx, adv, reg, cfg.ScanInterval)
	}

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      apiRouter,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	serverErrCh := make(chan error, 1)
	go func() {
		logger.Info("HTTP server listening", "addr", cfg.ListenAddr)
		if serveErr := srv.ListenAndServe(); serveErr != nil && serveErr != http.ErrServerClosed {
			serverErrCh <- serveErr
		}
	}()

	printAccessInfo(cfg, projectPaths)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	var serverErr error
	select {
	case sig := <-sigCh:
		logger.Info("received signal, shutting down", "signal", sig)
	case serverErr = <-serverErrCh:
		logger.Error("HTTP server error", "error", serverErr)
	}

	cancel()
	if adv != nil {
		adv.Shutdown()
	}
	if lnch != nil {
		lnch.Shutdown()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("server shutdown error", "error", err)
	}

	if serverErr != nil {
		return serverErr
	}

	return nil
}

func runMDNSSyncLoop(ctx context.Context, adv *discovery.Advertiser, reg *registry.Registry, interval time.Duration) {
	select {
	case <-time.After(3 * time.Second):
		adv.Sync(reg.All())
	case <-ctx.Done():
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			adv.Sync(reg.All())
		}
	}
}

func printAccessInfo(cfg config.Config, projectPaths []string) {
	outboundIP := config.GetOutboundIP()
	fmt.Println()
	fmt.Printf("  Dashboard:     http://localhost:%d\n", cfg.ListenPort)
	fmt.Printf("  Network:       http://%s:%d\n", outboundIP, cfg.ListenPort)
	fmt.Printf("  API:           http://localhost:%d/api/backends\n", cfg.ListenPort)
	fmt.Printf("  Username:      %s\n", cfg.Username)
	fmt.Printf("  Domain format: {project}-%s.local:%d\n", cfg.Username, cfg.ListenPort)
	fmt.Printf("  Path format:   http://localhost:%d/{project}/...\n", cfg.ListenPort)
	if cfg.EnableMDNS {
		fmt.Printf("  mDNS:          enabled (type: %s)\n", cfg.MDNSServiceType)
	}
	if len(projectPaths) > 0 {
		fmt.Printf("  Projects:      %d managed\n", len(projectPaths))
	}
	fmt.Println()
}
