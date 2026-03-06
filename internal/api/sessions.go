package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"

	"opencoderouter/internal/cache"
	"opencoderouter/internal/daemon"
	errorx "opencoderouter/internal/errors"
	"opencoderouter/internal/session"
)

type SessionsHandlerConfig struct {
	SessionManager  session.SessionManager
	ScrollbackCache cache.ScrollbackCache
	Logger          *slog.Logger
}

type SessionsHandler struct {
	sessions   session.SessionManager
	scrollback *ScrollbackHandler
	logger     *slog.Logger

	mu          sync.Mutex
	attachments map[string][]session.TerminalConn
}

type createSessionRequest struct {
	WorkspacePath string            `json:"workspacePath"`
	Label         string            `json:"label"`
	Labels        map[string]string `json:"labels"`
}

type sessionView struct {
	ID              string                `json:"id"`
	DaemonPort      int                   `json:"daemonPort"`
	WorkspacePath   string                `json:"workspacePath"`
	Status          session.SessionStatus `json:"status"`
	CreatedAt       string                `json:"createdAt"`
	LastActivity    string                `json:"lastActivity"`
	AttachedClients int                   `json:"attachedClients"`
	Labels          map[string]string     `json:"labels,omitempty"`
	Health          session.HealthStatus  `json:"health"`
}

type errorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func NewSessionsHandler(cfg SessionsHandlerConfig) *SessionsHandler {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &SessionsHandler{
		sessions:    cfg.SessionManager,
		scrollback:  NewScrollbackHandler(cfg.ScrollbackCache),
		logger:      logger,
		attachments: make(map[string][]session.TerminalConn),
	}
}

func (h *SessionsHandler) Register(mux *http.ServeMux) {
	if h == nil || mux == nil {
		return
	}
	mux.HandleFunc("/api/sessions", h.handleCollection)
	mux.HandleFunc("/api/sessions/", h.handleByID)
}

func (h *SessionsHandler) handleCollection(w http.ResponseWriter, r *http.Request) {
	if h.sessions == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "session manager unavailable", "SESSION_MANAGER_UNAVAILABLE")
		return
	}

	switch r.Method {
	case http.MethodPost:
		h.handleCreate(w, r)
	case http.MethodGet:
		h.handleList(w, r)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
	}
}

func (h *SessionsHandler) handleByID(w http.ResponseWriter, r *http.Request) {
	if h.sessions == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "session manager unavailable", "SESSION_MANAGER_UNAVAILABLE")
		return
	}

	id, action, ok := parseSessionPath(r.URL.Path)
	if !ok {
		writeAPIError(w, http.StatusNotFound, "route not found", "NOT_FOUND")
		return
	}

	if action == "" {
		switch r.Method {
		case http.MethodGet:
			h.handleGet(w, r, id)
		case http.MethodDelete:
			h.handleDelete(w, r, id)
		default:
			writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
		}
		return
	}

	if action == "chat" && r.Method == http.MethodGet {
		h.handleChatHistory(w, r, id)
		return
	}

	if action == "scrollback" && r.Method == http.MethodGet {
		h.handleScrollback(w, r, id)
		return
	}

	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	switch action {
	case "stop":
		h.handleStop(w, r, id)
	case "start":
		h.handleStart(w, r, id)
	case "restart":
		h.handleRestart(w, r, id)
	case "attach":
		h.handleAttach(w, r, id)
	case "detach":
		h.handleDetach(w, r, id)
	case "chat":
		h.handleChat(w, r, id)
	default:
		writeAPIError(w, http.StatusNotFound, "route not found", "NOT_FOUND")
	}
}

func (h *SessionsHandler) handleScrollback(w http.ResponseWriter, r *http.Request, id string) {
	if _, err := h.sessions.Get(id); err != nil {
		h.writeSessionManagerError(w, err)
		return
	}
	h.scrollback.HandleGet(w, r, id)
}

func (h *SessionsHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req createSessionRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "INVALID_REQUEST_BODY")
		return
	}

	opts := session.CreateOpts{
		WorkspacePath: req.WorkspacePath,
	}
	if len(req.Labels) > 0 || strings.TrimSpace(req.Label) != "" {
		labels := make(map[string]string, len(req.Labels)+1)
		for k, v := range req.Labels {
			labels[k] = v
		}
		if label := strings.TrimSpace(req.Label); label != "" {
			if _, exists := labels["label"]; !exists {
				labels["label"] = label
			}
		}
		opts.Labels = labels
	}

	handle, err := h.sessions.Create(r.Context(), opts)
	if err != nil {
		h.writeSessionManagerError(w, err)
		return
	}

	view, err := h.buildSessionView(r.Context(), handle.ID)
	if err != nil {
		h.writeSessionManagerError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, view)
}

