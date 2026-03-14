package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultClientTimeout      = 15 * time.Second
	defaultRetryBackoff       = 150 * time.Millisecond
	defaultStreamBuffer       = 64
	defaultStreamIdleTimeout  = 2 * time.Second
	defaultScannerInitialSize = 64 * 1024
	defaultScannerMaxSize     = 1024 * 1024
)

type Client struct {
	baseURL    string
	config     ClientConfig
	httpClient *http.Client
}

type DaemonClient = Client

type endpointCandidate struct {
	Path  string
	Query url.Values
}

type httpResult struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

type sseFrame struct {
	ID    string
	Event string
	Data  string
}

type postResult struct {
	payload map[string]interface{}
	err     error
}

func NewClient(baseURL string, cfg ClientConfig) (*Client, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil, errors.New("base URL is required")
	}
	baseURL = strings.TrimRight(baseURL, "/")

	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultClientTimeout
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.RetryBackoff <= 0 {
		cfg.RetryBackoff = defaultRetryBackoff
	}
	if cfg.StreamBuffer <= 0 {
		cfg.StreamBuffer = defaultStreamBuffer
	}
	if cfg.StreamIdleTimeout <= 0 {
		cfg.StreamIdleTimeout = defaultStreamIdleTimeout
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	} else {
		cloned := *httpClient
		httpClient = &cloned
	}
	httpClient.Timeout = cfg.Timeout

	return &Client{
		baseURL:    baseURL,
		config:     cfg,
		httpClient: httpClient,
	}, nil
}

func NewDaemonClient(baseURL string, cfg ClientConfig) (*DaemonClient, error) {
	return NewClient(baseURL, cfg)
}

func (c *Client) ListSessions(ctx context.Context) ([]DaemonSession, error) {
	candidates := []endpointCandidate{
		{Path: "/session"},
		{Path: "/sessions"},
	}

	payload, endpoint, err := c.getJSONFromCandidates(ctx, candidates)
	if err != nil {
		return nil, fmt.Errorf("list sessions failed: %w", err)
	}

	sessions, ok := parseSessionListPayload(payload)
	if !ok {
		return nil, fmt.Errorf("list sessions failed: unsupported payload from %s", endpoint)
	}

	return sessions, nil
}

func (c *Client) GetSession(ctx context.Context, sessionID string) (DaemonSession, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return DaemonSession{}, errors.New("session ID is required")
	}

	id := url.PathEscape(sessionID)
	candidates := []endpointCandidate{
		{Path: "/session/" + id},
		{Path: "/sessions/" + id},
	}

	payload, endpoint, err := c.getJSONFromCandidates(ctx, candidates)
	if err != nil {
		return DaemonSession{}, fmt.Errorf("get session failed: %w", err)
	}

	obj, ok := payload.(map[string]interface{})
	if !ok {
		return DaemonSession{}, fmt.Errorf("get session failed: non-object payload from %s", endpoint)
	}

	session := parseSessionEntry(obj)
	if session.ID == "" {
		session.ID = sessionID
	}
	if session.ID == "" {
		return DaemonSession{}, fmt.Errorf("get session failed: missing session id in payload from %s", endpoint)
	}

	return session, nil
}

func (c *Client) GetMessages(ctx context.Context, sessionID string) ([]map[string]interface{}, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session ID is required")
	}

	id := url.PathEscape(sessionID)
	candidates := []endpointCandidate{
		{Path: "/session/" + id + "/message"},
		{Path: "/sessions/" + id + "/messages"},
	}

	payload, endpoint, err := c.getJSONFromCandidates(ctx, candidates)
	if err != nil {
		return nil, fmt.Errorf("get messages failed: %w", err)
	}

	arr, ok := payload.([]interface{})
	if !ok {
		return nil, fmt.Errorf("get messages failed: non-array payload from %s", endpoint)
	}

	var msgs []map[string]interface{}
	for _, item := range arr {
		if m, ok := item.(map[string]interface{}); ok {
			msgs = append(msgs, m)
		}
	}
	return msgs, nil
}

