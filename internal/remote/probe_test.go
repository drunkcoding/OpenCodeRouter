package remote

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"opencoderouter/internal/model"
)

type probeRunnerMock struct {
	mu      sync.Mutex
	output  map[string]string
	err     map[string]error
	runFn   map[string]func(context.Context) ([]byte, error)
	calls   int
	lastSSH []string
}

func (m *probeRunnerMock) Run(ctx context.Context, _ string, args ...string) ([]byte, error) {
	m.mu.Lock()
	m.calls++
	m.lastSSH = append([]string(nil), args...)

	if len(args) < 2 {
		m.mu.Unlock()
		return []byte("[]"), nil
	}
	host := args[len(args)-2]
	if runFn := m.runFn[host]; runFn != nil {
		m.mu.Unlock()
		return runFn(ctx)
	}
	err := m.err[host]
	out, ok := m.output[host]
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if ok {
		return []byte(out), nil
	}
	return []byte("[]"), nil
}

func defaultProbeOptions() ProbeOptions {
	return ProbeOptions{
		MaxParallel:      2,
		SessionScanPaths: nil,
		Overrides:        nil,
		SSH: SSHOptions{
			BatchMode:      true,
			ConnectTimeout: 10,
		},
		SortBy:          "last_activity",
		ShowArchived:    false,
		MaxDisplay:      50,
		ActiveThreshold: 10 * time.Minute,
		IdleThreshold:   24 * time.Hour,
	}
}

func TestProbeHosts_ParsesSessions(t *testing.T) {
	opts := defaultProbeOptions()
	opts.ShowArchived = false
	runner := &probeRunnerMock{
		output: map[string]string{
			"dev-1": `[
				{"id":"s1","project":"alpha","title":"Fix bug","last_activity":"2026-03-01T10:00:00Z","status":"active","message_count":5,"agents":["coder"]},
				{"id":"s2","project":"alpha","title":"Done","last_activity":"2026-03-01T09:00:00Z","status":"archived","message_count":3,"agents":["coder"]},
				{"id":"s3","project":"beta","title":"Investigate","last_activity":"2026-03-01T11:00:00Z","status":"idle","message_count":2,"agents":["oracle"]}
			]`,
		},
		err: map[string]error{},
	}

	svc := NewProbeService(opts, runner, NewCacheStore(time.Minute), nil)
	hosts, err := svc.ProbeHosts(context.Background(), []model.Host{{Name: "dev-1"}})
	if err != nil {
		t.Fatalf("probe hosts failed: %v", err)
	}
	if len(hosts) != 1 {
		t.Fatalf("expected 1 host, got %d", len(hosts))
	}
	if hosts[0].Status != model.HostStatusOnline {
		t.Fatalf("expected host online, got %s", hosts[0].Status)
	}
	if len(hosts[0].Projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(hosts[0].Projects))
	}

	totalSessions := hosts[0].SessionCount()
	if totalSessions != 2 {
		t.Fatalf("expected archived sessions filtered, got %d visible sessions", totalSessions)
	}
}

func TestProbeHosts_PropagatesErrors(t *testing.T) {
	opts := defaultProbeOptions()
	runner := &probeRunnerMock{
		output: map[string]string{},
		err: map[string]error{
			"prod-1": errors.New("ssh failed"),
		},
	}

	svc := NewProbeService(opts, runner, nil, nil)
	hosts, err := svc.ProbeHosts(context.Background(), []model.Host{{Name: "prod-1"}})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if len(hosts) != 1 {
		t.Fatalf("expected one host result, got %d", len(hosts))
	}
	if hosts[0].Status != model.HostStatusOffline {
		t.Fatalf("expected offline status, got %s", hosts[0].Status)
	}
}

