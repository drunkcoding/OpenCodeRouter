package discovery

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"opencoderouter/internal/tui/config"
)

type mockRunner struct {
	byAlias map[string]runResult
}

type runResult struct {
	stdout string
	err    error
}

func (m mockRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	if len(args) == 0 {
		return nil, nil
	}
	alias := args[len(args)-1]
	res, ok := m.byAlias[alias]
	if !ok {
		return []byte(""), nil
	}
	if res.err != nil {
		return nil, res.err
	}
	return []byte(res.stdout), nil
}

func TestParseSSHConfigHosts(t *testing.T) {
	content := `
Host *
  ForwardAgent no

Host prod-1 dev-1 backup-1
  User alice

Host jump-?
  User bob

Host !ignored
`

	hosts := parseSSHConfigHosts(content)
	if len(hosts) != 3 {
		t.Fatalf("expected 3 concrete hosts, got %d (%v)", len(hosts), hosts)
	}
	want := map[string]struct{}{"prod-1": {}, "dev-1": {}, "backup-1": {}}
	for _, h := range hosts {
		if _, ok := want[h]; !ok {
			t.Fatalf("unexpected host alias %q", h)
		}
	}
}

func TestDiscover_WithFilteringAndOverrides(t *testing.T) {
	tmpDir := t.TempDir()
	sshPath := filepath.Join(tmpDir, "config")
	configBody := `
Host prod-1 dev-1 backup-1
  User alice
`
	if err := os.WriteFile(sshPath, []byte(configBody), 0o600); err != nil {
		t.Fatalf("write ssh config: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.Hosts.Include = []string{"prod-*", "dev-*"}
	cfg.Hosts.Ignore = []string{"backup-*"}
	cfg.Hosts.Overrides = map[string]config.HostOverride{
		"prod-1": {
			Label:        "Production 1",
			Priority:     1,
			OpencodePath: "/usr/local/bin/opencode",
		},
	}

	runner := mockRunner{byAlias: map[string]runResult{
		"prod-1": {stdout: "hostname 10.0.0.1\nuser deploy\n"},
		"dev-1":  {stdout: "hostname 10.0.0.2\nuser dev\n"},
	}}

	svc := NewDiscoveryService(cfg, runner)
	svc.sshConfigPath = sshPath

	hosts, err := svc.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover returned error: %v", err)
	}

	if len(hosts) != 2 {
		t.Fatalf("expected 2 hosts after filters, got %d", len(hosts))
	}

	if hosts[0].Name != "prod-1" {
		t.Fatalf("expected first host to be prod-1 due priority sort, got %q", hosts[0].Name)
	}
	if hosts[0].Label != "Production 1" {
		t.Fatalf("expected override label, got %q", hosts[0].Label)
	}
	if hosts[0].OpencodeBin != "/usr/local/bin/opencode" {
		t.Fatalf("expected override opencode path, got %q", hosts[0].OpencodeBin)
	}
}
