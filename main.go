package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
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

func main() {
	cfg := config.Defaults()

	// CLI flags.
	flag.IntVar(&cfg.ListenPort, "port", cfg.ListenPort, "Port for the router to listen on")
	flag.StringVar(&cfg.Username, "username", cfg.Username, "Username for domain naming (default: OS user)")
	flag.IntVar(&cfg.ScanPortStart, "scan-start", cfg.ScanPortStart, "Start of port scan range")
	flag.IntVar(&cfg.ScanPortEnd, "scan-end", cfg.ScanPortEnd, "End of port scan range")
	flag.DurationVar(&cfg.ScanInterval, "scan-interval", cfg.ScanInterval, "How often to scan for instances")
	flag.IntVar(&cfg.ScanConcurrency, "scan-concurrency", cfg.ScanConcurrency, "Max concurrent port probes")
	flag.DurationVar(&cfg.ProbeTimeout, "probe-timeout", cfg.ProbeTimeout, "Timeout for each port probe")
	flag.DurationVar(&cfg.StaleAfter, "stale-after", cfg.StaleAfter, "Remove backends unseen for this duration")
	flag.BoolVar(&cfg.EnableMDNS, "mdns", cfg.EnableMDNS, "Enable mDNS service advertisement")
	cleanupOrphans := flag.Bool("cleanup-orphans", false, "Cleanup likely orphan opencode serve processes in scan range on startup")
	hostname := flag.String("hostname", "0.0.0.0", "Hostname/IP to bind the router to")
	flag.Parse()

	// Positional args are project paths to launch opencode serve in.
	projectPaths := flag.Args()

	cfg.ListenAddr = fmt.Sprintf("%s:%d", *hostname, cfg.ListenPort)

	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "invalid config: %v\n", err)
		os.Exit(1)
	}

	// Logger (file-backed to avoid BubbleTea alt-screen swallowing stdout/stderr logs).
	var (
		logFile *os.File
		logPath string
	)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		logDir := filepath.Join(home, ".local", "share", "opencoderouter")
		if err := os.MkdirAll(logDir, 0o755); err == nil {
			path := filepath.Join(logDir, "debug.log")
			if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
				logFile = f
				logPath = path
			}
		}
	}
	if logFile == nil {
		const fallback = "/tmp/ocr-debug.log"
		if f, err := os.OpenFile(fallback, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			logFile = f
			logPath = fallback
		}
	}
	var logWriter io.Writer = io.Discard
	if logFile != nil {
		logWriter = logFile
		defer logFile.Close()
	} else {
		logPath = "disabled (io.Discard)"
	}
	logger := slog.New(slog.NewTextHandler(logWriter, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(logger)
	fmt.Fprintf(os.Stderr, "Logs: %s\n", logPath)

	logger.Info("OpenCodeRouter starting",
		"log_file", logPath,
		"listen", cfg.ListenAddr,
		"username", cfg.Username,
		"scan_range", fmt.Sprintf("%d-%d", cfg.ScanPortStart, cfg.ScanPortEnd),
		"scan_interval", cfg.ScanInterval,
		"mdns", cfg.EnableMDNS,
	)

	orphanCleanupEnabled := *cleanupOrphans || envEnabled("OCR_CLEANUP_ORPHANS")
	handleStartupOrphanOffer(cfg.ScanPortStart, cfg.ScanPortEnd, orphanCleanupEnabled, logger.With("component", "startup-cleanup"))

	// Launch opencode serve instances for any project paths given as args.
	var lnch *launcher.Launcher
	if len(projectPaths) > 0 {
		lnch = launcher.New(cfg.ScanPortStart, cfg.ScanPortEnd, logger.With("component", "launcher"))
		if err := lnch.Launch(projectPaths); err != nil {
			logger.Error("launcher error", "error", err)
			os.Exit(1)
		}
	}

	// Components.
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
		logger.Error("failed to initialize scrollback cache", "error", err)
		os.Exit(1)
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
		PortStart:           cfg.ScanPortStart + 100, // separate range
		PortEnd:             cfg.ScanPortEnd + 100,
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

	// Context for graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start scanner in background.
	go sc.Run(ctx)

	// Start mDNS sync loop in background.
	if adv != nil {
		go func() {
			// Initial sync: wait briefly for the first scan to discover backends,
			// then advertise immediately instead of waiting for the full ticker interval.
			select {
			case <-time.After(3 * time.Second):
				adv.Sync(reg.All())
			case <-ctx.Done():
				return
			}

			ticker := time.NewTicker(cfg.ScanInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					adv.Sync(reg.All())
				}
			}
		}()
	}

	// HTTP server.
	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      apiRouter,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second, // long for SSE streaming
		IdleTimeout:  120 * time.Second,
	}

	// Start server in background.
	go func() {
		logger.Info("HTTP server listening", "addr", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server error", "error", err)
			os.Exit(1)
		}
	}()

	// Print access info.
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

	// Wait for shutdown signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	logger.Info("received signal, shutting down", "signal", sig)

	// Graceful shutdown.
	cancel() // stop scanner + mDNS sync

	if adv != nil {
		adv.Shutdown()
	}

	// Stop managed opencode serve instances.
	if lnch != nil {
		lnch.Shutdown()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("server shutdown error", "error", err)
	}

	logger.Info("OpenCode Router stopped")
}