func (h *SessionsHandler) handleList(w http.ResponseWriter, r *http.Request) {
	filter := session.SessionListFilter{}

	if rawStatus := strings.TrimSpace(r.URL.Query().Get("status")); rawStatus != "" {
		status := session.SessionStatus(rawStatus)
		if !isValidSessionStatus(status) {
			writeAPIError(w, http.StatusBadRequest, "invalid status filter", "INVALID_STATUS_FILTER")
			return
		}
		filter.Status = status
	}

	handles, err := h.sessions.List(filter)
	if err != nil {
		h.writeSessionManagerError(w, err)
		return
	}

	switch strings.TrimSpace(r.URL.Query().Get("sort")) {
	case "", "createdAt":
	case "lastActivity":
		sort.Slice(handles, func(i, j int) bool {
			if handles[i].LastActivity.Equal(handles[j].LastActivity) {
				return handles[i].ID < handles[j].ID
			}
			return handles[i].LastActivity.After(handles[j].LastActivity)
		})
	default:
		writeAPIError(w, http.StatusBadRequest, "invalid sort option", "INVALID_SORT")
		return
	}

	views := make([]sessionView, 0, len(handles))
	for _, handle := range handles {
		health, err := h.sessions.Health(r.Context(), handle.ID)
		if err != nil {
			h.logger.Debug("session health lookup failed for list", "session_id", handle.ID, "error", err)
			health = session.HealthStatus{State: session.HealthStateUnknown}
		}
		views = append(views, toSessionView(handle, health))
	}

	writeJSON(w, http.StatusOK, views)
}

func (h *SessionsHandler) handleGet(w http.ResponseWriter, r *http.Request, id string) {
	view, err := h.buildSessionView(r.Context(), id)
	if err != nil {
		h.writeSessionManagerError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, view)
}

func (h *SessionsHandler) handleStop(w http.ResponseWriter, r *http.Request, id string) {
	if err := h.sessions.Stop(r.Context(), id); err != nil {
		h.writeSessionManagerError(w, err)
		return
	}

	view, err := h.buildSessionView(r.Context(), id)
	if err != nil {
		h.writeSessionManagerError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, view)
}

func (h *SessionsHandler) handleRestart(w http.ResponseWriter, r *http.Request, id string) {
	handle, err := h.sessions.Restart(r.Context(), id)
	if err != nil {
		h.writeSessionManagerError(w, err)
		return
	}

	view := toSessionView(*handle, session.HealthStatus{State: session.HealthStateUnknown})
	if health, healthErr := h.sessions.Health(r.Context(), id); healthErr == nil {
		view.Health = health
	}

	writeJSON(w, http.StatusOK, view)
}

func (h *SessionsHandler) handleStart(w http.ResponseWriter, r *http.Request, id string) {
	h.handleRestart(w, r, id)
}

func (h *SessionsHandler) handleDelete(w http.ResponseWriter, r *http.Request, id string) {
	if err := h.sessions.Delete(r.Context(), id); err != nil {
		h.writeSessionManagerError(w, err)
		return
	}

	h.clearAttachments(id)
	w.WriteHeader(http.StatusNoContent)
}

func (h *SessionsHandler) handleAttach(w http.ResponseWriter, r *http.Request, id string) {
	conn, err := h.sessions.AttachTerminal(r.Context(), id)
	if err != nil {
		h.writeSessionManagerError(w, err)
		return
	}
	if conn != nil {
		h.storeAttachment(id, conn)
	}

	view, err := h.buildSessionView(r.Context(), id)
	if err != nil {
		h.writeSessionManagerError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, view)
}

func (h *SessionsHandler) handleDetach(w http.ResponseWriter, r *http.Request, id string) {
	if _, err := h.sessions.Get(id); err != nil {
		h.writeSessionManagerError(w, err)
		return
	}

	if conn, ok := h.popAttachment(id); ok && conn != nil {
		_ = conn.Close()
	}

	view, err := h.buildSessionView(r.Context(), id)
	if err != nil {
		h.writeSessionManagerError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, view)
}

func (h *SessionsHandler) buildSessionView(ctx context.Context, id string) (sessionView, error) {
	handle, err := h.sessions.Get(id)
	if err != nil {
		return sessionView{}, err
	}

	health, err := h.sessions.Health(ctx, id)
	if err != nil {
		return sessionView{}, err
	}

	return toSessionView(*handle, health), nil
}

func (h *SessionsHandler) storeAttachment(id string, conn session.TerminalConn) {
	h.mu.Lock()
	h.attachments[id] = append(h.attachments[id], conn)
	h.mu.Unlock()
}

func (h *SessionsHandler) popAttachment(id string) (session.TerminalConn, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	conns := h.attachments[id]
	if len(conns) == 0 {
		return nil, false
	}

	idx := len(conns) - 1
	conn := conns[idx]
	if idx == 0 {
		delete(h.attachments, id)
	} else {
		h.attachments[id] = conns[:idx]
	}

	return conn, true
}

