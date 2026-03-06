package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func mustNewClient(t *testing.T, baseURL string, cfg ClientConfig) *Client {
	t.Helper()
	client, err := NewClient(baseURL, cfg)
	if err != nil {
		t.Fatalf("failed to create daemon client: %v", err)
	}
	return client
}

func TestListSessionsPrefersSingularSessionEndpoint(t *testing.T) {
	var singularHits atomic.Int32
	var pluralHits atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) {
		singularHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"sessions": []map[string]interface{}{
				{
					"id":        "ses-1",
					"directory": "/work/proj",
					"time":      "2026-03-05T10:00:00Z",
					"projectID": "proj-1",
					"slug":      "proj",
					"version":   "1.2.17",
				},
			},
		})
	})
	mux.HandleFunc("/sessions", func(w http.ResponseWriter, r *http.Request) {
		pluralHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := mustNewClient(t, server.URL, ClientConfig{Timeout: 2 * time.Second})
	sessions, err := client.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions returned error: %v", err)
	}

	if singularHits.Load() != 1 {
		t.Fatalf("expected singular endpoint hit once, got %d", singularHits.Load())
	}
	if pluralHits.Load() != 0 {
		t.Fatalf("expected plural endpoint to be skipped, got %d hits", pluralHits.Load())
	}

	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].ID != "ses-1" {
		t.Fatalf("expected session id ses-1, got %q", sessions[0].ID)
	}
	if sessions[0].Directory != "/work/proj" {
		t.Fatalf("expected directory /work/proj, got %q", sessions[0].Directory)
	}
	if sessions[0].CreatedAt.IsZero() {
		t.Fatalf("expected CreatedAt parsed from time field")
	}
}

func TestListSessionsFallsBackFromHTMLShellResponse(t *testing.T) {
	var singularHits atomic.Int32
	var pluralHits atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) {
		singularHits.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><body>shell</body></html>"))
	})
	mux.HandleFunc("/sessions", func(w http.ResponseWriter, r *http.Request) {
		pluralHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{{"id": "ses-fallback", "directory": "/tmp/fallback"}})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := mustNewClient(t, server.URL, ClientConfig{Timeout: 2 * time.Second})
	sessions, err := client.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions returned error: %v", err)
	}

	if singularHits.Load() != 1 || pluralHits.Load() != 1 {
		t.Fatalf("expected fallback behavior singular=1 plural=1, got singular=%d plural=%d", singularHits.Load(), pluralHits.Load())
	}
	if len(sessions) != 1 || sessions[0].ID != "ses-fallback" {
		t.Fatalf("unexpected sessions payload: %+v", sessions)
	}
}

func TestGetSessionFallbackAndValidationError(t *testing.T) {
	var singularHits atomic.Int32
	var pluralHits atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/session/ses-42", func(w http.ResponseWriter, r *http.Request) {
		singularHits.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><body>ui shell</body></html>"))
	})
	mux.HandleFunc("/sessions/ses-42", func(w http.ResponseWriter, r *http.Request) {
		pluralHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":        "ses-42",
			"directory": "/work/fallback",
			"slug":      "fallback",
		})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := mustNewClient(t, server.URL, ClientConfig{Timeout: 2 * time.Second})
	session, err := client.GetSession(context.Background(), "ses-42")
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if session.ID != "ses-42" {
		t.Fatalf("expected session id ses-42, got %q", session.ID)
	}
	if singularHits.Load() != 1 || pluralHits.Load() != 1 {
		t.Fatalf("expected fallback hits singular=1 plural=1, got singular=%d plural=%d", singularHits.Load(), pluralHits.Load())
	}

	if _, err := client.GetSession(context.Background(), ""); err == nil {
		t.Fatalf("expected validation error for empty session id")
	}
}

