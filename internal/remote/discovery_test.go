package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type discoveryRunnerMock struct {
	byAlias map[string]runResult
}

type runResult struct {
	stdout string
	err    error
}

func (m discoveryRunnerMock) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
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

	hosts := ParseSSHConfigHosts(content)
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

func TestLoadHostAliases_IncludeGlobAbsolute(t *testing.T) {
	tmpDir := t.TempDir()
	mainConfigPath := filepath.Join(tmpDir, "config")
	includeDir := filepath.Join(tmpDir, "config.d")

	writeSSHConfigFile(t, mainConfigPath, "Host root-host\n  User root\nInclude "+filepath.Join(includeDir, "*.conf")+"\n")
	writeSSHConfigFile(t, filepath.Join(includeDir, "one.conf"), "Host include-one\n")
	writeSSHConfigFile(t, filepath.Join(includeDir, "two.conf"), "Host include-two\n")

	svc := NewDiscoveryService(DiscoveryOptions{}, discoveryRunnerMock{}, nil)
	svc.SetSSHConfigPath(mainConfigPath)

	aliases, err := svc.loadHostAliases()
	if err != nil {
		t.Fatalf("load host aliases: %v", err)
	}

	assertAliasSet(t, aliases, "root-host", "include-one", "include-two")
}

func TestLoadHostAliases_IncludeRelativePath(t *testing.T) {
	tmpDir := t.TempDir()
	mainConfigPath := filepath.Join(tmpDir, "config")
	relativeIncludePath := filepath.Join("includes", "relative.conf")

	writeSSHConfigFile(t, mainConfigPath, "Host root-host\nInclude "+relativeIncludePath+"\n")
	writeSSHConfigFile(t, filepath.Join(tmpDir, relativeIncludePath), "Host relative-host\n")

	svc := NewDiscoveryService(DiscoveryOptions{}, discoveryRunnerMock{}, nil)
	svc.SetSSHConfigPath(mainConfigPath)

	aliases, err := svc.loadHostAliases()
	if err != nil {
		t.Fatalf("load host aliases: %v", err)
	}

	assertAliasSet(t, aliases, "root-host", "relative-host")
}

func TestLoadHostAliases_IncludeNested(t *testing.T) {
	tmpDir := t.TempDir()
	mainConfigPath := filepath.Join(tmpDir, "config")
	levelOnePath := filepath.Join(tmpDir, "level-one.conf")
	levelTwoPath := filepath.Join(tmpDir, "level-two.conf")

	writeSSHConfigFile(t, mainConfigPath, "Host root-host\nInclude "+levelOnePath+"\n")
	writeSSHConfigFile(t, levelOnePath, "Host level-one-host\nInclude "+levelTwoPath+"\n")
	writeSSHConfigFile(t, levelTwoPath, "Host level-two-host\n")

	svc := NewDiscoveryService(DiscoveryOptions{}, discoveryRunnerMock{}, nil)
	svc.SetSSHConfigPath(mainConfigPath)

	aliases, err := svc.loadHostAliases()
	if err != nil {
		t.Fatalf("load host aliases: %v", err)
	}

	assertAliasSet(t, aliases, "root-host", "level-one-host", "level-two-host")
}

func TestLoadHostAliases_IncludeCycleSafe(t *testing.T) {
	tmpDir := t.TempDir()
	mainConfigPath := filepath.Join(tmpDir, "a.conf")
	otherConfigPath := filepath.Join(tmpDir, "b.conf")

	writeSSHConfigFile(t, mainConfigPath, "Host cycle-a\nInclude "+otherConfigPath+"\n")
	writeSSHConfigFile(t, otherConfigPath, "Host cycle-b\nInclude "+mainConfigPath+"\n")

	svc := NewDiscoveryService(DiscoveryOptions{}, discoveryRunnerMock{}, nil)
	svc.SetSSHConfigPath(mainConfigPath)

	aliases, err := svc.loadHostAliases()
	if err != nil {
		t.Fatalf("load host aliases: %v", err)
	}

	assertAliasSet(t, aliases, "cycle-a", "cycle-b")
}

func TestLoadHostAliases_IncludeNonexistentGraceful(t *testing.T) {
	tmpDir := t.TempDir()
	mainConfigPath := filepath.Join(tmpDir, "config")
	existingIncludePath := filepath.Join(tmpDir, "existing.conf")

	writeSSHConfigFile(t, mainConfigPath, "Host root-host\nInclude "+filepath.Join(tmpDir, "missing", "*.conf")+"\nInclude "+existingIncludePath+"\n")
	writeSSHConfigFile(t, existingIncludePath, "Host existing-host\n")

	svc := NewDiscoveryService(DiscoveryOptions{}, discoveryRunnerMock{}, nil)
	svc.SetSSHConfigPath(mainConfigPath)

	aliases, err := svc.loadHostAliases()
	if err != nil {
		t.Fatalf("load host aliases: %v", err)
	}

	assertAliasSet(t, aliases, "root-host", "existing-host")
}

func TestLoadHostAliases_IncludeExpandsHomeDir(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	t.Setenv("HOME", homeDir)

	mainConfigPath := filepath.Join(tmpDir, "config")
	homeIncludeDir := filepath.Join(homeDir, ".ssh", "config.d")
	writeSSHConfigFile(t, filepath.Join(homeIncludeDir, "home.conf"), "Host home-host\n")
	writeSSHConfigFile(t, mainConfigPath, "Host root-host\nInclude ~/.ssh/config.d/*.conf\n")

	svc := NewDiscoveryService(DiscoveryOptions{}, discoveryRunnerMock{}, nil)
	svc.SetSSHConfigPath(mainConfigPath)

	aliases, err := svc.loadHostAliases()
	if err != nil {
		t.Fatalf("load host aliases: %v", err)
	}

	assertAliasSet(t, aliases, "root-host", "home-host")
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

	opts := DiscoveryOptions{
		Include: []string{"prod-*", "dev-*"},
		Ignore:  []string{"backup-*"},
		Overrides: map[string]HostOverride{
			"prod-1": {
				Label:        "Production 1",
				Priority:     1,
				OpencodePath: "/usr/local/bin/opencode",
			},
		},
	}

	runner := discoveryRunnerMock{byAlias: map[string]runResult{
		"prod-1": {stdout: "hostname 10.0.0.1\nuser deploy\n"},
		"dev-1":  {stdout: "hostname 10.0.0.2\nuser dev\n"},
	}}

	svc := NewDiscoveryService(opts, runner, nil)
	svc.SetSSHConfigPath(sshPath)

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

func TestNewDiscoveryService_NilLoggerDefaultsToDiscard(t *testing.T) {
	t.Parallel()

	svc := NewDiscoveryService(DiscoveryOptions{}, discoveryRunnerMock{}, nil)
	if svc == nil {
		t.Fatal("expected discovery service to be constructed")
	}
	if svc.logger == nil {
		t.Fatal("expected discovery service logger to default to non-nil discard logger")
	}
}

func writeSSHConfigFile(t *testing.T, filePath, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatalf("create config directory %q: %v", filepath.Dir(filePath), err)
	}
	if err := os.WriteFile(filePath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config file %q: %v", filePath, err)
	}
}

func assertAliasSet(t *testing.T, got []string, want ...string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("expected %d aliases, got %d (%v)", len(want), len(got), got)
	}

	wantSet := make(map[string]struct{}, len(want))
	for _, alias := range want {
		wantSet[alias] = struct{}{}
	}

	for _, alias := range got {
		if _, ok := wantSet[alias]; !ok {
			t.Fatalf("unexpected alias %q in %v", alias, got)
		}
	}
}
