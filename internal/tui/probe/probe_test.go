package probe

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"opencoderouter/internal/model"
	"opencoderouter/internal/tui/config"
)

type probeRunnerMock struct {
	mu      sync.Mutex
	output  map[string]string
	err     map[string]error
	calls   int
	lastSSH []string
}

func (m *probeRunnerMock) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.lastSSH = append([]string(nil), args...)

	if len(args) < 2 {
		return []byte("[]"), nil
	}
	host := args[len(args)-2]
	if err := m.err[host]; err != nil {
		return nil, err
	}
	if out, ok := m.output[host]; ok {
		return []byte(out), nil
	}
	return []byte("[]"), nil
}

func TestProbeHosts_ParsesSessions(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Sessions.ShowArchived = false
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

	svc := NewProbeService(cfg, runner, NewCacheStore(time.Minute), nil)
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
	cfg := config.DefaultConfig()
	runner := &probeRunnerMock{
		output: map[string]string{},
		err: map[string]error{
			"prod-1": errors.New("ssh failed"),
		},
	}

	svc := NewProbeService(cfg, runner, nil, nil)
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

func TestProbeHosts_OpencodeNativeFormat(t *testing.T) {
	cfg := config.DefaultConfig()
	runner := &probeRunnerMock{
		output: map[string]string{
			"dev-2": `[
				{"id":"s1","title":"Fix bug","updated":1772565534745,"created":1772563561839,"projectId":"abc123","directory":"/home/user/DeviceEmulator"},
				{"id":"s2","title":"Add feature","updated":1772565000000,"created":1772560000000,"projectId":"def456","directory":"/home/user/MobiCom"}
			]`,
		},
		err: map[string]error{},
	}

	svc := NewProbeService(cfg, runner, NewCacheStore(time.Minute), nil)
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
	cfg := config.DefaultConfig()
	runner := &probeRunnerMock{
		output: map[string]string{
			"dev-3": `[{"id":"s1","title":"A","updated":1772565534745,"directory":"/home/user/proj-a"}]` +
				`[{"id":"s2","title":"B","updated":1772565000000,"directory":"/home/user/proj-b"}]`,
		},
		err: map[string]error{},
	}

	svc := NewProbeService(cfg, runner, NewCacheStore(time.Minute), nil)
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
	cfg := config.DefaultConfig()
	runner := &probeRunnerMock{
		output: map[string]string{
			"cache-1": `[]`,
		},
		err: map[string]error{},
	}

	cache := NewCacheStore(time.Minute)
	svc := NewProbeService(cfg, runner, cache, nil)

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

	svc := NewProbeService(config.DefaultConfig(), &probeRunnerMock{}, nil, nil)
	if svc == nil {
		t.Fatal("expected probe service to be constructed")
	}
	if svc.logger == nil {
		t.Fatal("expected probe service logger to default to non-nil discard logger")
	}
}