func TestSubscribeEventsParsesMultiLineSSEData(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		_, _ = fmt.Fprint(w, "id: 1\n")
		_, _ = fmt.Fprint(w, "event: message.part.delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message.part.delta\",\"sessionID\":\"ses-1\",\n")
		_, _ = fmt.Fprint(w, "data: \"part\":{\"delta\":\"hel\"}}\n\n")
		flusher.Flush()

		_, _ = fmt.Fprint(w, "id: 2\n")
		_, _ = fmt.Fprint(w, "event: session.updated\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"session.updated\",\"sessionID\":\"ses-1\"}\n\n")
		flusher.Flush()
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := mustNewClient(t, server.URL, ClientConfig{Timeout: 2 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	events, err := client.SubscribeEvents(ctx)
	if err != nil {
		t.Fatalf("SubscribeEvents returned error: %v", err)
	}

	first := <-events
	if first.Type != "message.part.delta" {
		t.Fatalf("expected first type message.part.delta, got %q", first.Type)
	}
	if first.ID != "1" {
		t.Fatalf("expected first id 1, got %q", first.ID)
	}
	if first.SessionID != "ses-1" {
		t.Fatalf("expected first session ses-1, got %q", first.SessionID)
	}
	if first.Delta != "hel" {
		t.Fatalf("expected parsed delta hel, got %q", first.Delta)
	}

	second := <-events
	if second.Type != "session.updated" {
		t.Fatalf("expected second type session.updated, got %q", second.Type)
	}
}

func TestSendMessageStreamsChunksFromEventEndpoint(t *testing.T) {
	startEvents := make(chan struct{})
	postedBody := make(chan MessageRequest, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		<-startEvents

		_, _ = fmt.Fprint(w, "event: message.part.delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message.part.delta\",\"sessionID\":\"other\",\"delta\":\"ignore\"}\n\n")
		flusher.Flush()

		_, _ = fmt.Fprint(w, "event: message.part.delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message.part.delta\",\"sessionID\":\"ses-1\",\"delta\":\"Hel\"}\n\n")
		flusher.Flush()

		_, _ = fmt.Fprint(w, "event: message.part.delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message.part.delta\",\"sessionID\":\"ses-1\",\"part\":{\"delta\":\"lo\"}}\n\n")
		flusher.Flush()

		_, _ = fmt.Fprint(w, "event: session.idle\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"session.idle\",\"sessionID\":\"ses-1\"}\n\n")
		flusher.Flush()
	})

	mux.HandleFunc("/session/ses-1/message", func(w http.ResponseWriter, r *http.Request) {
		defer close(startEvents)
		var req MessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			postedBody <- req
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "msg-1"})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := mustNewClient(t, server.URL, ClientConfig{
		Timeout:           2 * time.Second,
		StreamIdleTimeout: 300 * time.Millisecond,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	chunks, err := client.SendMessage(ctx, "ses-1", "hello")
	if err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}

	posted := <-postedBody
	if len(posted.Parts) != 1 || posted.Parts[0].Text != "hello" {
		t.Fatalf("unexpected posted request body: %+v", posted)
	}

	collected := make([]MessageChunk, 0, 4)
	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				goto done
			}
			collected = append(collected, chunk)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for streamed chunks")
		}
	}

done:
	deltas := make([]string, 0, 2)
	var doneChunk MessageChunk
	for _, chunk := range collected {
		if chunk.Delta != "" {
			deltas = append(deltas, chunk.Delta)
		}
		if chunk.Done {
			doneChunk = chunk
		}
	}

	if strings.Join(deltas, "") != "Hello" {
		t.Fatalf("expected streamed deltas to form Hello, got %q (%+v)", strings.Join(deltas, ""), deltas)
	}
	if !doneChunk.Done {
		t.Fatalf("expected terminal done chunk, got %+v", collected)
	}
	if doneChunk.Type != "session.idle" {
		t.Fatalf("expected done chunk type session.idle, got %q", doneChunk.Type)
	}
}

func TestHealthHonorsTimeout(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/global/health", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(120 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"healthy": true})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := mustNewClient(t, server.URL, ClientConfig{Timeout: 40 * time.Millisecond, MaxRetries: 0})
	start := time.Now()
	_, err := client.Health(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(strings.ToLower(err.Error()), "timeout") {
		t.Fatalf("expected timeout/deadline error, got %v", err)
	}
	if elapsed >= 300*time.Millisecond {
		t.Fatalf("expected timeout to return quickly, elapsed=%s", elapsed)
	}
}

func TestHealthRetriesTransientFailures(t *testing.T) {
	var attempts atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/global/health", func(w http.ResponseWriter, r *http.Request) {
		count := attempts.Add(1)
		if count < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("retry me"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"healthy": true, "version": "1.2.17"})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := mustNewClient(t, server.URL, ClientConfig{
		Timeout:      2 * time.Second,
		MaxRetries:   2,
		RetryBackoff: time.Millisecond,
	})

	health, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("Health returned error: %v", err)
	}
	if !health.Healthy {
		t.Fatalf("expected healthy=true after retries")
	}
	if attempts.Load() != 3 {
		t.Fatalf("expected exactly 3 attempts, got %d", attempts.Load())
	}
}

