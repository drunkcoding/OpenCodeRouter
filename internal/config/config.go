package config

import (
	"fmt"
	"net"
	"os/user"
	"time"
)

// Config holds all router configuration.
type Config struct {
	// ListenPort is the port the router listens on.
	ListenPort int
	// ListenAddr is the full bind address (e.g. "0.0.0.0:8080").
	ListenAddr string
	// Username is the OS username of the server runner.
	// Used in domain naming and to filter discovered instances.
	Username string
	// ScanPortStart is the beginning of the port range to scan (inclusive).
	ScanPortStart int
	// ScanPortEnd is the end of the port range to scan (inclusive).
	ScanPortEnd int
	// ScanInterval controls how often the scanner runs.
	ScanInterval time.Duration
	// ScanConcurrency is the max number of concurrent port probes.
	ScanConcurrency int
	// ProbeTimeout is the HTTP timeout for each port probe.
	ProbeTimeout time.Duration
	// StaleAfter is how long a backend can go unseen before removal.
	StaleAfter time.Duration
	// EnableMDNS controls mDNS service advertisement.
	EnableMDNS bool
	// MDNSServiceType is the DNS-SD service type to advertise.
	MDNSServiceType string
}

// Defaults returns a Config with sensible defaults.
func Defaults() Config {
	username := "unknown"
	if u, err := user.Current(); err == nil {
		username = u.Username
	}

	return Config{
		ListenPort:      8080,
		ListenAddr:      "0.0.0.0:8080",
		Username:        username,
		ScanPortStart:   30000,
		ScanPortEnd:     31000,
		ScanInterval:    5 * time.Second,
		ScanConcurrency: 20,
		ProbeTimeout:    800 * time.Millisecond,
		StaleAfter:      30 * time.Second,
		EnableMDNS:      true,
		MDNSServiceType: "_opencode._tcp",
	}
}

// Validate checks the config for obvious errors.
func (c *Config) Validate() error {
	if c.ListenPort < 1 || c.ListenPort > 65535 {
		return fmt.Errorf("listen port must be 1-65535, got %d", c.ListenPort)
	}
	if c.ScanPortStart < 1 || c.ScanPortStart > 65535 {
		return fmt.Errorf("scan port start must be 1-65535, got %d", c.ScanPortStart)
	}
	if c.ScanPortEnd < c.ScanPortStart {
		return fmt.Errorf("scan port end (%d) must be >= start (%d)", c.ScanPortEnd, c.ScanPortStart)
	}
	if c.ScanPortEnd > 65535 {
		return fmt.Errorf("scan port end must be <= 65535, got %d", c.ScanPortEnd)
	}
	if c.Username == "" {
		return fmt.Errorf("username must not be empty")
	}
	if c.ScanInterval < 1*time.Second {
		return fmt.Errorf("scan interval must be >= 1s, got %s", c.ScanInterval)
	}
	return nil
}

// DomainFor returns the mDNS hostname for a project slug.
// Format: {slug}-{username}.local
func (c *Config) DomainFor(slug string) string {
	return fmt.Sprintf("%s-%s.local", slug, c.Username)
}

// GetOutboundIP returns the preferred outbound IP of this machine.
// Falls back to 127.0.0.1 if detection fails.
func GetOutboundIP() net.IP {
	// Use a UDP dial to determine the outbound interface (no actual connection made).
	conn, err := net.DialTimeout("udp", "8.8.8.8:80", 1*time.Second)
	if err != nil {
		return net.ParseIP("127.0.0.1")
	}
	defer conn.Close()
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP
}