func (c *Client) SendMessage(ctx context.Context, sessionID, prompt string) (<-chan MessageChunk, error) {
	sessionID = strings.TrimSpace(sessionID)
	prompt = strings.TrimSpace(prompt)
	if sessionID == "" {
		return nil, errors.New("session ID is required")
	}
	if prompt == "" {
		return nil, errors.New("prompt is required")
	}

	streamCtx, cancelStream := context.WithCancel(ctx)
	events, err := c.subscribeEventsInternal(streamCtx)
	if err != nil {
		cancelStream()
		fmt.Printf("subscribeEventsInternal failed: %v\n", err)
		return c.sendMessageWithoutStream(ctx, sessionID, prompt), nil
	}

	out := make(chan MessageChunk, c.config.StreamBuffer)

	go func() {
		defer close(out)
		defer cancelStream()

		postCh := make(chan postResult, 1)
		go func() {
			payload, postErr := c.postMessage(ctx, sessionID, prompt)
			postCh <- postResult{payload: payload, err: postErr}
			close(postCh)
		}()

		var (
			postDone bool
			sawDelta bool
			idleCh   <-chan time.Time
			idleT    *time.Timer
			pending  string
		)

		resetIdle := func() {
			if !postDone || c.config.StreamIdleTimeout <= 0 {
				return
			}
			if idleT == nil {
				idleT = time.NewTimer(c.config.StreamIdleTimeout)
				idleCh = idleT.C
				return
			}
			if !idleT.Stop() {
				select {
				case <-idleT.C:
				default:
				}
			}
			idleT.Reset(c.config.StreamIdleTimeout)
		}

		stopIdle := func() {
			if idleT == nil {
				return
			}
			if !idleT.Stop() {
				select {
				case <-idleT.C:
				default:
				}
			}
			idleCh = nil
		}

		emit := func(chunk MessageChunk) bool {
			select {
			case out <- chunk:
				return true
			case <-ctx.Done():
				return false
			}
		}

		for {
			select {
			case <-ctx.Done():
				stopIdle()
				return
			case res, ok := <-postCh:
				if !ok {
					postCh = nil
					continue
				}
				postCh = nil
				postDone = true
				if res.err != nil {
					emit(MessageChunk{SessionID: sessionID, Type: "error", Error: res.err.Error(), Done: true})
					stopIdle()
					return
				}
				if !sawDelta {
					pending = extractMessageText(res.payload)
					if pending == "" {
						if encoded, marshalErr := json.Marshal(res.payload); marshalErr == nil {
							pending = strings.TrimSpace(string(encoded))
						}
					}
				}
				resetIdle()
			case ev, ok := <-events:
				if !ok {
					if sawDelta {
						emit(MessageChunk{SessionID: sessionID, Type: "stream.closed", Done: true})
					} else if postDone && pending != "" {
						emit(MessageChunk{SessionID: sessionID, Type: "message.final", Delta: pending, Done: true})
					} else if postDone {
						emit(MessageChunk{SessionID: sessionID, Type: "stream.closed", Done: true})
					}
					stopIdle()
					return
				}

				if ev.Error != "" {
					emit(MessageChunk{SessionID: sessionID, Type: "stream.error", Error: ev.Error, Done: true})
					stopIdle()
					return
				}

				if !eventMatchesSession(ev, sessionID) {
					continue
				}

				if isDeltaEvent(ev) {
					sawDelta = true
					pending = ""
					if !emit(MessageChunk{
						SessionID: ev.SessionID,
						MessageID: ev.MessageID,
						Type:      ev.Type,
						Delta:     ev.Delta,
						Timestamp: ev.Timestamp,
						RawData:   ev.RawData,
						Payload:   ev.Payload,
					}) {
						stopIdle()
						return
					}
					resetIdle()
				}

				if isTerminalEvent(ev.Type) && (postDone || sawDelta) {
					if !sawDelta && pending != "" {
						emit(MessageChunk{
							SessionID: sessionID,
							Type:      "message.final",
							Delta:     pending,
							Done:      true,
							Timestamp: ev.Timestamp,
							RawData:   ev.RawData,
							Payload:   ev.Payload,
						})
						stopIdle()
						return
					}
					emit(MessageChunk{
						SessionID: ev.SessionID,
						MessageID: ev.MessageID,
						Type:      ev.Type,
						Done:      true,
						Timestamp: ev.Timestamp,
						RawData:   ev.RawData,
						Payload:   ev.Payload,
					})
					stopIdle()
					return
				}
			case <-idleCh:
				if pending != "" && !sawDelta {
					emit(MessageChunk{SessionID: sessionID, Type: "message.final", Delta: pending, Done: true})
				} else {
					emit(MessageChunk{SessionID: sessionID, Type: "stream.idle", Done: true})
				}
				stopIdle()
				return
			}
		}
	}()

	return out, nil
}

func (c *Client) ExecuteCommand(ctx context.Context, sessionID, command string) (CommandResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	command = strings.TrimSpace(command)
	if sessionID == "" {
		return CommandResult{}, errors.New("session ID is required")
	}
	if command == "" {
		return CommandResult{}, errors.New("command is required")
	}

	requestBody, err := json.Marshal(ExecuteCommandRequest{Command: command})
	if err != nil {
		return CommandResult{}, err
	}

	id := url.PathEscape(sessionID)
	candidates := []endpointCandidate{
		{Path: "/session/" + id + "/command"},
		{Path: "/session/" + id + "/commands"},
		{Path: "/command", Query: url.Values{"sessionID": []string{sessionID}}},
		{Path: "/commands", Query: url.Values{"sessionID": []string{sessionID}}},
		{Path: "/command", Query: url.Values{"sessionId": []string{sessionID}}},
		{Path: "/commands", Query: url.Values{"sessionId": []string{sessionID}}},
	}

	payload, endpoint, err := c.postJSONFromCandidates(ctx, candidates, requestBody)
	if err != nil {
		return CommandResult{}, fmt.Errorf("execute command failed: %w", err)
	}

	obj, ok := payload.(map[string]interface{})
	if !ok {
		return CommandResult{}, fmt.Errorf("execute command failed: non-object payload from %s", endpoint)
	}

	result := parseCommandResultPayload(obj)
	return result, nil
}