func TestExecuteCommandFallbackAndValidationError(t *testing.T) {
	var commandHits atomic.Int32
	var commandsHits atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/session/ses-cmd/command", func(w http.ResponseWriter, r *http.Request) {
		commandHits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/session/ses-cmd/commands", func(w http.ResponseWriter, r *http.Request) {
		commandsHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"exit_code": 7, "stderr": "boom", "success": false})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := mustNewClient(t, server.URL, ClientConfig{Timeout: 2 * time.Second})
	result, err := client.ExecuteCommand(context.Background(), "ses-cmd", "ls")
	if err != nil {
		t.Fatalf("ExecuteCommand returned error: %v", err)
	}
	if result.ExitCode != 7 || result.Success {
		t.Fatalf("unexpected command result: %+v", result)
	}
	if commandHits.Load() != 1 || commandsHits.Load() != 1 {
		t.Fatalf("expected fallback hits command=1 commands=1, got command=%d commands=%d", commandHits.Load(), commandsHits.Load())
	}

	if _, err := client.ExecuteCommand(context.Background(), "ses-cmd", ""); err == nil {
		t.Fatalf("expected validation error for empty command")
	}
}

func TestListFilesAndReadFileFallbackPaths(t *testing.T) {
	var singularFilesHits atomic.Int32
	var pluralFilesHits atomic.Int32
	var queryReadHits atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/session/ses-files/file", func(w http.ResponseWriter, r *http.Request) {
		singularFilesHits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/session/ses-files/files", func(w http.ResponseWriter, r *http.Request) {
		pluralFilesHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"files": []map[string]interface{}{{"path": "README.md", "size": 6}}})
	})
	mux.HandleFunc("/session/ses-files/file/README.md", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/session/ses-files/files/README.md", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/file/README.md", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/files/README.md", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/file", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("sessionID") == "ses-files" && r.URL.Query().Get("path") == "README.md" {
			queryReadHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"path": "README.md", "content": "query-read"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := mustNewClient(t, server.URL, ClientConfig{Timeout: 2 * time.Second})
	files, err := client.ListFiles(context.Background(), "ses-files", "*.md")
	if err != nil {
		t.Fatalf("ListFiles returned error: %v", err)
	}
	if len(files) != 1 || files[0].Path != "README.md" {
		t.Fatalf("unexpected files payload: %+v", files)
	}
	if singularFilesHits.Load() != 1 || pluralFilesHits.Load() != 1 {
		t.Fatalf("expected fallback hits file=1 files=1, got file=%d files=%d", singularFilesHits.Load(), pluralFilesHits.Load())
	}

	read, err := client.ReadFile(context.Background(), "ses-files", "README.md")
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if read.Content != "query-read" {
		t.Fatalf("expected query-read content, got %q", read.Content)
	}
	if queryReadHits.Load() != 1 {
		t.Fatalf("expected query-path fallback hit once, got %d", queryReadHits.Load())
	}
}

func TestConfigInvalidPayloadReturnsError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`["bad-shape"]`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := mustNewClient(t, server.URL, ClientConfig{Timeout: 2 * time.Second})
	if _, err := client.Config(context.Background()); err == nil {
		t.Fatalf("expected config shape error")
	}
}

func TestExecuteCommandListFilesReadFileAndConfig(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/session/ses-1/command", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"exit_code": 0, "stdout": "ok", "success": true})
	})
	mux.HandleFunc("/session/ses-1/file", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"files": []map[string]interface{}{{"path": "README.md", "size": 5, "is_dir": false}},
		})
	})
	mux.HandleFunc("/session/ses-1/file/README.md", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello"))
	})
	mux.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"model": "claude", "provider": "anthropic"})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	client := mustNewClient(t, server.URL, ClientConfig{Timeout: 2 * time.Second})
	ctx := context.Background()

	cmd, err := client.ExecuteCommand(ctx, "ses-1", "pwd")
	if err != nil {
		t.Fatalf("ExecuteCommand returned error: %v", err)
	}
	if !cmd.Success || cmd.ExitCode != 0 || cmd.Stdout != "ok" {
		t.Fatalf("unexpected command result: %+v", cmd)
	}

	files, err := client.ListFiles(ctx, "ses-1", "*.md")
	if err != nil {
		t.Fatalf("ListFiles returned error: %v", err)
	}
	if len(files) != 1 || files[0].Path != "README.md" {
		t.Fatalf("unexpected files payload: %+v", files)
	}

	file, err := client.ReadFile(ctx, "ses-1", "README.md")
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if file.Content != "hello" {
		t.Fatalf("expected plain-text file content hello, got %q", file.Content)
	}

	conf, err := client.Config(ctx)
	if err != nil {
		t.Fatalf("Config returned error: %v", err)
	}
	if conf.Raw["model"] != "claude" {
		t.Fatalf("unexpected config payload: %+v", conf)
	}
}
