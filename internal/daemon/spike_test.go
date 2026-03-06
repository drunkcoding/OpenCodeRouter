package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	spikeStartupTimeout = 15 * time.Second
	spikeHTTPTimeout    = 30 * time.Second
)

type spikeDaemon struct {
	baseURL string
	client  *http.Client
}

type openAPIDoc struct {
	OpenAPI string                            `json:"openapi"`
	Paths   map[string]map[string]interface{} `json:"paths"`
}

func requireSpikeDaemon(t *testing.T) *spikeDaemon {
	t.Helper()

	binaryPath, err := exec.LookPath("opencode")
	if err != nil {
		t.Skipf("spike skipped: opencode binary not available in PATH: %v", err)
	}

	port, err := reservePort()
	if err != nil {
		t.Skipf("spike skipped: unable to reserve local port: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, binaryPath, "serve", "--port", strconv.Itoa(port))
	cmd.Dir = moduleRoot(t)

	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		cancel()
		t.Skipf("spike skipped: failed to start opencode serve: %v", err)
	}

	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 1200 * time.Millisecond}

	deadline := time.Now().Add(spikeStartupTimeout)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/global/health", nil)
		resp, err := client.Do(req)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				var health struct {
					Healthy bool `json:"healthy"`
				}
				if json.Unmarshal(body, &health) == nil && health.Healthy {
					return &spikeDaemon{
						baseURL: baseURL,
						client:  &http.Client{Timeout: spikeHTTPTimeout},
					}
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}

	t.Skipf(
		"spike skipped: opencode serve did not become healthy at %s within %s; stderr=%q",
		baseURL,
		spikeStartupTimeout,
		trimForLog(stderr.String(), 400),
	)
	return nil
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to resolve caller for module root")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func reservePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected address type %T", ln.Addr())
	}
	return addr.Port, nil
}