func (c *Client) ListFiles(ctx context.Context, sessionID, globPattern string) ([]FileInfo, error) {
	sessionID = strings.TrimSpace(sessionID)
	globPattern = strings.TrimSpace(globPattern)
	if sessionID == "" {
		return nil, errors.New("session ID is required")
	}

	id := url.PathEscape(sessionID)
	query := url.Values{}
	if globPattern != "" {
		query.Set("glob", globPattern)
		query.Set("pattern", globPattern)
	}

	candidates := []endpointCandidate{
		{Path: "/session/" + id + "/file", Query: cloneValues(query)},
		{Path: "/session/" + id + "/files", Query: cloneValues(query)},
		{Path: "/file", Query: mergeValues(cloneValues(query), url.Values{"sessionID": []string{sessionID}})},
		{Path: "/files", Query: mergeValues(cloneValues(query), url.Values{"sessionID": []string{sessionID}})},
		{Path: "/file", Query: mergeValues(cloneValues(query), url.Values{"sessionId": []string{sessionID}})},
		{Path: "/files", Query: mergeValues(cloneValues(query), url.Values{"sessionId": []string{sessionID}})},
	}

	payload, endpoint, err := c.getJSONFromCandidates(ctx, candidates)
	if err != nil {
		return nil, fmt.Errorf("list files failed: %w", err)
	}

	files, ok := parseFileListPayload(payload)
	if !ok {
		return nil, fmt.Errorf("list files failed: unsupported payload from %s", endpoint)
	}
	return files, nil
}

func (c *Client) ReadFile(ctx context.Context, sessionID, filePath string) (FileContent, error) {
	sessionID = strings.TrimSpace(sessionID)
	filePath = strings.TrimSpace(filePath)
	if sessionID == "" {
		return FileContent{}, errors.New("session ID is required")
	}
	if filePath == "" {
		return FileContent{}, errors.New("file path is required")
	}

	id := url.PathEscape(sessionID)
	escapedPath := escapePath(filePath)

	candidates := []endpointCandidate{
		{Path: "/session/" + id + "/file/" + escapedPath},
		{Path: "/session/" + id + "/files/" + escapedPath},
		{Path: "/file/" + escapedPath, Query: url.Values{"sessionID": []string{sessionID}}},
		{Path: "/files/" + escapedPath, Query: url.Values{"sessionID": []string{sessionID}}},
		{Path: "/file", Query: url.Values{"sessionID": []string{sessionID}, "path": []string{filePath}}},
		{Path: "/files", Query: url.Values{"sessionID": []string{sessionID}, "path": []string{filePath}}},
		{Path: "/file", Query: url.Values{"sessionId": []string{sessionID}, "path": []string{filePath}}},
		{Path: "/files", Query: url.Values{"sessionId": []string{sessionID}, "path": []string{filePath}}},
	}

	var lastErr error
	for _, candidate := range candidates {
		res, err := c.doRequest(ctx, http.MethodGet, candidate.Path, candidate.Query, nil, map[string]string{"Accept": "application/json"}, true)
		if err != nil {
			lastErr = err
			continue
		}
		if isEndpointMismatchStatus(res.StatusCode) {
			continue
		}
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			lastErr = fmt.Errorf("endpoint %s returned status %d", candidate.Path, res.StatusCode)
			continue
		}

		if responseLooksJSON(res) {
			payload, decodeErr := decodeJSONPayload(res.Body)
			if decodeErr != nil {
				lastErr = fmt.Errorf("endpoint %s returned invalid JSON: %w", candidate.Path, decodeErr)
				continue
			}
			switch typed := payload.(type) {
			case map[string]interface{}:
				content := parseFileContentPayload(typed, filePath)
				if len(content.RawBytes) == 0 {
					content.RawBytes = []byte(content.Content)
				}
				return content, nil
			case string:
				return FileContent{Path: filePath, Content: typed, RawBytes: []byte(typed)}, nil
			default:
				lastErr = fmt.Errorf("endpoint %s returned unsupported payload type", candidate.Path)
				continue
			}
		}

		body := append([]byte(nil), res.Body...)
		return FileContent{Path: filePath, Content: string(body), RawBytes: body}, nil
	}

	if lastErr == nil {
		lastErr = errors.New("no compatible file endpoint")
	}
	return FileContent{}, fmt.Errorf("read file failed: %w", lastErr)
}

func (c *Client) SubscribeEvents(ctx context.Context) (<-chan DaemonEvent, error) {
	return c.subscribeEventsInternal(ctx)
}

func (c *Client) Health(ctx context.Context) (HealthResponse, error) {
	candidates := []endpointCandidate{{Path: "/global/health"}, {Path: "/health"}}
	payload, endpoint, err := c.getJSONFromCandidates(ctx, candidates)
	if err != nil {
		return HealthResponse{}, fmt.Errorf("health check failed: %w", err)
	}

	obj, ok := payload.(map[string]interface{})
	if !ok {
		return HealthResponse{}, fmt.Errorf("health check failed: non-object payload from %s", endpoint)
	}

	return parseHealthPayload(obj), nil
}

func (c *Client) Config(ctx context.Context) (DaemonConfig, error) {
	candidates := []endpointCandidate{{Path: "/config"}, {Path: "/project/config"}}
	payload, endpoint, err := c.getJSONFromCandidates(ctx, candidates)
	if err != nil {
		return DaemonConfig{}, fmt.Errorf("config fetch failed: %w", err)
	}

	obj, ok := payload.(map[string]interface{})
	if !ok {
		return DaemonConfig{}, fmt.Errorf("config fetch failed: non-object payload from %s", endpoint)
	}

	return DaemonConfig{Raw: cloneMap(obj)}, nil
}

