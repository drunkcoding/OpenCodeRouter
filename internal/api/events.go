package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"opencoderouter/internal/session"
)

const (
	defaultEventsKeepaliveInterval = 15 * time.Second
	defaultEventsRetryInterval     = 5 * time.Second
)

type BackendEvent struct {
	Type      string
	Timestamp time.Time
	SessionID string
	Data      any
}

type BackendEventSubscribeFunc func(ctx context.Context) (<-chan BackendEvent, func(), error)

type EventsHandlerConfig struct {
	SessionEventBus   session.EventBus
	BackendSubscribe  BackendEventSubscribeFunc
	Logger            *slog.Logger
	KeepaliveInterval time.Duration
	RetryInterval     time.Duration
}

type EventsHandler struct {
	sessionEvents    session.EventBus
	backendSubscribe BackendEventSubscribeFunc
	logger           *slog.Logger
	keepalive        time.Duration
	retry            time.Duration
}

type streamEnvelope struct {
	Type      string `json:"type"`
	Source    string `json:"source"`
	Timestamp string `json:"timestamp"`
	SessionID string `json:"sessionId,omitempty"`
	Sequence  int64  `json:"sequence"`
	Payload   any    `json:"payload,omitempty"`
}

func NewEventsHandler(cfg EventsHandlerConfig) *EventsHandler {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	keepalive := cfg.KeepaliveInterval
	if keepalive <= 0 {
		keepalive = defaultEventsKeepaliveInterval
	}

	retry := cfg.RetryInterval
	if retry <= 0 {
		retry = defaultEventsRetryInterval
	}

	return &EventsHandler{
		sessionEvents:    cfg.SessionEventBus,
		backendSubscribe: cfg.BackendSubscribe,
		logger:           logger,
		keepalive:        keepalive,
		retry:            retry,
	}
}

func (h *EventsHandler) Register(mux *http.ServeMux) {
	if h == nil || mux == nil {
		return
	}
	mux.HandleFunc("/api/events", h.handleEvents)
}

func (h *EventsHandler) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	if h.sessionEvents == nil && h.backendSubscribe == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "event stream unavailable", "EVENT_STREAM_UNAVAILABLE")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "streaming unsupported", "STREAMING_UNSUPPORTED")
		return
	}

	var (
		sessionCh          <-chan session.Event
		sessionUnsubscribe func()
		backendCh          <-chan BackendEvent
		backendUnsubscribe func()
	)

	if h.sessionEvents != nil {
		var err error
		sessionCh, sessionUnsubscribe, err = h.sessionEvents.Subscribe(session.EventFilter{Types: []session.EventType{
			session.EventTypeSessionCreated,
			session.EventTypeSessionStopped,
			session.EventTypeSessionHealthChanged,
			session.EventTypeSessionAttached,
			session.EventTypeSessionDetached,
		}})
		if err != nil {
			h.logger.Warn("failed to subscribe to session events", "error", err)
			writeAPIError(w, http.StatusServiceUnavailable, "event stream unavailable", "EVENT_STREAM_UNAVAILABLE")
			return
		}
	}

	if h.backendSubscribe != nil {
		var err error
		backendCh, backendUnsubscribe, err = h.backendSubscribe(r.Context())
		if err != nil {
			if sessionUnsubscribe != nil {
				sessionUnsubscribe()
			}
			h.logger.Warn("failed to subscribe to backend events", "error", err)
			writeAPIError(w, http.StatusServiceUnavailable, "event stream unavailable", "EVENT_STREAM_UNAVAILABLE")
			return
		}
	}

	defer func() {
		if sessionUnsubscribe != nil {
			sessionUnsubscribe()
		}
		if backendUnsubscribe != nil {
			backendUnsubscribe()
		}
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	if err := writeSSERetry(w, flusher, h.retry); err != nil {
		return
	}

	sequence := parseLastEventID(r.Header.Get("Last-Event-ID"))
	ticker := time.NewTicker(h.keepalive)
	defer ticker.Stop()

	for {
		if sessionCh == nil && backendCh == nil {
			return
		}

		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-sessionCh:
			if !ok {
				sessionCh = nil
				continue
			}

			sequence++
			envelope := newSessionEnvelope(sequence, ev)
			if err := writeSSEJSON(w, flusher, sequence, envelope.Type, envelope); err != nil {
				return
			}
		case ev, ok := <-backendCh:
			if !ok {
				backendCh = nil
				continue
			}

			sequence++
			envelope := newBackendEnvelope(sequence, ev)
			if err := writeSSEJSON(w, flusher, sequence, envelope.Type, envelope); err != nil {
				return
			}
		case <-ticker.C:
			if err := writeSSEComment(w, flusher, "keepalive"); err != nil {
				return
			}
		}
	}
}

func newSessionEnvelope(sequence int64, ev session.Event) streamEnvelope {
	timestamp := ev.Timestamp().UTC()
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}

	eventType := string(ev.Type())
	if ev.Type() == session.EventTypeSessionHealthChanged {
		eventType = "session.health"
	}

	return streamEnvelope{
		Type:      eventType,
		Source:    "session",
		Timestamp: timestamp.Format(timeLayoutRFC3339Nano),
		SessionID: ev.SessionID(),
		Sequence:  sequence,
		Payload:   ev,
	}
}

func newBackendEnvelope(sequence int64, ev BackendEvent) streamEnvelope {
	timestamp := ev.Timestamp.UTC()
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}

	eventType := strings.TrimSpace(ev.Type)
	if eventType == "" {
		eventType = "backend.event"
	}

	return streamEnvelope{
		Type:      eventType,
		Source:    "backend",
		Timestamp: timestamp.Format(timeLayoutRFC3339Nano),
		SessionID: strings.TrimSpace(ev.SessionID),
		Sequence:  sequence,
		Payload:   ev.Data,
	}
}

func parseLastEventID(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}

	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 0 {
		return 0
	}

	return id
}

func writeSSERetry(w io.Writer, flusher http.Flusher, retry time.Duration) error {
	if retry <= 0 {
		retry = defaultEventsRetryInterval
	}
	if _, err := io.WriteString(w, "retry: "+strconv.FormatInt(retry.Milliseconds(), 10)+"\n\n"); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeSSEComment(w io.Writer, flusher http.Flusher, comment string) error {
	comment = strings.ReplaceAll(comment, "\n", " ")
	comment = strings.ReplaceAll(comment, "\r", " ")
	if _, err := io.WriteString(w, ": "+comment+"\n\n"); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeSSEJSON(w io.Writer, flusher http.Flusher, id int64, event string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	if _, err := io.WriteString(w, "id: "+strconv.FormatInt(id, 10)+"\n"); err != nil {
		return err
	}

	event = strings.ReplaceAll(event, "\n", " ")
	event = strings.ReplaceAll(event, "\r", " ")
	if _, err := io.WriteString(w, "event: "+event+"\n"); err != nil {
		return err
	}

	if _, err := io.WriteString(w, "data: "+string(encoded)+"\n\n"); err != nil {
		return err
	}

	flusher.Flush()
	return nil
}
