package proxy

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const defaultWSPingInterval = 30 * time.Second

func (rt *Router) isWSRoute(path string) bool {
	return path == "/ws" || path == "/ws/" || strings.HasPrefix(path, "/ws/")
}

func (rt *Router) wsRoute(path string) (slug, remainder string, ok bool) {
	if !strings.HasPrefix(path, "/ws/") {
		return "", "", false
	}

	trimmed := strings.TrimPrefix(path, "/ws/")
	if trimmed == "" {
		return "", "", false
	}

	parts := strings.SplitN(trimmed, "/", 2)
	slug = parts[0]
	if slug == "" {
		return "", "", false
	}

	remainder = "/"
	if len(parts) == 2 && parts[1] != "" {
		remainder = "/" + parts[1]
	}

	return slug, remainder, true
}

func (rt *Router) handleWSProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	slug, remainder, ok := rt.wsRoute(r.URL.Path)
	if !ok {
		http.Error(w, "invalid websocket route: expected /ws/{backend-slug}/{path...}", http.StatusBadRequest)
		return
	}

	if !isWebSocketUpgrade(r) {
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return
	}

	backend, found := rt.registry.Lookup(slug)
	if !found {
		http.Error(w, fmt.Sprintf("backend %q not found", slug), http.StatusNotFound)
		return
	}

	connID := rt.trackWSConnection(slug)
	defer rt.untrackWSConnection(connID)

	rt.proxyTo(backend, w, r, remainder)
}

func isWebSocketUpgrade(r *http.Request) bool {
	if !headerHasToken(r.Header.Get("Connection"), "upgrade") {
		return false
	}
	if !headerHasToken(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	return true
}

func headerHasToken(headerValue, token string) bool {
	for _, part := range strings.Split(headerValue, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

func (rt *Router) trackWSConnection(slug string) string {
	connID := fmt.Sprintf("%s-%d", slug, atomic.AddUint64(&rt.wsConnSeq, 1))
	rt.wsMu.Lock()
	rt.wsConnections[connID] = slug
	rt.wsMu.Unlock()
	return connID
}

func (rt *Router) untrackWSConnection(connID string) {
	rt.wsMu.Lock()
	delete(rt.wsConnections, connID)
	rt.wsMu.Unlock()
}
