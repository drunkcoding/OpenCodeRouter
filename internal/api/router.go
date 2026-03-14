package api

import (
	"log/slog"
	"net/http"
	"time"

	"opencoderouter/internal/auth"
	"opencoderouter/internal/cache"
	"opencoderouter/internal/remote"
	"opencoderouter/internal/session"
	"opencoderouter/internal/terminal"
)

type RouterConfig struct {
	SessionManager        session.SessionManager
	SessionEventBus       session.EventBus
	BackendEventSubscribe BackendEventSubscribeFunc
	AuthConfig            auth.Config
	ScrollbackCache       cache.ScrollbackCache
	RemoteDiscovery       remote.DiscoveryOptions
	RemoteProbe           remote.ProbeOptions
	RemoteCacheTTL        time.Duration
	RemoteRunner          remote.Runner
	RemoteLogger          *slog.Logger
	Fallback              http.Handler
}

func NewRouter(cfg RouterConfig) http.Handler {
	mux := http.NewServeMux()
	NewSessionsHandler(SessionsHandlerConfig{SessionManager: cfg.SessionManager, ScrollbackCache: cfg.ScrollbackCache}).Register(mux)
	NewEventsHandler(EventsHandlerConfig{
		SessionEventBus:  cfg.SessionEventBus,
		BackendSubscribe: cfg.BackendEventSubscribe,
	}).Register(mux)
	NewRemoteHostsHandler(RemoteHostsHandlerConfig{
		DiscoveryOptions: cfg.RemoteDiscovery,
		ProbeOptions:     cfg.RemoteProbe,
		CacheTTL:         cfg.RemoteCacheTTL,
		Runner:           cfg.RemoteRunner,
		Logger:           cfg.RemoteLogger,
	}).Register(mux)

	// Wire up the terminal handler
	terminal.NewHandler(terminal.HandlerConfig{
		SessionManager:  cfg.SessionManager,
		ScrollbackCache: cfg.ScrollbackCache,
	}).Register(mux)

	fallback := cfg.Fallback
	if fallback == nil {
		fallback = http.NotFoundHandler()
	}
	mux.Handle("/", fallback)

	authCfg := cfg.AuthConfig
	defaults := auth.Defaults()
	if authCfg.BypassPaths == nil {
		authCfg.BypassPaths = defaults.BypassPaths
	}
	if len(authCfg.CORSAllowedOrigins) == 0 {
		authCfg.CORSAllowedOrigins = defaults.CORSAllowedOrigins
	}
	if authCfg.BasicAuth == nil {
		authCfg.BasicAuth = map[string]string{}
	}

	return auth.Middleware(mux, authCfg)
}
