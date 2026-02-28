package discovery

import (
	"fmt"
	"log/slog"
	"net"
	"sync"

	"opencoderouter/internal/config"
	"opencoderouter/internal/registry"

	"github.com/grandcat/zeroconf"
)

// Advertiser manages mDNS service advertisements for discovered backends.
// Each backend gets its own mDNS entry so clients on the LAN can discover
// individual OpenCode projects.
type Advertiser struct {
	cfg        config.Config
	outboundIP net.IP
	servers    map[string]*zeroconf.Server // slug → mDNS server
	mu         sync.Mutex
	logger     *slog.Logger
}

// New creates a new mDNS Advertiser.
func New(cfg config.Config, logger *slog.Logger) *Advertiser {
	return &Advertiser{
		cfg:        cfg,
		outboundIP: config.GetOutboundIP(),
		servers:    make(map[string]*zeroconf.Server),
		logger:     logger,
	}
}

// Sync reconciles the set of mDNS advertisements with the current registry state.
// It registers new backends and unregisters removed ones.
func (a *Advertiser) Sync(backends []*registry.Backend) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Build a set of current slugs.
	currentSlugs := make(map[string]struct{}, len(backends))
	for _, b := range backends {
		currentSlugs[b.Slug] = struct{}{}
	}

	// Remove advertisements for backends no longer present.
	for slug, srv := range a.servers {
		if _, ok := currentSlugs[slug]; !ok {
			srv.Shutdown()
			delete(a.servers, slug)
			a.logger.Info("mDNS service removed", "slug", slug)
		}
	}

	// Add advertisements for new backends.
	for _, b := range backends {
		if _, ok := a.servers[b.Slug]; ok {
			continue // already advertised
		}
		if err := a.register(b); err != nil {
			a.logger.Error("mDNS registration failed", "slug", b.Slug, "error", err)
		}
	}
}

// register creates an mDNS entry for a single backend.
func (a *Advertiser) register(b *registry.Backend) error {
	host := a.cfg.DomainFor(b.Slug)
	ip := a.outboundIP.String()
	txt := []string{
		fmt.Sprintf("project=%s", b.ProjectName),
		fmt.Sprintf("path=%s", b.ProjectPath),
		fmt.Sprintf("backend=127.0.0.1:%d", b.Port),
		fmt.Sprintf("owner=%s", a.cfg.Username),
	}

	if b.Version != "" {
		txt = append(txt, fmt.Sprintf("version=%s", b.Version))
	}

	// RegisterProxy lets us set a custom hostname for the A record,
	// so "{slug}-{username}.local" resolves to this machine's IP.
	srv, err := zeroconf.RegisterProxy(
		b.Slug,                // instance name
		a.cfg.MDNSServiceType, // service type: "_opencode._tcp"
		"local.",              // domain
		a.cfg.ListenPort,      // port (router's port, not the backend)
		host,                  // hostname for A record
		[]string{ip},          // IPs
		txt,                   // TXT records
		nil,                   // interfaces (nil = all)
	)
	if err != nil {
		return fmt.Errorf("zeroconf.RegisterProxy: %w", err)
	}

	a.servers[b.Slug] = srv
	a.logger.Info("mDNS service registered",
		"slug", b.Slug,
		"host", host,
		"ip", ip,
		"port", a.cfg.ListenPort,
	)
	return nil
}

// Shutdown stops all mDNS advertisements.
func (a *Advertiser) Shutdown() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for slug, srv := range a.servers {
		srv.Shutdown()
		a.logger.Debug("mDNS service shut down", "slug", slug)
	}
	a.servers = make(map[string]*zeroconf.Server)
	a.logger.Info("all mDNS services shut down")
}
