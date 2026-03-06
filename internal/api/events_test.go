package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"opencoderouter/internal/session"
)

type parsedSSEFrame struct {
	ID       string
	Event    string
	Data     []string
	Comments []string
	Retry    string
}

type parsedStreamEnvelope struct {
	Type      string `json:"type"`
	Source    string `json:"source"`
	Timestamp string `json:"timestamp"`
	SessionID string `json:"sessionId"`
	Sequence  int64  `json:"sequence"`
}

func TestEventsHandlerStreamsSessionEventsAndKeepalive(t *testing.T) {
	eventBus := session.NewEventBus(16)

	mux := http.NewServeMux()
	NewEventsHandler(EventsHandlerConfig{
		SessionEventBus:   eventBus,
		KeepaliveInterval: 20 * time.Millisecond,
		RetryInterval:     10 * time.Millisecond,
	}).Register(mux)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/events", nil)
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		t.Fatalf("status=%d want=%d", resp.StatusCode, http.StatusOK)
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		defer resp.Body.Close()
		t.Fatalf("content-type=%q want prefix text/event-stream", contentType)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	retryFrame := readSSEUntil(t, reader, 2*time.Second, func(frame parsedSSEFrame) bool {
		return frame.Retry != ""
	})
	if retryFrame.Retry == "" {
		t.Fatal("expected retry frame")
	}

	now := time.Now().UTC()
	handle := session.SessionHandle{ID: "s-created", DaemonPort: 30123, WorkspacePath: "/tmp/work", Status: session.SessionStatusActive, CreatedAt: now, LastActivity: now}
	if err := eventBus.Publish(session.SessionCreated{At: now, Session: handle}); err != nil {
		t.Fatalf("publish session.created: %v", err)
	}

	createdFrame := readSSEUntil(t, reader, 2*time.Second, func(frame parsedSSEFrame) bool {
		return frame.Event == "session.created" && len(frame.Data) > 0
	})
	if createdFrame.ID != "1" {
		t.Fatalf("created id=%q want=1", createdFrame.ID)
	}

	var createdPayload parsedStreamEnvelope
	decodeSSEDataJSON(t, createdFrame, &createdPayload)
	if createdPayload.Type != "session.created" {
		t.Fatalf("created type=%q want=session.created", createdPayload.Type)
	}
	if createdPayload.Source != "session" {
		t.Fatalf("created source=%q want=session", createdPayload.Source)
	}
	if createdPayload.SessionID != "s-created" {
		t.Fatalf("created sessionId=%q want=s-created", createdPayload.SessionID)
	}
	if createdPayload.Sequence != 1 {
		t.Fatalf("created sequence=%d want=1", createdPayload.Sequence)
	}

	if err := eventBus.Publish(session.SessionHealthChanged{
		At:       now.Add(2 * time.Second),
		Session:  handle,
		Previous: session.HealthStatus{State: session.HealthStateHealthy},
		Current:  session.HealthStatus{State: session.HealthStateUnhealthy, Error: "probe timeout"},
	}); err != nil {
		t.Fatalf("publish session.health: %v", err)
	}

	healthFrame := readSSEUntil(t, reader, 2*time.Second, func(frame parsedSSEFrame) bool {
		return frame.Event == "session.health" && len(frame.Data) > 0
	})
	if healthFrame.ID != "2" {
		t.Fatalf("health id=%q want=2", healthFrame.ID)
	}

	var healthPayload parsedStreamEnvelope
	decodeSSEDataJSON(t, healthFrame, &healthPayload)
	if healthPayload.Type != "session.health" {
		t.Fatalf("health type=%q want=session.health", healthPayload.Type)
	}
	if healthPayload.Sequence != 2 {
		t.Fatalf("health sequence=%d want=2", healthPayload.Sequence)
	}

	keepaliveFrame := readSSEUntil(t, reader, 2*time.Second, func(frame parsedSSEFrame) bool {
		for _, comment := range frame.Comments {
			if strings.Contains(comment, "keepalive") {
				return true
			}
		}
		return false
	})
	if len(keepaliveFrame.Comments) == 0 {
		t.Fatal("expected keepalive comment frame")
	}
}

func TestEventsHandlerAppliesLastEventIDSequencing(t *testing.T) {
	eventBus := session.NewEventBus(16)

	mux := http.NewServeMux()
	NewEventsHandler(EventsHandlerConfig{SessionEventBus: eventBus}).Register(mux)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Last-Event-ID", "41")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		t.Fatalf("status=%d want=%d", resp.StatusCode, http.StatusOK)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	_ = readSSEUntil(t, reader, 2*time.Second, func(frame parsedSSEFrame) bool {
		return frame.Retry != ""
	})

	now := time.Now().UTC()
	handle := session.SessionHandle{ID: "s-stopped", DaemonPort: 30124, WorkspacePath: "/tmp/work", Status: session.SessionStatusStopped, CreatedAt: now, LastActivity: now}
	if err := eventBus.Publish(session.SessionStopped{At: now, Session: handle, Reason: "user"}); err != nil {
		t.Fatalf("publish session.stopped: %v", err)
	}

	stoppedFrame := readSSEUntil(t, reader, 2*time.Second, func(frame parsedSSEFrame) bool {
		return frame.Event == "session.stopped" && len(frame.Data) > 0
	})
	if stoppedFrame.ID != "42" {
		t.Fatalf("stopped id=%q want=42", stoppedFrame.ID)
	}

	var stoppedPayload parsedStreamEnvelope
	decodeSSEDataJSON(t, stoppedFrame, &stoppedPayload)
	if stoppedPayload.Sequence != 42 {
		t.Fatalf("stopped sequence=%d want=42", stoppedPayload.Sequence)
	}
}

