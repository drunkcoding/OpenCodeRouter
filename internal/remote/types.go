package remote

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type ExecRunner struct{}

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

type HostOverride struct {
	Label        string
	Priority     int
	OpencodePath string
	ScanPaths    []string
}

type DiscoveryOptions struct {
	Include       []string
	Ignore        []string
	Overrides     map[string]HostOverride
	SSHConfigPath string
}

type SSHOptions struct {
	ControlMaster  string
	ControlPersist int
	ControlPath    string
	BatchMode      bool
	ConnectTimeout int
}

type ProbeOptions struct {
	MaxParallel      int
	SessionScanPaths []string
	Overrides        map[string]HostOverride
	SSH              SSHOptions
	SortBy           string
	ShowArchived     bool
	MaxDisplay       int
	ActiveThreshold  time.Duration
	IdleThreshold    time.Duration
}
