package main

import (
	"flag"
	"fmt"

	"opencoderouter/internal/config"
)

func parseCLIConfig() (config.Config, []string, bool, error) {
	cfg := config.Defaults()

	flag.IntVar(&cfg.ListenPort, "port", cfg.ListenPort, "Port for the router to listen on")
	flag.StringVar(&cfg.Username, "username", cfg.Username, "Username for domain naming (default: OS user)")
	flag.IntVar(&cfg.ScanPortStart, "scan-start", cfg.ScanPortStart, "Start of port scan range")
	flag.IntVar(&cfg.ScanPortEnd, "scan-end", cfg.ScanPortEnd, "End of port scan range")
	flag.IntVar(&cfg.SessionPortStart, "session-port-start", cfg.SessionPortStart, "Start of port range for managed OpenCode session daemons")
	flag.IntVar(&cfg.SessionPortEnd, "session-port-end", cfg.SessionPortEnd, "End of port range for managed OpenCode session daemons")
	flag.DurationVar(&cfg.ScanInterval, "scan-interval", cfg.ScanInterval, "How often to scan for instances")
	flag.IntVar(&cfg.ScanConcurrency, "scan-concurrency", cfg.ScanConcurrency, "Max concurrent port probes")
	flag.DurationVar(&cfg.ProbeTimeout, "probe-timeout", cfg.ProbeTimeout, "Timeout for each port probe")
	flag.DurationVar(&cfg.StaleAfter, "stale-after", cfg.StaleAfter, "Remove backends unseen for this duration")
	flag.BoolVar(&cfg.EnableMDNS, "mdns", cfg.EnableMDNS, "Enable mDNS service advertisement")

	cleanupOrphans := flag.Bool("cleanup-orphans", false, "Cleanup likely orphan opencode serve processes in scan range on startup")
	hostname := flag.String("hostname", "0.0.0.0", "Hostname/IP to bind the router to")

	flag.Parse()
	projectPaths := flag.Args()

	cfg.ListenAddr = fmt.Sprintf("%s:%d", *hostname, cfg.ListenPort)
	defaultSessionStartOffset := cfg.SessionPortStart - cfg.ScanPortStart
	defaultSessionEndOffset := cfg.SessionPortEnd - cfg.ScanPortEnd

	sessionStartFlagSet, sessionEndFlagSet := false, false
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "session-port-start":
			sessionStartFlagSet = true
		case "session-port-end":
			sessionEndFlagSet = true
		}
	})

	if !sessionStartFlagSet {
		cfg.SessionPortStart = cfg.ScanPortStart + defaultSessionStartOffset
	}
	if !sessionEndFlagSet {
		cfg.SessionPortEnd = cfg.ScanPortEnd + defaultSessionEndOffset
	}

	if err := cfg.Validate(); err != nil {
		return config.Config{}, nil, false, err
	}

	return cfg, projectPaths, *cleanupOrphans, nil
}
