package config

import (
	"os"
	"path/filepath"
	"time"
)

// DefaultPath returns ~/.opencode/remote-tui.yaml when HOME is available.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".opencode/remote-tui.yaml"
	}
	return filepath.Join(home, ".opencode", "remote-tui.yaml")
}

// DefaultConfig returns the baseline configuration values.
func DefaultConfig() Config {
	return Config{
		Polling: PollingConfig{
			Interval:    30 * time.Second,
			Timeout:     10 * time.Second,
			Jitter:      5 * time.Second,
			MaxParallel: 10,
		},
		Cache: CacheConfig{
			TTL:           60 * time.Second,
			PersistToDisk: false,
		},
		Display: DisplayConfig{
			Theme:           "nightops",
			Unicode:         true,
			Animation:       true,
			ActiveThreshold: 10 * time.Minute,
			IdleThreshold:   24 * time.Hour,
		},
		Hosts: HostsConfig{
			Include: []string{"*"},
			Ignore:  []string{"backup-*"},
			Groups: map[string][]string{
				"production":  {"prod-*"},
				"development": {"dev-*"},
			},
			Overrides: map[string]HostOverride{},
		},
		SSH: SSHConfig{
			ControlMaster:  "auto",
			ControlPersist: 60,
			ControlPath:    "~/.ssh/ocr-%C",
			BatchMode:      true,
			ConnectTimeout: 10,
		},
		Sessions: SessionsConfig{
			SortBy:       "last_activity",
			ShowArchived: false,
			MaxDisplay:   50,
			EnrichFromDB: true,
		},
		Keybindings: KeybindingsConfig{
			Attach:      "enter",
			Search:      "/",
			Refresh:     "r",
			Quit:        "q",
			NewSession:  "n",
			KillSession: "d",
			Inspect:      "i",
			CycleView:    "tab",
			Authenticate: "a",
		},
	}
}