func (h *SessionsHandler) clearAttachments(id string) {
	h.mu.Lock()
	conns := h.attachments[id]
	delete(h.attachments, id)
	h.mu.Unlock()

	for _, conn := range conns {
		if conn != nil {
			_ = conn.Close()
		}
	}
}

func (h *SessionsHandler) writeSessionManagerError(w http.ResponseWriter, err error) {
	switch errorx.Code(err) {
	case "WORKSPACE_PATH_REQUIRED", "WORKSPACE_PATH_INVALID":
		writeAPIError(w, http.StatusBadRequest, errorx.Message(err), errorx.Code(err))
	case "SESSION_ALREADY_EXISTS", "SESSION_STOPPED":
		writeAPIError(w, http.StatusConflict, errorx.Message(err), errorx.Code(err))
	case "SESSION_NOT_FOUND", "NO_AVAILABLE_SESSION_PORTS", "TERMINAL_ATTACH_UNAVAILABLE", "DAEMON_UNHEALTHY":
		writeAPIError(w, errorx.HTTPStatus(err), errorx.Message(err), errorx.Code(err))
	case "REQUEST_CANCELED", "REQUEST_TIMEOUT":
		writeAPIError(w, errorx.HTTPStatus(err), errorx.Message(err), errorx.Code(err))
	default:
		h.logger.Error("session handler error", "error", err)
		writeAPIError(w, http.StatusInternalServerError, errorx.Message(err), errorx.Code(err))
	}
}

func parseSessionPath(path string) (id string, action string, ok bool) {
	tail := strings.TrimPrefix(path, "/api/sessions/")
	tail = strings.TrimSpace(tail)
	tail = strings.Trim(tail, "/")
	if tail == "" {
		return "", "", false
	}

	parts := strings.Split(tail, "/")
	if len(parts) == 1 {
		if parts[0] == "" {
			return "", "", false
		}
		return parts[0], "", true
	}
	if len(parts) == 2 {
		if parts[0] == "" || parts[1] == "" {
			return "", "", false
		}
		return parts[0], parts[1], true
	}

	return "", "", false
}

func toSessionView(handle session.SessionHandle, health session.HealthStatus) sessionView {
	return sessionView{
		ID:              handle.ID,
		DaemonPort:      handle.DaemonPort,
		WorkspacePath:   handle.WorkspacePath,
		Status:          handle.Status,
		CreatedAt:       handle.CreatedAt.UTC().Format(timeLayoutRFC3339Nano),
		LastActivity:    handle.LastActivity.UTC().Format(timeLayoutRFC3339Nano),
		AttachedClients: handle.AttachedClients,
		Labels:          handle.Labels,
		Health:          health,
	}
}

const timeLayoutRFC3339Nano = "2006-01-02T15:04:05.999999999Z07:00"

func decodeJSONBody(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain a single JSON object")
		}
		return err
	}
	return nil
}

func isValidSessionStatus(status session.SessionStatus) bool {
	switch status {
	case session.SessionStatusUnknown, session.SessionStatusActive, session.SessionStatusIdle, session.SessionStatusStopped, session.SessionStatusError:
		return true
	default:
		return false
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeAPIError(w http.ResponseWriter, status int, message string, code string) {
	writeJSON(w, status, errorResponse{Error: message, Code: code})
}

func (h *SessionsHandler) handleChat(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		Prompt string `json:"prompt"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "INVALID_REQUEST_BODY")
		return
	}

	handle, err := h.sessions.Get(id)
	if err != nil {
		h.writeSessionManagerError(w, err)
		return
	}

	client, err := daemon.NewDaemonClient(fmt.Sprintf("http://127.0.0.1:%d", handle.DaemonPort), daemon.ClientConfig{})
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error(), "DAEMON_CLIENT_ERROR")
		return
	}

	ch, err := client.SendMessage(r.Context(), id, req.Prompt)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error(), "SEND_MESSAGE_ERROR")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "streaming unsupported", "STREAMING_UNSUPPORTED")
		return
	}

	for chunk := range ch {
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}
}

func (h *SessionsHandler) handleChatHistory(w http.ResponseWriter, r *http.Request, id string) {
	handle, err := h.sessions.Get(id)
	if err != nil {
		h.writeSessionManagerError(w, err)
		return
	}

	client, err := daemon.NewDaemonClient(fmt.Sprintf("http://127.0.0.1:%d", handle.DaemonPort), daemon.ClientConfig{})
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error(), "DAEMON_CLIENT_ERROR")
		return
	}

	msgs, err := client.GetMessages(r.Context(), id)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error(), "GET_MESSAGES_ERROR")
		return
	}

	writeJSON(w, http.StatusOK, msgs)
}