func TestProbeHosts_CanceledContextPreservesHostMetadata(t *testing.T) {
	opts := defaultProbeOptions()
	runner := &probeRunnerMock{
		output: map[string]string{},
		err:    map[string]error{},
		runFn: map[string]func(context.Context) ([]byte, error){
			"cancel-1": func(_ context.Context) ([]byte, error) {
				time.Sleep(25 * time.Millisecond)
				return []byte("[]"), nil
			},
		},
	}

	svc := NewProbeService(opts, runner, nil, nil)
	hostInput := model.Host{
		Name:    "cancel-1",
		Label:   "Cancel Host",
		Address: "cancel-1.local",
		User:    "alice",
		Status:  model.HostStatusUnknown,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	hosts, err := svc.ProbeHosts(ctx, []model.Host{hostInput})
	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled error, got %v", err)
	}
	if len(hosts) != 1 {
		t.Fatalf("expected one host result, got %d", len(hosts))
	}
	if hosts[0].Name != hostInput.Name {
		t.Fatalf("expected host name %q to be preserved, got %q", hostInput.Name, hosts[0].Name)
	}
	if hosts[0].Label != hostInput.Label {
		t.Fatalf("expected host label %q to be preserved, got %q", hostInput.Label, hosts[0].Label)
	}
	if hosts[0].Address != hostInput.Address {
		t.Fatalf("expected host address %q to be preserved, got %q", hostInput.Address, hosts[0].Address)
	}
	if hosts[0].Status != hostInput.Status {
		t.Fatalf("expected host status %q to remain unchanged, got %q", hostInput.Status, hosts[0].Status)
	}
}

func TestProbeHosts_PartialFleetProbeRetainsMetadataForAllEntries(t *testing.T) {
	opts := defaultProbeOptions()
	opts.MaxParallel = 2
	runner := &probeRunnerMock{
		output: map[string]string{},
		err:    map[string]error{},
		runFn: map[string]func(context.Context) ([]byte, error){
			"fast-1": func(_ context.Context) ([]byte, error) {
				time.Sleep(25 * time.Millisecond)
				return []byte("[]"), nil
			},
			"slow-1": func(_ context.Context) ([]byte, error) {
				time.Sleep(25 * time.Millisecond)
				return []byte("[]"), nil
			},
		},
	}

	svc := NewProbeService(opts, runner, nil, nil)
	hostInput := []model.Host{
		{Name: "fast-1", Label: "Fast Host", Status: model.HostStatusUnknown},
		{Name: "slow-1", Label: "Slow Host", Status: model.HostStatusUnknown},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	hosts, err := svc.ProbeHosts(ctx, hostInput)
	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled error, got %v", err)
	}
	if len(hosts) != len(hostInput) {
		t.Fatalf("expected %d host results, got %d", len(hostInput), len(hosts))
	}
	if hosts[0].Name != hostInput[0].Name {
		t.Fatalf("expected host[0] name %q, got %q", hostInput[0].Name, hosts[0].Name)
	}
	if hosts[0].Label != hostInput[0].Label {
		t.Fatalf("expected host[0] label %q, got %q", hostInput[0].Label, hosts[0].Label)
	}
	if hosts[1].Name != hostInput[1].Name {
		t.Fatalf("expected host[1] name %q, got %q", hostInput[1].Name, hosts[1].Name)
	}
	if hosts[1].Label != hostInput[1].Label {
		t.Fatalf("expected host[1] label %q, got %q", hostInput[1].Label, hosts[1].Label)
	}
}

func TestProbeHosts_MissingOpencodeClassifiedOffline(t *testing.T) {
	opts := defaultProbeOptions()
	runner := &probeRunnerMock{
		output: map[string]string{
			"no-opencode": "__OCR_OPENCODE_MISSING__\n",
		},
		err: map[string]error{},
	}

	svc := NewProbeService(opts, runner, nil, nil)
	hosts, err := svc.ProbeHosts(context.Background(), []model.Host{{Name: "no-opencode"}})
	if err == nil {
		t.Fatal("expected missing opencode to return an error")
	}
	if len(hosts) != 1 {
		t.Fatalf("expected one host result, got %d", len(hosts))
	}
	if hosts[0].Status != model.HostStatusOffline {
		t.Fatalf("expected offline status for missing opencode, got %q", hosts[0].Status)
	}
	if !strings.Contains(hosts[0].LastError, "opencode") {
		t.Fatalf("expected missing-opencode error context, got %q", hosts[0].LastError)
	}
}