func (c *Client) sendMessageWithoutStream(ctx context.Context, sessionID, prompt string) <-chan MessageChunk {
	out := make(chan MessageChunk, 1)
	go func() {
		defer close(out)
		payload, err := c.postMessage(ctx, sessionID, prompt)
		if err != nil {
			out <- MessageChunk{SessionID: sessionID, Type: "error", Error: err.Error(), Done: true}
			return
		}
		text := extractMessageText(payload)
		if text == "" {
			encoded, _ := json.Marshal(payload)
			text = string(encoded)
		}
		out <- MessageChunk{SessionID: sessionID, Type: "message.final", Delta: text, Done: true}
	}()
	return out
}

func (c *Client) postMessage(ctx context.Context, sessionID, prompt string) (map[string]interface{}, error) {
	requestBody, err := json.Marshal(MessageRequest{Parts: []MessagePart{{Type: "text", Text: prompt}}})
	if err != nil {
		return nil, err
	}

	id := url.PathEscape(sessionID)
	candidates := []endpointCandidate{
		{Path: "/session/" + id + "/message"},
		{Path: "/sessions/" + id + "/messages"},
		{Path: "/session/" + id + "/messages"},
	}

	payload, _, err := c.postJSONFromCandidates(ctx, candidates, requestBody)
	if err != nil {
		return nil, err
	}

	switch typed := payload.(type) {
	case nil:
		return map[string]interface{}{}, nil
	case map[string]interface{}:
		return typed, nil
	default:
		return map[string]interface{}{"data": typed}, nil
	}
}

func (c *Client) subscribeEventsInternal(ctx context.Context) (<-chan DaemonEvent, error) {
	resp, err := c.openEventStream(ctx)
	if err != nil {
		return nil, err
	}

	out := make(chan DaemonEvent, c.config.StreamBuffer)

	go func() {
		defer close(out)
		defer resp.Body.Close()

		emit := func(ev DaemonEvent) bool {
			select {
			case out <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		}

		err := readSSEFrames(ctx, resp.Body, func(frame sseFrame) bool {
			ev := parseDaemonEvent(frame)
			return emit(ev)
		})
		if err != nil && ctx.Err() == nil {
			emit(DaemonEvent{Type: "stream.error", Error: err.Error()})
		}
	}()

	return out, nil
}

func (c *Client) openEventStream(ctx context.Context) (*http.Response, error) {
	endpoints := []string{"/event", "/events"}
	var lastErr error

	for _, endpoint := range endpoints {
		resp, err := c.openEventEndpoint(ctx, endpoint)
		if err != nil {
			lastErr = err
			continue
		}

		if isEndpointMismatchStatus(resp.StatusCode) {
			resp.Body.Close()
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			lastErr = fmt.Errorf("endpoint %s returned status %d body=%s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
			continue
		}

		contentType := strings.ToLower(resp.Header.Get("Content-Type"))
		if contentType != "" && !strings.Contains(contentType, "text/event-stream") {
			resp.Body.Close()
			lastErr = fmt.Errorf("endpoint %s returned non-SSE content-type %q", endpoint, contentType)
			continue
		}

		return resp, nil
	}

	if lastErr == nil {
		lastErr = errors.New("no compatible event endpoint")
	}
	return nil, lastErr
}

func (c *Client) openEventEndpoint(ctx context.Context, endpoint string) (*http.Response, error) {
	attempts := 1
	if c.config.MaxRetries > 0 {
		attempts += c.config.MaxRetries
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.buildURL(endpoint, nil), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "text/event-stream")
		if token := strings.TrimSpace(c.config.AuthToken); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			if attempt >= attempts-1 || ctx.Err() != nil || !isRetryableError(err) {
				return nil, err
			}
			if !sleepBackoff(ctx, c.config.RetryBackoff, attempt+1) {
				return nil, ctx.Err()
			}
			continue
		}

		if attempt < attempts-1 && isRetryableStatus(resp.StatusCode) {
			resp.Body.Close()
			if !sleepBackoff(ctx, c.config.RetryBackoff, attempt+1) {
				return nil, ctx.Err()
			}
			continue
		}

		return resp, nil
	}

	if lastErr == nil {
		lastErr = errors.New("event stream request failed")
	}
	return nil, lastErr
}

func (c *Client) getJSONFromCandidates(ctx context.Context, candidates []endpointCandidate) (interface{}, string, error) {
	var errs []string

	for _, candidate := range candidates {
		res, err := c.doRequest(ctx, http.MethodGet, candidate.Path, candidate.Query, nil, map[string]string{"Accept": "application/json"}, true)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", candidate.Path, err))
			continue
		}

		if isEndpointMismatchStatus(res.StatusCode) {
			continue
		}
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			errs = append(errs, fmt.Sprintf("%s: status %d", candidate.Path, res.StatusCode))
			continue
		}
		if !responseLooksJSON(res) {
			errs = append(errs, fmt.Sprintf("%s: non-JSON content-type %q", candidate.Path, res.Header.Get("Content-Type")))
			continue
		}

		payload, decodeErr := decodeJSONPayload(res.Body)
		if decodeErr != nil {
			errs = append(errs, fmt.Sprintf("%s: invalid JSON (%v)", candidate.Path, decodeErr))
			continue
		}

		return payload, candidate.Path, nil
	}

	if len(errs) == 0 {
		return nil, "", errors.New("no compatible endpoint")
	}
	return nil, "", errors.New(strings.Join(errs, "; "))
}

