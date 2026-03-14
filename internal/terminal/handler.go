package terminal

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"opencoderouter/internal/cache"
	"opencoderouter/internal/session"
)

const defaultTerminalRoutePrefix = "/ws/terminal/"

type HandlerConfig struct {
	SessionManager  session.SessionManager
	ScrollbackCache cache.ScrollbackCache
	Bridge          *TerminalBridge
	Logger          *slog.Logger
	RoutePrefix     string
}

type Handler struct {
	sessions    session.SessionManager
	bridge      *TerminalBridge
	logger      *slog.Logger
	routePrefix string
}

func NewHandler(cfg HandlerConfig) *Handler {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	bridge := cfg.Bridge
	if bridge == nil {
		bridge = NewBridge(BridgeConfig{Logger: logger, ScrollbackCache: cfg.ScrollbackCache})
	}

	routePrefix := strings.TrimSpace(cfg.RoutePrefix)
	if routePrefix == "" {
		routePrefix = defaultTerminalRoutePrefix
	}
	if !strings.HasPrefix(routePrefix, "/") {
		routePrefix = "/" + routePrefix
	}
	if !strings.HasSuffix(routePrefix, "/") {
		routePrefix += "/"
	}

	return &Handler{
		sessions:    cfg.SessionManager,
		bridge:      bridge,
		logger:      logger,
		routePrefix: routePrefix,
	}
}

func (h *Handler) Register(mux *http.ServeMux) {
	if h == nil || mux == nil {
		return
	}
	mux.Handle(h.routePrefix, h)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		http.Error(w, "terminal handler is not configured", http.StatusInternalServerError)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isWebSocketUpgrade(r) {
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return
	}
	if h.sessions == nil {
		http.Error(w, "session manager unavailable", http.StatusInternalServerError)
		return
	}

	sessionID, ok := h.sessionIDFromPath(r.URL.Path)
	if !ok {
		http.Error(w, "invalid terminal websocket route: expected /ws/terminal/{session-id}", http.StatusBadRequest)
		return
	}

	handle, err := h.sessions.Get(sessionID)
	if err != nil {
		h.writeSessionLookupError(w, sessionID, err)
		return
	}
	if handle.Status == session.SessionStatusStopped {
		http.Error(w, fmt.Sprintf("session %q is stopped", sessionID), http.StatusServiceUnavailable)
		return
	}

	health, err := h.sessions.Health(r.Context(), sessionID)
	if err != nil {
		h.writeSessionLookupError(w, sessionID, err)
		return
	}
	if health.State == session.HealthStateUnhealthy {
		msg := fmt.Sprintf("session %q is unhealthy", sessionID)
		if health.Error != "" {
			msg = fmt.Sprintf("%s: %s", msg, health.Error)
		}
		http.Error(w, msg, http.StatusServiceUnavailable)
		return
	}

	terminalConn, err := h.sessions.AttachTerminal(r.Context(), sessionID)
	if err != nil {
		h.writeAttachError(w, sessionID, err)
		return
	}

	wsConn, err := h.bridge.Upgrade(w, r)
	if err != nil {
		_ = terminalConn.Close()
		h.logger.Warn("terminal websocket upgrade failed", "session_id", sessionID, "error", err)
		return
	}

	start := time.Now()
	h.logger.Info("terminal websocket accepted", "session_id", sessionID, "remote_addr", r.RemoteAddr)
	bridgeErr := h.bridge.Bridge(r.Context(), wsConn, terminalConn, sessionID)
	h.logger.Info("terminal websocket disconnected", "session_id", sessionID, "duration", time.Since(start), "error", bridgeErr)
}

func (h *Handler) sessionIDFromPath(path string) (string, bool) {
	if !strings.HasPrefix(path, h.routePrefix) {
		return "", false
	}

	tail := strings.TrimPrefix(path, h.routePrefix)
	tail = strings.TrimSpace(tail)
	if tail == "" {
		return "", false
	}
	if strings.Contains(tail, "/") {
		return "", false
	}

	return tail, true
}

func (h *Handler) writeSessionLookupError(w http.ResponseWriter, sessionID string, err error) {
	if errors.Is(err, session.ErrSessionNotFound) {
		http.Error(w, fmt.Sprintf("session %q not found", sessionID), http.StatusNotFound)
		return
	}

	http.Error(w, fmt.Sprintf("failed to resolve session %q: %v", sessionID, err), http.StatusBadGateway)
}

func (h *Handler) writeAttachError(w http.ResponseWriter, sessionID string, err error) {
	if errors.Is(err, session.ErrSessionNotFound) {
		http.Error(w, fmt.Sprintf("session %q not found", sessionID), http.StatusNotFound)
		return
	}
	if errors.Is(err, session.ErrSessionStopped) {
		http.Error(w, fmt.Sprintf("session %q is stopped", sessionID), http.StatusServiceUnavailable)
		return
	}

	http.Error(w, fmt.Sprintf("failed to attach terminal for session %q: %v", sessionID, err), http.StatusBadGateway)
}