func TestProbeHosts_PerHostTimeoutIsolation(t *testing.T) {
	opts := defaultProbeOptions()
	opts.MaxParallel = 2
	opts.SSH.ConnectTimeout = 1

	var mu sync.Mutex
	sawDeadline := map[string]bool{}

	runner := &probeRunnerMock{
		output: map[string]string{},
		err:    map[string]error{},
		runFn: map[string]func(context.Context) ([]byte, error){
			"slow-timeout": func(ctx context.Context) ([]byte, error) {
				_, ok := ctx.Deadline()
				mu.Lock()
				sawDeadline["slow-timeout"] = ok
				mu.Unlock()
				<-ctx.Done()
				return nil, ctx.Err()
			},
			"fast-ok": func(ctx context.Context) ([]byte, error) {
				_, ok := ctx.Deadline()
				mu.Lock()
				sawDeadline["fast-ok"] = ok
				mu.Unlock()
				return []byte(`[]`), nil
			},
		},
	}

	svc := NewProbeService(opts, runner, nil, nil)

	type probeOutcome struct {
		hosts []model.Host
		err   error
	}
	outcomeCh := make(chan probeOutcome, 1)
	go func() {
		hosts, err := svc.ProbeHosts(context.Background(), []model.Host{{Name: "slow-timeout"}, {Name: "fast-ok"}})
		outcomeCh <- probeOutcome{hosts: hosts, err: err}
	}()

	select {
	case outcome := <-outcomeCh:
		if outcome.err == nil {
			t.Fatal("expected timeout error for slow host, got nil")
		}
		if !errors.Is(outcome.err, context.DeadlineExceeded) {
			t.Fatalf("expected deadline exceeded in aggregate error, got %v", outcome.err)
		}
		if len(outcome.hosts) != 2 {
			t.Fatalf("expected two host results, got %d", len(outcome.hosts))
		}
		if outcome.hosts[0].Name != "slow-timeout" {
			t.Fatalf("expected slow host metadata retained, got %q", outcome.hosts[0].Name)
		}
		if outcome.hosts[0].Status != model.HostStatusOffline {
			t.Fatalf("expected slow host offline, got %q", outcome.hosts[0].Status)
		}
		if outcome.hosts[1].Name != "fast-ok" {
			t.Fatalf("expected fast host metadata retained, got %q", outcome.hosts[1].Name)
		}
		if outcome.hosts[1].Status != model.HostStatusOnline {
			t.Fatalf("expected fast host online, got %q", outcome.hosts[1].Status)
		}
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("probe hosts did not return within per-host timeout bound")
	}

	mu.Lock()
	defer mu.Unlock()
	if !sawDeadline["slow-timeout"] {
		t.Fatal("expected slow host probe context to have deadline")
	}
	if !sawDeadline["fast-ok"] {
		t.Fatal("expected fast host probe context to have deadline")
	}
}

func TestClassifyProbeResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		output        []byte
		runErr        error
		parseErr      error
		authRequired  bool
		wantStatus    model.HostStatus
		wantErr       bool
		wantErrKind   string
		wantLastError string
	}{
		{
			name:          "auth error",
			runErr:        errors.New("permission denied"),
			authRequired:  true,
			wantStatus:    model.HostStatusAuthRequired,
			wantErr:       true,
			wantErrKind:   "auth",
			wantLastError: "password authentication required",
		},
		{
			name:          "generic run error",
			runErr:        context.DeadlineExceeded,
			wantStatus:    model.HostStatusOffline,
			wantErr:       true,
			wantErrKind:   "timeout",
			wantLastError: context.DeadlineExceeded.Error(),
		},
		{
			name:          "missing opencode sentinel",
			output:        []byte(opencodeMissingSentinel + "\n"),
			wantStatus:    model.HostStatusOffline,
			wantErr:       true,
			wantErrKind:   "opencode_missing",
			wantLastError: "opencode binary not found",
		},
		{
			name:          "parse error",
			parseErr:      errors.New("invalid character 'o' in literal null"),
			wantStatus:    model.HostStatusError,
			wantErr:       true,
			wantErrKind:   "parse",
			wantLastError: "invalid character 'o' in literal null",
		},
		{
			name:       "success",
			output:     []byte("[]"),
			wantStatus: model.HostStatusOnline,
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := classifyProbeResult("test-host", tt.output, tt.runErr, tt.parseErr, tt.authRequired)
			if got.status != tt.wantStatus {
				t.Fatalf("expected status %q, got %q", tt.wantStatus, got.status)
			}
			if tt.wantLastError != "" && got.lastError != tt.wantLastError {
				t.Fatalf("expected lastError %q, got %q", tt.wantLastError, got.lastError)
			}
			if tt.wantErr {
				if got.err == nil {
					t.Fatal("expected non-nil error")
				}
				if got.errKind != tt.wantErrKind {
					t.Fatalf("expected errKind %q, got %q", tt.wantErrKind, got.errKind)
				}
				return
			}
			if got.err != nil {
				t.Fatalf("expected nil error, got %v", got.err)
			}
		})
	}
}

