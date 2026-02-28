package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"opencoderouter/internal/config"
	"opencoderouter/internal/discovery"
	"opencoderouter/internal/proxy"
	"opencoderouter/internal/registry"
	"opencoderouter/internal/scanner"
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
	hostname := flag.String("hostname", "0.0.0.0", "Hostname/IP to bind the router to")
	flag.Parse()

	cfg.ListenAddr = fmt.Sprintf("%s:%d", *hostname, cfg.ListenPort)

	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "invalid config: %v\n", err)
		os.Exit(1)
	}

	// Logger.
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	logger.Info("starting OpenCode Router",
		"listen", cfg.ListenAddr,
		"username", cfg.Username,
		"scan_range", fmt.Sprintf("%d-%d", cfg.ScanPortStart, cfg.ScanPortEnd),
		"scan_interval", cfg.ScanInterval,
		"mdns", cfg.EnableMDNS,
	)

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
	rt := proxy.New(reg, cfg, logger.With("component", "proxy"))

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
			ticker := time.NewTicker(cfg.ScanInterval + 1*time.Second) // offset from scanner
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
		Handler:      rt,
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

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("server shutdown error", "error", err)
	}

	logger.Info("OpenCode Router stopped")
}