func trimForLog(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func mustCreateSession(t *testing.T, d *spikeDaemon) map[string]interface{} {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, d.baseURL+"/session", nil)
	if err != nil {
		t.Fatalf("create-session request build failed: %v", err)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		t.Fatalf("create-session request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("create-session response read failed: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create-session unexpected status=%d body=%s", resp.StatusCode, string(body))
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("create-session response is not JSON: %v; body=%s", err, string(body))
	}

	if stringField(payload, "id") == "" {
		t.Fatalf("create-session response missing id field: %v", payload)
	}

	return payload
}

func stringField(payload map[string]interface{}, key string) string {
	v, ok := payload[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

func hasAnyKey(payload map[string]interface{}, keys ...string) bool {
	for _, key := range keys {
		if _, ok := payload[key]; ok {
			return true
		}
	}
	return false
}

func waitForEventDataMatch(ctx context.Context, d *spikeDaemon, match func(data string) bool) (matched bool, dataLines []string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.baseURL+"/event", nil)
	if err != nil {
		return false, nil, err
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := d.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, nil, nil
		}
		return false, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return false, nil, fmt.Errorf("event stream status=%d body=%s", resp.StatusCode, string(body))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if data == "" {
			continue
		}
		dataLines = append(dataLines, data)
		if match(data) {
			return true, dataLines, nil
		}
	}

	if scanErr := scanner.Err(); scanErr != nil && ctx.Err() == nil {
		return false, dataLines, scanErr
	}

	return false, dataLines, nil
}

func patchSessionTitle(t *testing.T, d *spikeDaemon, sessionID, title string) {
	t.Helper()
	body := strings.NewReader(fmt.Sprintf(`{"title":%q}`, title))
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPatch, d.baseURL+"/session/"+sessionID, body)
	if err != nil {
		t.Fatalf("patch-session request build failed: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		t.Fatalf("patch-session request failed: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch-session unexpected status=%d body=%s", resp.StatusCode, string(bodyBytes))
	}
}

func TestSpikeDocEndpointOpenAPI(t *testing.T) {
	d := requireSpikeDaemon(t)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, d.baseURL+"/doc", nil)
	if err != nil {
		t.Fatalf("request build failed: %v", err)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		t.Fatalf("GET /doc failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		t.Fatalf("GET /doc unexpected status=%d body=%s", resp.StatusCode, string(body))
	}

	var spec openAPIDoc
	if err := json.NewDecoder(resp.Body).Decode(&spec); err != nil {
		t.Fatalf("GET /doc returned non-JSON payload: %v", err)
	}

	if spec.OpenAPI == "" {
		t.Fatalf("openapi version missing in /doc payload")
	}

	if len(spec.Paths) == 0 {
		t.Fatalf("/doc paths section is empty")
	}

	requiredPaths := []string{
		"/event",
		"/project/current",
		"/session",
		"/session/{sessionID}",
		"/session/{sessionID}/message",
	}

	for _, path := range requiredPaths {
		if _, ok := spec.Paths[path]; !ok {
			t.Fatalf("required path missing from /doc: %s", path)
		}
	}

	t.Logf("/doc openapi=%s path_count=%d", spec.OpenAPI, len(spec.Paths))
}

func TestSpikeCreateSessionShape(t *testing.T) {
	d := requireSpikeDaemon(t)
	payload := mustCreateSession(t, d)

	id := stringField(payload, "id")
	directory := stringField(payload, "directory")

	if id == "" {
		t.Fatalf("session create response missing id: %v", payload)
	}
	if directory == "" {
		t.Fatalf("session create response missing directory: %v", payload)
	}

	shape := map[string]bool{
		"id":               hasAnyKey(payload, "id"),
		"daemon_port":      hasAnyKey(payload, "daemonPort", "daemon_port", "port"),
		"workspace_path":   hasAnyKey(payload, "workspacePath", "workspace_path", "directory"),
		"status":           hasAnyKey(payload, "status"),
		"created_at":       hasAnyKey(payload, "createdAt", "created_at", "time"),
		"last_activity":    hasAnyKey(payload, "lastActivity", "last_activity"),
		"attached_clients": hasAnyKey(payload, "attachedClients", "attached_clients"),
	}

	t.Logf("create-session field coverage=%v", shape)
}

func TestSpikeSessionMessagesEndpoints(t *testing.T) {
	d := requireSpikeDaemon(t)
	payload := mustCreateSession(t, d)
	sessionID := stringField(payload, "id")

	reqPlural, err := http.NewRequestWithContext(context.Background(), http.MethodGet, d.baseURL+"/session/"+sessionID+"/messages", nil)
	if err != nil {
		t.Fatalf("plural endpoint request build failed: %v", err)
	}
	reqPlural.Header.Set("Accept", "text/event-stream")

	respPlural, err := d.client.Do(reqPlural)
	if err != nil {
		t.Fatalf("GET /session/{id}/messages failed: %v", err)
	}
	pluralBody, _ := io.ReadAll(io.LimitReader(respPlural.Body, 2048))
	_ = respPlural.Body.Close()

	pluralContentType := respPlural.Header.Get("Content-Type")
	t.Logf("plural messages endpoint status=%d content-type=%q body-prefix=%q", respPlural.StatusCode, pluralContentType, trimForLog(string(pluralBody), 220))

	reqSingular, err := http.NewRequestWithContext(context.Background(), http.MethodGet, d.baseURL+"/session/"+sessionID+"/message", nil)
	if err != nil {
		t.Fatalf("singular endpoint request build failed: %v", err)
	}

	respSingular, err := d.client.Do(reqSingular)
	if err != nil {
		t.Fatalf("GET /session/{id}/message failed: %v", err)
	}
	defer respSingular.Body.Close()

	if respSingular.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(respSingular.Body, 2048))
		t.Fatalf("GET /session/{id}/message unexpected status=%d body=%s", respSingular.StatusCode, string(body))
	}

	var messages []interface{}
	if err := json.NewDecoder(respSingular.Body).Decode(&messages); err != nil {
		t.Fatalf("GET /session/{id}/message did not return JSON list: %v", err)
	}

	t.Logf("singular message endpoint returned %d entries", len(messages))
}

func TestSpikePostMessageAndTokenEvents(t *testing.T) {
	d := requireSpikeDaemon(t)
	payload := mustCreateSession(t, d)
	sessionID := stringField(payload, "id")

	eventCtx, cancelEvents := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelEvents()

	type eventResult struct {
		matched bool
		lines   []string
		err     error
	}

	eventCh := make(chan eventResult, 1)
	go func() {
		matched, lines, err := waitForEventDataMatch(eventCtx, d, func(data string) bool {
			return strings.Contains(data, sessionID) && strings.Contains(data, `"message.part.delta"`)
		})
		eventCh <- eventResult{matched: matched, lines: lines, err: err}
	}()

	time.Sleep(600 * time.Millisecond)

	body := strings.NewReader(`{"parts":[{"type":"text","text":"Reply with exactly: spike-pong"}]}`)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, d.baseURL+"/session/"+sessionID+"/message", body)
	if err != nil {
		t.Fatalf("POST /session/{id}/message request build failed: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		t.Skipf("spike skipped token assertion: prompt call failed (%v)", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /session/{id}/message unexpected status=%d body=%s", resp.StatusCode, trimForLog(string(respBody), 400))
	}

	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("POST /session/{id}/message expected application/json response, got %q", ct)
	}

	var messagePayload map[string]interface{}
	if err := json.Unmarshal(respBody, &messagePayload); err != nil {
		t.Fatalf("POST /session/{id}/message response was not JSON: %v", err)
	}

	result := <-eventCh
	if result.err != nil {
		t.Skipf("spike skipped token assertion: event stream error: %v", result.err)
	}
	if !result.matched {
		t.Skipf("spike skipped token assertion: no message.part.delta event observed for session %s within timeout (events=%d)", sessionID, len(result.lines))
	}

	t.Logf("observed message.part.delta events for session=%s (total_event_data_lines=%d)", sessionID, len(result.lines))
}