func TestProbeHosts_OpencodeNativeFormat(t *testing.T) {
	opts := defaultProbeOptions()
	runner := &probeRunnerMock{
		output: map[string]string{
			"dev-2": `[
				{"id":"s1","title":"Fix bug","updated":1772565534745,"created":1772563561839,"projectId":"abc123","directory":"/home/user/DeviceEmulator"},
				{"id":"s2","title":"Add feature","updated":1772565000000,"created":1772560000000,"projectId":"def456","directory":"/home/user/MobiCom"}
			]`,
		},
		err: map[string]error{},
	}

	svc := NewProbeService(opts, runner, NewCacheStore(time.Minute), nil)
	hosts, err := svc.ProbeHosts(context.Background(), []model.Host{{Name: "dev-2"}})
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	if hosts[0].Status != model.HostStatusOnline {
		t.Fatalf("expected online, got %s", hosts[0].Status)
	}
	if len(hosts[0].Projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(hosts[0].Projects))
	}
	found := false
	for _, p := range hosts[0].Projects {
		if p.Name == "DeviceEmulator" {
			found = true
			if len(p.Sessions) != 1 || p.Sessions[0].ID != "s1" {
				t.Fatalf("unexpected sessions for DeviceEmulator: %+v", p.Sessions)
			}
			if p.Sessions[0].LastActivity.IsZero() {
				t.Fatal("expected non-zero LastActivity from epoch ms")
			}
		}
	}
	if !found {
		t.Fatal("project DeviceEmulator not found")
	}
}

func TestProbeHosts_MultiArraySweep(t *testing.T) {
	opts := defaultProbeOptions()
	runner := &probeRunnerMock{
		output: map[string]string{
			"dev-3": `[{"id":"s1","title":"A","updated":1772565534745,"directory":"/home/user/proj-a"}]` +
				`[{"id":"s2","title":"B","updated":1772565000000,"directory":"/home/user/proj-b"}]`,
		},
		err: map[string]error{},
	}

	svc := NewProbeService(opts, runner, NewCacheStore(time.Minute), nil)
	hosts, err := svc.ProbeHosts(context.Background(), []model.Host{{Name: "dev-3"}})
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	if len(hosts[0].Projects) != 2 {
		t.Fatalf("expected 2 projects from multi-array, got %d", len(hosts[0].Projects))
	}
	total := hosts[0].SessionCount()
	if total != 2 {
		t.Fatalf("expected 2 sessions total, got %d", total)
	}
}

func TestProbeHosts_UsesCache(t *testing.T) {
	opts := defaultProbeOptions()
	runner := &probeRunnerMock{
		output: map[string]string{
			"cache-1": `[]`,
		},
		err: map[string]error{},
	}

	cache := NewCacheStore(time.Minute)
	svc := NewProbeService(opts, runner, cache, nil)

	_, err := svc.ProbeHosts(context.Background(), []model.Host{{Name: "cache-1"}})
	if err != nil {
		t.Fatalf("first probe failed: %v", err)
	}
	_, err = svc.ProbeHosts(context.Background(), []model.Host{{Name: "cache-1"}})
	if err != nil {
		t.Fatalf("second probe failed: %v", err)
	}

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.calls != 1 {
		t.Fatalf("expected runner to be called once due cache, got %d", runner.calls)
	}
}

func TestNewProbeService_NilLoggerDefaultsToDiscard(t *testing.T) {
	t.Parallel()

	svc := NewProbeService(defaultProbeOptions(), &probeRunnerMock{}, nil, nil)
	if svc == nil {
		t.Fatal("expected probe service to be constructed")
	}
	if svc.logger == nil {
		t.Fatal("expected probe service logger to default to non-nil discard logger")
	}
}