func (c *Client) postJSONFromCandidates(ctx context.Context, candidates []endpointCandidate, body []byte) (interface{}, string, error) {
	var errs []string

	for _, candidate := range candidates {
		res, err := c.doRequest(
			ctx,
			http.MethodPost,
			candidate.Path,
			candidate.Query,
			body,
			map[string]string{"Accept": "application/json", "Content-Type": "application/json"},
			false,
		)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", candidate.Path, err))
			continue
		}

		if isEndpointMismatchStatus(res.StatusCode) {
			continue
		}
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			errs = append(errs, fmt.Sprintf("%s: status %d", candidate.Path, res.StatusCode))
			continue
		}

		if len(bytes.TrimSpace(res.Body)) == 0 {
			return nil, candidate.Path, nil
		}

		if !responseLooksJSON(res) {
			errs = append(errs, fmt.Sprintf("%s: non-JSON content-type %q", candidate.Path, res.Header.Get("Content-Type")))
			continue
		}

		payload, decodeErr := decodeJSONPayload(res.Body)
		if decodeErr != nil {
			errs = append(errs, fmt.Sprintf("%s: invalid JSON (%v)", candidate.Path, decodeErr))
			continue
		}

		return payload, candidate.Path, nil
	}

	if len(errs) == 0 {
		return nil, "", errors.New("no compatible endpoint")
	}
	return nil, "", errors.New(strings.Join(errs, "; "))
}

func (c *Client) doRequest(
	ctx context.Context,
	method string,
	endpoint string,
	query url.Values,
	body []byte,
	headers map[string]string,
	retry bool,
) (*httpResult, error) {
	attempts := 1
	if retry && c.config.MaxRetries > 0 {
		attempts += c.config.MaxRetries
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.buildURL(endpoint, query), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}

		if token := strings.TrimSpace(c.config.AuthToken); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		for key, value := range headers {
			if strings.TrimSpace(value) == "" {
				continue
			}
			req.Header.Set(key, value)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			if !retry || attempt >= attempts-1 || ctx.Err() != nil || !isRetryableError(err) {
				return nil, err
			}
			if !sleepBackoff(ctx, c.config.RetryBackoff, attempt+1) {
				return nil, ctx.Err()
			}
			continue
		}

		responseBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if !retry || attempt >= attempts-1 || ctx.Err() != nil {
				return nil, readErr
			}
			if !sleepBackoff(ctx, c.config.RetryBackoff, attempt+1) {
				return nil, ctx.Err()
			}
			continue
		}

		result := &httpResult{StatusCode: resp.StatusCode, Header: resp.Header.Clone(), Body: responseBody}
		if retry && attempt < attempts-1 && isRetryableStatus(result.StatusCode) {
			lastErr = fmt.Errorf("retryable status %d", result.StatusCode)
			if !sleepBackoff(ctx, c.config.RetryBackoff, attempt+1) {
				return nil, ctx.Err()
			}
			continue
		}

		return result, nil
	}

	if lastErr == nil {
		lastErr = errors.New("request failed")
	}
	return nil, lastErr
}

func (c *Client) buildURL(endpoint string, query url.Values) string {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		endpoint = "/" + strings.TrimPrefix(endpoint, "/")
		if len(query) == 0 {
			return strings.TrimRight(c.baseURL, "/") + endpoint
		}
		return strings.TrimRight(c.baseURL, "/") + endpoint + "?" + query.Encode()
	}

	basePath := strings.TrimSuffix(u.Path, "/")
	endpointPath := "/" + strings.TrimPrefix(endpoint, "/")
	u.Path = basePath + endpointPath
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	} else {
		u.RawQuery = ""
	}

	return u.String()
}

func parseSessionListPayload(payload interface{}) ([]DaemonSession, bool) {
	var entries []interface{}

	switch typed := payload.(type) {
	case []interface{}:
		entries = typed
	case map[string]interface{}:
		if list, ok := typed["sessions"].([]interface{}); ok {
			entries = list
		} else if nested, ok := typed["data"].(map[string]interface{}); ok {
			if list, ok := nested["sessions"].([]interface{}); ok {
				entries = list
			}
		} else if firstString(typed, "id", "session_id", "sessionId", "sessionID") != "" {
			entries = []interface{}{typed}
		} else {
			return nil, false
		}
	default:
		return nil, false
	}

	result := make([]DaemonSession, 0, len(entries))
	for _, entry := range entries {
		obj, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}
		session := parseSessionEntry(obj)
		if session.ID == "" {
			continue
		}
		result = append(result, session)
	}

	return result, true
}

func parseSessionEntry(payload map[string]interface{}) DaemonSession {
	return DaemonSession{
		ID:              firstString(payload, "id", "session_id", "sessionId", "sessionID"),
		Title:           firstString(payload, "title", "name"),
		Directory:       firstString(payload, "directory", "worktree", "cwd", "workspace_path", "workspacePath"),
		Status:          firstString(payload, "status", "state"),
		CreatedAt:       firstTime(payload, "created_at", "createdAt", "created", "time"),
		LastActivity:    firstTime(payload, "last_activity", "lastActivity", "updated", "updated_at", "updatedAt", "time"),
		DaemonPort:      firstInt(payload, "daemon_port", "daemonPort", "port"),
		AttachedClients: firstInt(payload, "attached_clients", "attachedClients"),
		ProjectID:       firstString(payload, "projectID", "projectId", "project_id"),
		Slug:            firstString(payload, "slug"),
		Version:         firstString(payload, "version"),
		Raw:             cloneMap(payload),
	}
}

