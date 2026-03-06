package api

import (
	"net/http"

	"opencoderouter/internal/auth"
	"opencoderouter/internal/cache"
	"opencoderouter/internal/session"
	"opencoderouter/internal/terminal"
)

type RouterConfig struct {
	SessionManager        session.SessionManager
	SessionEventBus       session.EventBus
	BackendEventSubscribe BackendEventSubscribeFunc
	AuthConfig            auth.Config
	ScrollbackCache       cache.ScrollbackCache
	Fallback              http.Handler
}

func NewRouter(cfg RouterConfig) http.Handler {
	mux := http.NewServeMux()
	NewSessionsHandler(SessionsHandlerConfig{SessionManager: cfg.SessionManager, ScrollbackCache: cfg.ScrollbackCache}).Register(mux)
	NewEventsHandler(EventsHandlerConfig{
		SessionEventBus:  cfg.SessionEventBus,
		BackendSubscribe: cfg.BackendEventSubscribe,
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
