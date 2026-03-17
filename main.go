package main

import (
	"fmt"
	"os"
)

func main() {
	cfg, projectPaths, cleanupOrphansFlag, err := parseCLIConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid config: %v\n", err)
		os.Exit(1)
	}

	logger, logPath, closeLogger := setupLogger()
	defer closeLogger()

	fmt.Fprintf(os.Stderr, "Logs: %s\n", logPath)
	logger.Info("OpenCodeRouter starting",
		"log_file", logPath,
		"listen", cfg.ListenAddr,
		"username", cfg.Username,
		"scan_range", fmt.Sprintf("%d-%d", cfg.ScanPortStart, cfg.ScanPortEnd),
		"session_range", fmt.Sprintf("%d-%d", cfg.SessionPortStart, cfg.SessionPortEnd),
		"scan_interval", cfg.ScanInterval,
		"mdns", cfg.EnableMDNS,
	)

	orphanCleanupEnabled := cleanupOrphansFlag || envEnabled("OCR_CLEANUP_ORPHANS")
	handleStartupOrphanOffer(cfg.ScanPortStart, cfg.ScanPortEnd, orphanCleanupEnabled, logger.With("component", "startup-cleanup"))

	if err := runRouter(cfg, projectPaths, logger); err != nil {
		logger.Error("OpenCode Router fatal error", "error", err)
		os.Exit(1)
	}

	logger.Info("OpenCode Router stopped")
}