func parseFileListPayload(payload interface{}) ([]FileInfo, bool) {
	var entries []interface{}

	switch typed := payload.(type) {
	case []interface{}:
		entries = typed
	case map[string]interface{}:
		switch {
		case typed["files"] != nil:
			list, ok := typed["files"].([]interface{})
			if !ok {
				return nil, false
			}
			entries = list
		case typed["data"] != nil:
			nested, ok := typed["data"].(map[string]interface{})
			if !ok {
				return nil, false
			}
			if list, ok := nested["files"].([]interface{}); ok {
				entries = list
			} else {
				return nil, false
			}
		case firstString(typed, "path", "file", "name") != "":
			entries = []interface{}{typed}
		default:
			return nil, false
		}
	default:
		return nil, false
	}

	files := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		obj, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}
		info := parseFileInfoEntry(obj)
		if info.Path == "" && info.Name == "" {
			continue
		}
		files = append(files, info)
	}

	return files, true
}

func parseFileInfoEntry(payload map[string]interface{}) FileInfo {
	pathValue := firstString(payload, "path", "file", "filepath", "filePath")
	nameValue := firstString(payload, "name")
	if nameValue == "" && pathValue != "" {
		segments := strings.Split(strings.Trim(pathValue, "/"), "/")
		if len(segments) > 0 {
			nameValue = segments[len(segments)-1]
		}
	}

	return FileInfo{
		Path:    pathValue,
		Name:    nameValue,
		Size:    firstInt64(payload, "size", "bytes", "length"),
		IsDir:   firstBool(payload, "is_dir", "isDir", "dir", "directory"),
		Mode:    firstString(payload, "mode", "permissions"),
		ModTime: firstTime(payload, "mod_time", "modTime", "modified", "updated_at", "updatedAt"),
		Raw:     cloneMap(payload),
	}
}

func parseFileContentPayload(payload map[string]interface{}, requestedPath string) FileContent {
	content := firstString(payload, "content", "text", "data")
	encoding := firstString(payload, "encoding")
	raw := []byte(content)

	if strings.EqualFold(encoding, "base64") && content != "" {
		if decoded, err := base64.StdEncoding.DecodeString(content); err == nil {
			raw = decoded
			content = string(decoded)
		}
	}

	pathValue := firstString(payload, "path", "file", "filePath", "filepath")
	if pathValue == "" {
		pathValue = requestedPath
	}

	return FileContent{
		Path:     pathValue,
		Content:  content,
		Encoding: encoding,
		RawBytes: raw,
	}
}

func parseCommandResultPayload(payload map[string]interface{}) CommandResult {
	result := CommandResult{
		ExitCode: firstInt(payload, "exit_code", "exitCode", "code", "status"),
		Stdout:   firstString(payload, "stdout", "output", "result"),
		Stderr:   firstString(payload, "stderr", "error"),
		Raw:      cloneMap(payload),
	}

	if success, ok := firstBoolWithPresence(payload, "success", "ok"); ok {
		result.Success = success
	} else {
		result.Success = result.ExitCode == 0 && result.Stderr == ""
	}

	return result
}

func parseHealthPayload(payload map[string]interface{}) HealthResponse {
	healthy := false
	if value, ok := firstBoolWithPresence(payload, "healthy", "ok", "status"); ok {
		healthy = value
	}

	return HealthResponse{
		Healthy: healthy,
		Version: firstString(payload, "version"),
		Raw:     cloneMap(payload),
	}
}

func parseDaemonEvent(frame sseFrame) DaemonEvent {
	ev := DaemonEvent{
		ID:      strings.TrimSpace(frame.ID),
		Type:    strings.TrimSpace(frame.Event),
		RawData: frame.Data,
	}

	trimmed := bytes.TrimSpace([]byte(frame.Data))
	if len(trimmed) == 0 {
		if ev.Type == "" {
			ev.Type = "message"
		}
		return ev
	}

	ev.Data = append([]byte(nil), trimmed...)
	payload, err := decodeJSONPayload(trimmed)
	if err != nil {
		if ev.Type == "" {
			ev.Type = "message"
		}
		return ev
	}

	obj, ok := payload.(map[string]interface{})
	if !ok {
		if ev.Type == "" {
			ev.Type = "message"
		}
		return ev
	}

	ev.Payload = cloneMap(obj)
	if ev.Type == "" {
		ev.Type = firstString(obj, "type", "event", "eventType", "name")
	}
	ev.SessionID = firstString(obj, "sessionID", "sessionId", "session_id")
	if ev.SessionID == "" {
		if nested := firstNestedMap(obj, "session", "data"); nested != nil {
			ev.SessionID = firstString(nested, "id", "sessionID", "sessionId", "session_id")
		}
	}
	ev.MessageID = firstString(obj, "messageID", "messageId", "message_id")
	if ev.MessageID == "" {
		if nested := firstNestedMap(obj, "message", "data"); nested != nil {
			ev.MessageID = firstString(nested, "id", "messageID", "messageId", "message_id")
		}
	}
	ev.Timestamp = firstTime(obj, "timestamp", "time", "created_at", "createdAt", "updated_at", "updatedAt")
	ev.Delta = extractDelta(obj)

	if ev.Type == "" {
		ev.Type = "message"
	}

	return ev
}