func TestEventsHandlerStreamsBackendEventsWhenAvailable(t *testing.T) {
	backendEvents := make(chan BackendEvent, 4)

	mux := http.NewServeMux()
	NewEventsHandler(EventsHandlerConfig{
		BackendSubscribe: func(_ context.Context) (<-chan BackendEvent, func(), error) {
			return backendEvents, func() {}, nil
		},
	}).Register(mux)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp := doJSONRequest(t, srv.Client(), http.MethodGet, srv.URL+"/api/events", nil)
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		t.Fatalf("status=%d want=%d", resp.StatusCode, http.StatusOK)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	_ = readSSEUntil(t, reader, 2*time.Second, func(frame parsedSSEFrame) bool {
		return frame.Retry != ""
	})

	backendEvents <- BackendEvent{
		Type:      "backend.updated",
		Timestamp: time.Now().UTC(),
		Data:      map[string]any{"slug": "proj-a", "port": 32000},
	}

	backendFrame := readSSEUntil(t, reader, 2*time.Second, func(frame parsedSSEFrame) bool {
		return frame.Event == "backend.updated" && len(frame.Data) > 0
	})
	if backendFrame.ID != "1" {
		t.Fatalf("backend id=%q want=1", backendFrame.ID)
	}

	var backendPayload parsedStreamEnvelope
	decodeSSEDataJSON(t, backendFrame, &backendPayload)
	if backendPayload.Source != "backend" {
		t.Fatalf("backend source=%q want=backend", backendPayload.Source)
	}
	if backendPayload.Type != "backend.updated" {
		t.Fatalf("backend type=%q want=backend.updated", backendPayload.Type)
	}
	if backendPayload.Sequence != 1 {
		t.Fatalf("backend sequence=%d want=1", backendPayload.Sequence)
	}
}

func readSSEUntil(t *testing.T, reader *bufio.Reader, timeout time.Duration, match func(parsedSSEFrame) bool) parsedSSEFrame {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("timed out waiting for matching SSE frame after %s", timeout)
		}
		frame := readSSEFrame(t, reader, remaining)
		if match(frame) {
			return frame
		}
	}
}

func readSSEFrame(t *testing.T, reader *bufio.Reader, timeout time.Duration) parsedSSEFrame {
	t.Helper()
	type result struct {
		frame parsedSSEFrame
		err   error
	}
	resultCh := make(chan result, 1)

	go func() {
		var frame parsedSSEFrame
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				resultCh <- result{err: err}
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if frame.ID != "" || frame.Event != "" || frame.Retry != "" || len(frame.Data) > 0 || len(frame.Comments) > 0 {
					resultCh <- result{frame: frame}
					return
				}
				continue
			}

			switch {
			case strings.HasPrefix(line, "id:"):
				frame.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
			case strings.HasPrefix(line, "event:"):
				frame.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				frame.Data = append(frame.Data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			case strings.HasPrefix(line, "retry:"):
				frame.Retry = strings.TrimSpace(strings.TrimPrefix(line, "retry:"))
			case strings.HasPrefix(line, ":"):
				frame.Comments = append(frame.Comments, strings.TrimSpace(strings.TrimPrefix(line, ":")))
			}
		}
	}()

	select {
	case res := <-resultCh:
		if res.err != nil {
			if res.err == io.EOF {
				t.Fatal("unexpected EOF while reading SSE frame")
			}
			t.Fatalf("read SSE frame: %v", res.err)
		}
		return res.frame
	case <-time.After(timeout):
		t.Fatalf("timed out reading SSE frame after %s", timeout)
	}

	return parsedSSEFrame{}
}

func decodeSSEDataJSON(t *testing.T, frame parsedSSEFrame, dst any) {
	t.Helper()
	if len(frame.Data) == 0 {
		t.Fatal("expected SSE data lines")
	}
	joined := strings.Join(frame.Data, "\n")
	if err := json.Unmarshal([]byte(joined), dst); err != nil {
		t.Fatalf("decode SSE data %q: %v", joined, err)
	}
}

func TestEventsHandlerRejectsUnsupportedMethod(t *testing.T) {
	eventBus := session.NewEventBus(4)

	mux := http.NewServeMux()
	NewEventsHandler(EventsHandlerConfig{SessionEventBus: eventBus}).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp := doJSONRequest(t, srv.Client(), http.MethodPost, srv.URL+"/api/events", nil)
	assertErrorShape(t, resp, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
}

func TestEventsHandlerParsesInvalidLastEventIDAsZero(t *testing.T) {
	eventBus := session.NewEventBus(8)

	mux := http.NewServeMux()
	NewEventsHandler(EventsHandlerConfig{SessionEventBus: eventBus}).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Last-Event-ID", "nonsense")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		t.Fatalf("status=%d want=%d", resp.StatusCode, http.StatusOK)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	_ = readSSEUntil(t, reader, 2*time.Second, func(frame parsedSSEFrame) bool {
		return frame.Retry != ""
	})

	now := time.Now().UTC()
	handle := session.SessionHandle{ID: "s-invalid-last-id", DaemonPort: 30125, WorkspacePath: "/tmp/work", Status: session.SessionStatusActive, CreatedAt: now, LastActivity: now}
	if err := eventBus.Publish(session.SessionAttached{At: now, Session: handle, AttachedClients: 1, ClientID: "c-1"}); err != nil {
		t.Fatalf("publish session.attached: %v", err)
	}

	attachedFrame := readSSEUntil(t, reader, 2*time.Second, func(frame parsedSSEFrame) bool {
		return frame.Event == "session.attached" && len(frame.Data) > 0
	})
	if attachedFrame.ID != strconv.FormatInt(1, 10) {
		t.Fatalf("attached id=%q want=1", attachedFrame.ID)
	}
}
