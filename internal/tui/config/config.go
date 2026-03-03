package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config is the root configuration for opencode-remote.
type Config struct {
	Polling     PollingConfig     `mapstructure:"polling" yaml:"polling"`
	Cache       CacheConfig       `mapstructure:"cache" yaml:"cache"`
	Display     DisplayConfig     `mapstructure:"display" yaml:"display"`
	Hosts       HostsConfig       `mapstructure:"hosts" yaml:"hosts"`
	SSH         SSHConfig         `mapstructure:"ssh" yaml:"ssh"`
	Sessions    SessionsConfig    `mapstructure:"sessions" yaml:"sessions"`
	Keybindings KeybindingsConfig `mapstructure:"keybindings" yaml:"keybindings"`
}

// PollingConfig defines probe scheduler behavior.
type PollingConfig struct {
	Interval    time.Duration `mapstructure:"interval" yaml:"interval"`
	Timeout     time.Duration `mapstructure:"timeout" yaml:"timeout"`
	Jitter      time.Duration `mapstructure:"jitter" yaml:"jitter"`
	MaxParallel int           `mapstructure:"max_parallel" yaml:"max_parallel"`
}

// CacheConfig defines in-memory cache behavior.
type CacheConfig struct {
	TTL           time.Duration `mapstructure:"ttl" yaml:"ttl"`
	PersistToDisk bool          `mapstructure:"persist_to_disk" yaml:"persist_to_disk"`
}

// DisplayConfig controls visual and activity rendering behavior.
type DisplayConfig struct {
	Theme           string        `mapstructure:"theme" yaml:"theme"`
	Unicode         bool          `mapstructure:"unicode" yaml:"unicode"`
	Animation       bool          `mapstructure:"animation" yaml:"animation"`
	ActiveThreshold time.Duration `mapstructure:"active_threshold" yaml:"active_threshold"`
	IdleThreshold   time.Duration `mapstructure:"idle_threshold" yaml:"idle_threshold"`
}

// HostsConfig controls host filtering, grouping, and overrides.
type HostsConfig struct {
	Include   []string                `mapstructure:"include" yaml:"include"`
	Ignore    []string                `mapstructure:"ignore" yaml:"ignore"`
	Groups    map[string][]string     `mapstructure:"groups" yaml:"groups"`
	Overrides map[string]HostOverride `mapstructure:"overrides" yaml:"overrides"`
}

// HostOverride customizes host display/probe behavior.
type HostOverride struct {
	Label        string `mapstructure:"label" yaml:"label"`
	Priority     int    `mapstructure:"priority" yaml:"priority"`
	OpencodePath string `mapstructure:"opencode_path" yaml:"opencode_path"`
}

// SSHConfig defines CLI ssh options used by probe execution.
type SSHConfig struct {
	ControlMaster  string `mapstructure:"control_master" yaml:"control_master"`
	ControlPersist int    `mapstructure:"control_persist" yaml:"control_persist"`
	BatchMode      bool   `mapstructure:"batch_mode" yaml:"batch_mode"`
	ConnectTimeout int    `mapstructure:"connect_timeout" yaml:"connect_timeout"`
}

// SessionsConfig defines view-level session filtering and limits.
type SessionsConfig struct {
	SortBy       string `mapstructure:"sort_by" yaml:"sort_by"`
	ShowArchived bool   `mapstructure:"show_archived" yaml:"show_archived"`
	MaxDisplay   int    `mapstructure:"max_display" yaml:"max_display"`
	EnrichFromDB bool   `mapstructure:"enrich_from_db" yaml:"enrich_from_db"`
}

// KeybindingsConfig defines runtime key maps.
type KeybindingsConfig struct {
	Attach      string `mapstructure:"attach" yaml:"attach"`
	Search      string `mapstructure:"search" yaml:"search"`
	Refresh     string `mapstructure:"refresh" yaml:"refresh"`
	Quit        string `mapstructure:"quit" yaml:"quit"`
	NewSession  string `mapstructure:"new_session" yaml:"new_session"`
	KillSession string `mapstructure:"kill_session" yaml:"kill_session"`
	Inspect     string `mapstructure:"inspect" yaml:"inspect"`
	CycleView   string `mapstructure:"cycle_view" yaml:"cycle_view"`
}

// Load reads YAML config from disk and merges it onto defaults.
func Load(ctx context.Context, filePath string) (Config, error) {
	select {
	case <-ctx.Done():
		return Config{}, fmt.Errorf("load config canceled: %w", ctx.Err())
	default:
	}

	cfg := DefaultConfig()
	resolved := filePath
	if strings.TrimSpace(resolved) == "" {
		resolved = DefaultPath()
	}

	if _, err := os.Stat(resolved); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if samePath(resolved, DefaultPath()) {
				// TODO: add first-run bootstrap workflow to materialize a starter file.
				return cfg, nil
			}
			return cfg, fmt.Errorf("config file %q does not exist", resolved)
		}
		return cfg, fmt.Errorf("stat config file %q: %w", resolved, err)
	}

	v := viper.New()
	v.SetConfigFile(resolved)
	v.SetConfigType("yaml")

	if err := v.ReadInConfig(); err != nil {
		return cfg, fmt.Errorf("read config file %q: %w", resolved, err)
	}

	if err := v.Unmarshal(&cfg); err != nil {
		return cfg, fmt.Errorf("unmarshal config %q: %w", resolved, err)
	}

	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("validate config %q: %w", resolved, err)
	}

	return cfg, nil
}

// Validate checks top-level constraints and required fields.
func (c Config) Validate() error {
	if c.Polling.Interval <= 0 {
		return fmt.Errorf("polling.interval must be > 0")
	}
	if c.Polling.Timeout <= 0 {
		return fmt.Errorf("polling.timeout must be > 0")
	}
	if c.Polling.MaxParallel <= 0 {
		return fmt.Errorf("polling.max_parallel must be > 0")
	}
	if c.Cache.TTL <= 0 {
		return fmt.Errorf("cache.ttl must be > 0")
	}
	if c.Display.ActiveThreshold <= 0 || c.Display.IdleThreshold <= 0 {
		return fmt.Errorf("display thresholds must be > 0")
	}
	if c.Display.ActiveThreshold > c.Display.IdleThreshold {
		return fmt.Errorf("display.active_threshold must be <= display.idle_threshold")
	}
	if c.Sessions.MaxDisplay <= 0 {
		return fmt.Errorf("sessions.max_display must be > 0")
	}
	if c.SSH.ConnectTimeout <= 0 {
		return fmt.Errorf("ssh.connect_timeout must be > 0")
	}
	if len(c.Hosts.Include) == 0 {
		return fmt.Errorf("hosts.include must contain at least one pattern")
	}
	return nil
}

// samePath compares two filesystem paths after cleaning.
func samePath(a, b string) bool {
	ca := filepath.Clean(a)
	cb := filepath.Clean(b)
	return ca == cb
}
