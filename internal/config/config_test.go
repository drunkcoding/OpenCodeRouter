package config

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Defaults
// ---------------------------------------------------------------------------

func TestDefaults_NonZero(t *testing.T) {
	cfg := Defaults()

	if cfg.ListenPort == 0 {
		t.Error("ListenPort should not be zero")
	}
	if cfg.ListenAddr == "" {
		t.Error("ListenAddr should not be empty")
	}
	if cfg.Username == "" {
		t.Error("Username should not be empty")
	}
	if cfg.ScanPortStart == 0 {
		t.Error("ScanPortStart should not be zero")
	}
	if cfg.ScanPortEnd == 0 {
		t.Error("ScanPortEnd should not be zero")
	}
	if cfg.ScanInterval == 0 {
		t.Error("ScanInterval should not be zero")
	}
	if cfg.ScanConcurrency == 0 {
		t.Error("ScanConcurrency should not be zero")
	}
	if cfg.ProbeTimeout == 0 {
		t.Error("ProbeTimeout should not be zero")
	}
	if cfg.StaleAfter == 0 {
		t.Error("StaleAfter should not be zero")
	}
	if cfg.MDNSServiceType == "" {
		t.Error("MDNSServiceType should not be empty")
	}
}

func TestDefaults_PortRange(t *testing.T) {
	cfg := Defaults()
	if cfg.ScanPortEnd <= cfg.ScanPortStart {
		t.Errorf("ScanPortEnd (%d) should be > ScanPortStart (%d)", cfg.ScanPortEnd, cfg.ScanPortStart)
	}
}

// ---------------------------------------------------------------------------
// Validate
// ---------------------------------------------------------------------------

func TestValidate_ValidDefaults(t *testing.T) {
	cfg := Defaults()
	if err := cfg.Validate(); err != nil {
		t.Errorf("default config should be valid, got: %v", err)
	}
}

func TestValidate_InvalidListenPort(t *testing.T) {
	tests := []struct {
		name string
		port int
	}{
		{"zero", 0},
		{"negative", -1},
		{"too high", 70000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Defaults()
			cfg.ListenPort = tt.port
			if err := cfg.Validate(); err == nil {
				t.Error("expected error for invalid listen port")
			}
		})
	}
}

func TestValidate_InvalidScanPortStart(t *testing.T) {
	cfg := Defaults()
	cfg.ScanPortStart = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for ScanPortStart == 0")
	}
}

func TestValidate_ScanPortEndBeforeStart(t *testing.T) {
	cfg := Defaults()
	cfg.ScanPortStart = 5000
	cfg.ScanPortEnd = 4000
	if err := cfg.Validate(); err == nil {
		t.Error("expected error when ScanPortEnd < ScanPortStart")
	}
}

func TestValidate_ScanPortEndTooHigh(t *testing.T) {
	cfg := Defaults()
	cfg.ScanPortEnd = 70000
	if err := cfg.Validate(); err == nil {
		t.Error("expected error when ScanPortEnd > 65535")
	}
}

func TestValidate_EmptyUsername(t *testing.T) {
	cfg := Defaults()
	cfg.Username = ""
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for empty username")
	}
}

func TestValidate_ScanIntervalTooShort(t *testing.T) {
	cfg := Defaults()
	cfg.ScanInterval = 500 * time.Millisecond
	if err := cfg.Validate(); err == nil {
		t.Error("expected error when ScanInterval < 1s")
	}
}

// ---------------------------------------------------------------------------
// DomainFor
// ---------------------------------------------------------------------------

func TestDomainFor(t *testing.T) {
	tests := []struct {
		slug     string
		username string
		want     string
	}{
		{"myproject", "alice", "myproject-alice.local"},
		{"backend", "bob", "backend-bob.local"},
		{"a", "x", "a-x.local"},
	}

	for _, tt := range tests {
		t.Run(tt.slug+"-"+tt.username, func(t *testing.T) {
			cfg := Defaults()
			cfg.Username = tt.username
			got := cfg.DomainFor(tt.slug)
			if got != tt.want {
				t.Errorf("DomainFor(%q) with username %q = %q, want %q", tt.slug, tt.username, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// GetOutboundIP
// ---------------------------------------------------------------------------

func TestGetOutboundIP_ReturnsNonNil(t *testing.T) {
	ip := GetOutboundIP()
	if ip == nil {
		t.Error("GetOutboundIP should never return nil")
	}
}

func TestGetOutboundIP_NotEmpty(t *testing.T) {
	ip := GetOutboundIP()
	if ip.String() == "" || ip.String() == "<nil>" {
		t.Errorf("GetOutboundIP returned invalid IP: %v", ip)
	}
}