func TestSpikeEventEndpointReceivesSessionUpdates(t *testing.T) {
	d := requireSpikeDaemon(t)
	payload := mustCreateSession(t, d)
	sessionID := stringField(payload, "id")

	eventCtx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	type eventResult struct {
		matched bool
		lines   []string
		err     error
	}

	eventCh := make(chan eventResult, 1)
	go func() {
		matched, lines, err := waitForEventDataMatch(eventCtx, d, func(data string) bool {
			return strings.Contains(data, sessionID) && strings.Contains(data, `"session.updated"`)
		})
		eventCh <- eventResult{matched: matched, lines: lines, err: err}
	}()

	time.Sleep(500 * time.Millisecond)
	patchSessionTitle(t, d, sessionID, "spike-event-update")

	result := <-eventCh
	if result.err != nil {
		t.Fatalf("event stream failed: %v", result.err)
	}
	if !result.matched {
		t.Fatalf("expected session.updated event for session %s; received %d data lines", sessionID, len(result.lines))
	}

	t.Logf("event stream delivered session.updated for session=%s", sessionID)
}

func TestSpikeMultiClientEventStreams(t *testing.T) {
	d := requireSpikeDaemon(t)
	payload := mustCreateSession(t, d)
	sessionID := stringField(payload, "id")

	ctx1, cancel1 := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel1()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel2()

	type eventResult struct {
		matched bool
		lines   []string
		err     error
	}

	stream1 := make(chan eventResult, 1)
	stream2 := make(chan eventResult, 1)

	go func() {
		matched, lines, err := waitForEventDataMatch(ctx1, d, func(data string) bool {
			return strings.Contains(data, sessionID) && strings.Contains(data, `"session.updated"`)
		})
		stream1 <- eventResult{matched: matched, lines: lines, err: err}
	}()

	go func() {
		matched, lines, err := waitForEventDataMatch(ctx2, d, func(data string) bool {
			return strings.Contains(data, sessionID) && strings.Contains(data, `"session.updated"`)
		})
		stream2 <- eventResult{matched: matched, lines: lines, err: err}
	}()

	time.Sleep(900 * time.Millisecond)
	patchSessionTitle(t, d, sessionID, "spike-multi-client")

	res1 := <-stream1
	res2 := <-stream2

	if res1.err != nil {
		t.Fatalf("event stream client #1 failed: %v", res1.err)
	}
	if res2.err != nil {
		t.Fatalf("event stream client #2 failed: %v", res2.err)
	}
	if !res1.matched || !res2.matched {
		t.Fatalf("expected both event clients to receive session update: client1=%t client2=%t", res1.matched, res2.matched)
	}

	t.Logf("both event clients observed session.updated for session=%s", sessionID)
}

func TestSpikeSessionDetailFields(t *testing.T) {
	d := requireSpikeDaemon(t)
	payload := mustCreateSession(t, d)
	sessionID := stringField(payload, "id")

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, d.baseURL+"/session/"+sessionID, nil)
	if err != nil {
		t.Fatalf("GET /session/{id} request build failed: %v", err)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		t.Fatalf("GET /session/{id} request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		t.Fatalf("GET /session/{id} unexpected status=%d body=%s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET /session/{id} read failed: %v", err)
	}

	var sessionPayload map[string]interface{}
	if err := json.Unmarshal(body, &sessionPayload); err != nil {
		t.Fatalf("GET /session/{id} response was not JSON: %v", err)
	}

	if stringField(sessionPayload, "id") == "" {
		t.Fatalf("GET /session/{id} response missing id: %v", sessionPayload)
	}

	hasWorkingDirectory := hasAnyKey(sessionPayload, "directory", "worktree", "cwd")
	hasFiles := hasAnyKey(sessionPayload, "files", "fileList", "file_list")
	hasAgent := hasAnyKey(sessionPayload, "agent", "agents", "agentInfo", "agent_info")

	t.Logf("session detail keys=%v", sortedKeys(sessionPayload))
	t.Logf("session detail capability working_directory=%t files=%t agent=%t", hasWorkingDirectory, hasFiles, hasAgent)
}

func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	for i := 0; i < len(keys)-1; i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}