func extractMessageText(payload map[string]interface{}) string {
	if len(payload) == 0 {
		return ""
	}

	if s := firstString(payload, "text", "content", "delta", "output", "result"); s != "" {
		return s
	}

	if nested := firstNestedMap(payload, "message", "data"); nested != nil {
		if s := firstString(nested, "text", "content", "delta", "output", "result"); s != "" {
			return s
		}
	}

	if parts, ok := payload["parts"].([]interface{}); ok {
		for i := len(parts) - 1; i >= 0; i-- {
			part, ok := parts[i].(map[string]interface{})
			if !ok {
				continue
			}
			if s := firstString(part, "text", "content", "delta"); s != "" {
				return s
			}
		}
	}

	return ""
}

func extractDelta(payload map[string]interface{}) string {
	if s := firstString(payload, "delta"); s != "" {
		return s
	}

	if part := firstNestedMap(payload, "part"); part != nil {
		if s := firstString(part, "delta", "text", "content"); s != "" {
			return s
		}
	}

	if message := firstNestedMap(payload, "message"); message != nil {
		if s := firstString(message, "delta", "text", "content"); s != "" {
			return s
		}
		if part := firstNestedMap(message, "part"); part != nil {
			if s := firstString(part, "delta", "text", "content"); s != "" {
				return s
			}
		}
	}

	if parts, ok := payload["parts"].([]interface{}); ok {
		for i := len(parts) - 1; i >= 0; i-- {
			part, ok := parts[i].(map[string]interface{})
			if !ok {
				continue
			}
			if s := firstString(part, "delta", "text", "content"); s != "" {
				return s
			}
		}
	}

	return ""
}

func eventMatchesSession(ev DaemonEvent, sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return true
	}
	return strings.TrimSpace(ev.SessionID) == sessionID
}

func isDeltaEvent(ev DaemonEvent) bool {
	if strings.TrimSpace(ev.Delta) != "" {
		return true
	}
	typ := strings.ToLower(strings.TrimSpace(ev.Type))
	return strings.Contains(typ, "message.part.delta")
}

func isTerminalEvent(eventType string) bool {
	switch strings.ToLower(strings.TrimSpace(eventType)) {
	case "session.idle", "session.error", "message.completed", "message.done", "message.error", "message.stopped", "response.completed", "completion.done":
		return true
	default:
		return false
	}
}

func readSSEFrames(ctx context.Context, reader io.Reader, handle func(frame sseFrame) bool) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, defaultScannerInitialSize), defaultScannerMaxSize)
	scanner.Split(splitSSEFrame)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		frame, ok := parseSSEFrame(scanner.Bytes())
		if !ok {
			continue
		}
		if !handle(frame) {
			return nil
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	return nil
}

func splitSSEFrame(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}

	if idx := bytes.Index(data, []byte("\r\n\r\n")); idx >= 0 {
		return idx + 4, bytes.Trim(data[:idx], "\r\n"), nil
	}
	if idx := bytes.Index(data, []byte("\n\n")); idx >= 0 {
		return idx + 2, bytes.Trim(data[:idx], "\r\n"), nil
	}

	if atEOF {
		return len(data), bytes.Trim(data, "\r\n"), nil
	}

	return 0, nil, nil
}

func parseSSEFrame(raw []byte) (sseFrame, bool) {
	if len(raw) == 0 {
		return sseFrame{}, false
	}

	normalized := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	lines := bytes.Split(normalized, []byte("\n"))

	frame := sseFrame{}
	dataLines := make([]string, 0, 4)

	for _, lineBytes := range lines {
		line := strings.TrimRight(string(lineBytes), "\r")
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimLeft(value, " ")

		switch key {
		case "id":
			frame.ID = value
		case "event":
			frame.Event = value
		case "data":
			dataLines = append(dataLines, value)
		}
	}

	frame.Data = strings.Join(dataLines, "\n")
	if strings.TrimSpace(frame.ID) == "" && strings.TrimSpace(frame.Event) == "" && strings.TrimSpace(frame.Data) == "" {
		return sseFrame{}, false
	}

	return frame, true
}

func isEndpointMismatchStatus(status int) bool {
	return status == http.StatusNotFound || status == http.StatusMethodNotAllowed || status == http.StatusNotAcceptable
}

func responseLooksJSON(res *httpResult) bool {
	contentType := strings.ToLower(strings.TrimSpace(res.Header.Get("Content-Type")))
	if strings.Contains(contentType, "application/json") || strings.Contains(contentType, "+json") {
		return true
	}
	trimmed := bytes.TrimSpace(res.Body)
	if len(trimmed) == 0 {
		return true
	}
	if json.Valid(trimmed) {
		return true
	}
	return false
}

func decodeJSONPayload(body []byte) (interface{}, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload interface{}
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func sleepBackoff(ctx context.Context, step time.Duration, multiplier int) bool {
	if step <= 0 {
		step = defaultRetryBackoff
	}
	d := step * time.Duration(multiplier)
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func isRetryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout || status >= 500
}

func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return true
		}
		if t, ok := interface{}(netErr).(interface{ Temporary() bool }); ok {
			return t.Temporary()
		}
	}
	return true
}