type orphanProcess struct {
	Port    int
	PID     int
	Command string
}

func handleStartupOrphanOffer(scanStart, scanEnd int, cleanup bool, logger *slog.Logger) {
	orphans, err := detectLikelyOrphanOpenCodeServes(scanStart, scanEnd)
	if err != nil {
		if logger != nil {
			logger.Debug("orphan detection unavailable", "error", err)
		}
		return
	}
	if len(orphans) == 0 {
		return
	}

	if logger != nil {
		logger.Warn(
			"detected likely orphan opencode serve processes in configured scan range",
			"count", len(orphans),
			"scan_range", fmt.Sprintf("%d-%d", scanStart, scanEnd),
			"cleanup_hint", "rerun with --cleanup-orphans or OCR_CLEANUP_ORPHANS=1",
		)
		for _, orphan := range orphans {
			logger.Warn("orphan candidate", "port", orphan.Port, "pid", orphan.PID, "command", orphan.Command)
		}
	}

	if !cleanup {
		return
	}

	if logger != nil {
		logger.Warn("startup orphan cleanup enabled; sending SIGTERM", "count", len(orphans))
	}
	cleanupLikelyOrphans(orphans, logger)
}

func detectLikelyOrphanOpenCodeServes(scanStart, scanEnd int) ([]orphanProcess, error) {
	if scanEnd < scanStart {
		return nil, nil
	}

	cmd := exec.Command("lsof", "-nP", fmt.Sprintf("-iTCP:%d-%d", scanStart, scanEnd), "-sTCP:LISTEN", "-Fpcn")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseLikelyOrphansFromLsofOutput(string(output), scanStart, scanEnd), nil
}

func parseLikelyOrphansFromLsofOutput(raw string, scanStart, scanEnd int) []orphanProcess {
	if scanEnd < scanStart {
		return nil
	}

	lines := strings.Split(raw, "\n")
	var (
		currentPID int
		currentCmd string
	)

	seen := make(map[string]struct{})
	orphans := make([]orphanProcess, 0)

	for _, lineRaw := range lines {
		line := strings.TrimSpace(lineRaw)
		if line == "" {
			continue
		}

		tag := line[0]
		value := strings.TrimSpace(line[1:])

		switch tag {
		case 'p':
			pid, convErr := strconv.Atoi(value)
			if convErr != nil {
				currentPID = 0
				continue
			}
			currentPID = pid
		case 'c':
			currentCmd = value
		case 'n':
			if currentPID == 0 {
				continue
			}
			if !strings.Contains(strings.ToLower(currentCmd), "opencode") {
				continue
			}
			port, ok := extractListenPort(value)
			if !ok || port < scanStart || port > scanEnd {
				continue
			}
			key := fmt.Sprintf("%d:%d", currentPID, port)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			orphans = append(orphans, orphanProcess{Port: port, PID: currentPID, Command: currentCmd})
		}
	}

	return orphans
}

func cleanupLikelyOrphans(orphans []orphanProcess, logger *slog.Logger) {
	seen := make(map[int]struct{})
	for _, orphan := range orphans {
		if _, ok := seen[orphan.PID]; ok {
			continue
		}
		seen[orphan.PID] = struct{}{}

		err := syscall.Kill(orphan.PID, syscall.SIGTERM)
		if err == nil {
			if logger != nil {
				logger.Info("sent SIGTERM to likely orphan process", "pid", orphan.PID, "command", orphan.Command)
			}
			continue
		}
		if errors.Is(err, syscall.ESRCH) {
			if logger != nil {
				logger.Debug("orphan process already exited", "pid", orphan.PID)
			}
			continue
		}
		if logger != nil {
			logger.Warn("failed to terminate likely orphan process", "pid", orphan.PID, "error", err)
		}
	}
}

func extractListenPort(addr string) (int, bool) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return 0, false
	}
	if idx := strings.Index(addr, "->"); idx > 0 {
		addr = strings.TrimSpace(addr[:idx])
	}
	if idx := strings.Index(addr, "("); idx > 0 {
		addr = strings.TrimSpace(addr[:idx])
	}
	idx := strings.LastIndex(addr, ":")
	if idx < 0 || idx+1 >= len(addr) {
		return 0, false
	}
	port, err := strconv.Atoi(strings.TrimSpace(addr[idx+1:]))
	if err != nil {
		return 0, false
	}
	return port, true
}

func envEnabled(name string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