func cloneValues(values url.Values) url.Values {
	if values == nil {
		return nil
	}
	cloned := url.Values{}
	for key, vals := range values {
		copyVals := make([]string, len(vals))
		copy(copyVals, vals)
		cloned[key] = copyVals
	}
	return cloned
}

func mergeValues(values ...url.Values) url.Values {
	merged := url.Values{}
	for _, item := range values {
		for key, vals := range item {
			for _, value := range vals {
				merged.Add(key, value)
			}
		}
	}
	return merged
}

func escapePath(filePath string) string {
	trimmed := strings.TrimSpace(filePath)
	trimmed = strings.TrimPrefix(trimmed, "/")
	segments := strings.Split(trimmed, "/")
	for i := range segments {
		segments[i] = url.PathEscape(segments[i])
	}
	return strings.Join(segments, "/")
}

func firstNestedMap(payload map[string]interface{}, keys ...string) map[string]interface{} {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok {
			continue
		}
		if nested, ok := value.(map[string]interface{}); ok {
			return nested
		}
	}
	return nil
}

func cloneMap(payload map[string]interface{}) map[string]interface{} {
	if payload == nil {
		return nil
	}
	cloned := make(map[string]interface{}, len(payload))
	for key, value := range payload {
		cloned[key] = value
	}
	return cloned
}

func firstString(payload map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			if s := strings.TrimSpace(typed); s != "" {
				return s
			}
		case json.Number:
			if s := strings.TrimSpace(typed.String()); s != "" {
				return s
			}
		case float64:
			if !math.IsNaN(typed) && !math.IsInf(typed, 0) {
				return strconv.FormatInt(int64(typed), 10)
			}
		case float32:
			f := float64(typed)
			if !math.IsNaN(f) && !math.IsInf(f, 0) {
				return strconv.FormatInt(int64(f), 10)
			}
		case int:
			return strconv.Itoa(typed)
		case int64:
			return strconv.FormatInt(typed, 10)
		}
	}
	return ""
}

func firstInt(payload map[string]interface{}, keys ...string) int {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case json.Number:
			if n, err := typed.Int64(); err == nil {
				return int(n)
			}
			if f, err := typed.Float64(); err == nil {
				return int(f)
			}
		case float64:
			if !math.IsNaN(typed) && !math.IsInf(typed, 0) {
				return int(typed)
			}
		case float32:
			f := float64(typed)
			if !math.IsNaN(f) && !math.IsInf(f, 0) {
				return int(f)
			}
		case int:
			return typed
		case int64:
			return int(typed)
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
				return n
			}
		}
	}
	return 0
}

func firstInt64(payload map[string]interface{}, keys ...string) int64 {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case json.Number:
			if n, err := typed.Int64(); err == nil {
				return n
			}
			if f, err := typed.Float64(); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
				return int64(f)
			}
		case float64:
			if !math.IsNaN(typed) && !math.IsInf(typed, 0) {
				return int64(typed)
			}
		case float32:
			f := float64(typed)
			if !math.IsNaN(f) && !math.IsInf(f, 0) {
				return int64(f)
			}
		case int:
			return int64(typed)
		case int64:
			return typed
		case string:
			if n, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64); err == nil {
				return n
			}
		}
	}
	return 0
}

func firstBool(payload map[string]interface{}, keys ...string) bool {
	b, _ := firstBoolWithPresence(payload, keys...)
	return b
}

func firstBoolWithPresence(payload map[string]interface{}, keys ...string) (bool, bool) {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case bool:
			return typed, true
		case string:
			s := strings.TrimSpace(strings.ToLower(typed))
			switch s {
			case "true", "1", "yes", "ok", "healthy", "success":
				return true, true
			case "false", "0", "no", "error", "failed", "unhealthy":
				return false, true
			}
		case json.Number:
			if n, err := typed.Int64(); err == nil {
				return n != 0, true
			}
			if f, err := typed.Float64(); err == nil {
				return f != 0, true
			}
		case float64:
			if !math.IsNaN(typed) && !math.IsInf(typed, 0) {
				return typed != 0, true
			}
		case int:
			return typed != 0, true
		}
	}
	return false, false
}

func firstTime(payload map[string]interface{}, keys ...string) time.Time {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok {
			continue
		}
		timestamp := parseFlexibleTime(value)
		if !timestamp.IsZero() {
			return timestamp
		}
	}
	return time.Time{}
}

func parseFlexibleTime(value interface{}) time.Time {
	switch typed := value.(type) {
	case string:
		s := strings.TrimSpace(typed)
		if s == "" {
			return time.Time{}
		}
		if ts, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return ts
		}
		if ts, err := time.Parse(time.RFC3339, s); err == nil {
			return ts
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return unixMaybeMillis(n)
		}
	case json.Number:
		if n, err := typed.Int64(); err == nil {
			return unixMaybeMillis(n)
		}
		if f, err := typed.Float64(); err == nil {
			return unixMaybeMillis(int64(f))
		}
	case float64:
		if !math.IsNaN(typed) && !math.IsInf(typed, 0) {
			return unixMaybeMillis(int64(typed))
		}
	case int64:
		return unixMaybeMillis(typed)
	case int:
		return unixMaybeMillis(int64(typed))
	}
	return time.Time{}
}

func unixMaybeMillis(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	if value > 1_000_000_000_000 {
		return time.UnixMilli(value)
	}
	return time.Unix(value, 0)
}
