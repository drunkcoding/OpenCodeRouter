package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"opencoderouter/internal/cache"
	"opencoderouter/internal/session"

	"github.com/gorilla/websocket"
)

const (
	defaultBridgePingInterval   = 30 * time.Second
	defaultBridgeWriteTimeout   = 10 * time.Second
	defaultBridgeReadBufferSize = 1024
	defaultBridgeWriteBufferSz  = 1024
)

type BridgeConfig struct {
	Logger          *slog.Logger
	ScrollbackCache cache.ScrollbackCache
	PingInterval    time.Duration
	WriteTimeout    time.Duration
	ReadBufferSize  int
	WriteBufferSize int
	CheckOrigin     func(*http.Request) bool
}

type TerminalBridge struct {
	logger       *slog.Logger
	cache        cache.ScrollbackCache
	pingInterval time.Duration
	writeTimeout time.Duration
	upgrader     websocket.Upgrader
}

type resizeControlMessage struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

func NewBridge(cfg BridgeConfig) *TerminalBridge {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	pingInterval := cfg.PingInterval
	if pingInterval < 0 {
		pingInterval = 0
	}
	if pingInterval == 0 {
		pingInterval = defaultBridgePingInterval
	}

	writeTimeout := cfg.WriteTimeout
	if writeTimeout < 0 {
		writeTimeout = 0
	}
	if writeTimeout == 0 {
		writeTimeout = defaultBridgeWriteTimeout
	}

	readBufferSize := cfg.ReadBufferSize
	if readBufferSize <= 0 {
		readBufferSize = defaultBridgeReadBufferSize
	}

	writeBufferSize := cfg.WriteBufferSize
	if writeBufferSize <= 0 {
		writeBufferSize = defaultBridgeWriteBufferSz
	}

	checkOrigin := cfg.CheckOrigin
	if checkOrigin == nil {
		checkOrigin = func(*http.Request) bool {
			return true
		}
	}

	return &TerminalBridge{
		logger:       logger,
		cache:        cfg.ScrollbackCache,
		pingInterval: pingInterval,
		writeTimeout: writeTimeout,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  readBufferSize,
			WriteBufferSize: writeBufferSize,
			CheckOrigin:     checkOrigin,
		},
	}
}

func (b *TerminalBridge) Upgrade(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	if b == nil {
		return nil, errors.New("terminal bridge is nil")
	}
	return b.upgrader.Upgrade(w, r, nil)
}

func (b *TerminalBridge) Bridge(ctx context.Context, wsConn *websocket.Conn, terminalConn session.TerminalConn, sessionID string) error {
	if b == nil {
		return errors.New("terminal bridge is nil")
	}
	if wsConn == nil {
		return errors.New("websocket connection is nil")
	}
	if terminalConn == nil {
		return errors.New("terminal connection is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	b.logger.Info("terminal websocket bridge connected", "session_id", sessionID)

	bridgeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if b.pingInterval > 0 {
		_ = wsConn.SetReadDeadline(time.Now().Add(2 * b.pingInterval))
		wsConn.SetPongHandler(func(_ string) error {
			return wsConn.SetReadDeadline(time.Now().Add(2 * b.pingInterval))
		})
	}

	var bytesToBackend atomic.Int64
	var bytesToClient atomic.Int64
	var resizeOps atomic.Int64

	var wsWriteMu sync.Mutex
	writeMessage := func(messageType int, payload []byte) error {
		wsWriteMu.Lock()
		defer wsWriteMu.Unlock()
		if b.writeTimeout > 0 {
			if err := wsConn.SetWriteDeadline(time.Now().Add(b.writeTimeout)); err != nil {
				return err
			}
		}
		return wsConn.WriteMessage(messageType, payload)
	}

	writeControl := func(messageType int, payload []byte) error {
		wsWriteMu.Lock()
		defer wsWriteMu.Unlock()
		deadline := time.Now()
		if b.writeTimeout > 0 {
			deadline = deadline.Add(b.writeTimeout)
		}
		return wsConn.WriteControl(messageType, payload, deadline)
	}

	workerCount := 2
	errCh := make(chan error, 3)

	go func() {
		errCh <- b.pipeBackendToWS(bridgeCtx, terminalConn, writeMessage, &bytesToClient, sessionID)
	}()

	go func() {
		errCh <- b.pipeWSToBackend(bridgeCtx, wsConn, terminalConn, &bytesToBackend, &resizeOps)
	}()

	if b.pingInterval > 0 {
		workerCount++
		go func() {
			errCh <- b.pingLoop(bridgeCtx, writeControl)
		}()
	}

	firstErr := <-errCh
	cancel()
	_ = wsConn.Close()
	_ = terminalConn.Close()

	for i := 1; i < workerCount; i++ {
		<-errCh
	}

	if isExpectedBridgeClosure(firstErr) {
		firstErr = nil
	}

	b.logger.Info(
		"terminal websocket bridge closed",
		"session_id", sessionID,
		"bytes_to_backend", bytesToBackend.Load(),
		"bytes_to_client", bytesToClient.Load(),
		"resize_ops", resizeOps.Load(),
		"error", firstErr,
	)

	return firstErr
}

func (b *TerminalBridge) pipeBackendToWS(
	_ context.Context,
	terminalConn session.TerminalConn,
	writeMessage func(messageType int, payload []byte) error,
	bytesToClient *atomic.Int64,
	sessionID string,
) error {
	buf := make([]byte, 32*1024)
	for {
		n, err := terminalConn.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			b.appendTerminalOutput(sessionID, chunk)
			if writeErr := writeMessage(websocket.BinaryMessage, chunk); writeErr != nil {
				return writeErr
			}
			bytesToClient.Add(int64(n))
		}
		if err != nil {
			return err
		}
	}
}

func (b *TerminalBridge) appendTerminalOutput(sessionID string, chunk []byte) {
	if b == nil || b.cache == nil || len(chunk) == 0 {
		return
	}
	entry := cache.Entry{
		Timestamp: time.Now().UTC(),
		Type:      cache.EntryTypeTerminalOutput,
		Content:   append([]byte(nil), chunk...),
		Metadata: map[string]any{
			"sessionId": sessionID,
			"bytes":     len(chunk),
		},
	}
	if err := b.cache.Append(sessionID, entry); err != nil {
		b.logger.Debug("failed to append terminal output scrollback", "session_id", sessionID, "error", err)
	}
}

func (b *TerminalBridge) pipeWSToBackend(
	_ context.Context,
	wsConn *websocket.Conn,
	terminalConn session.TerminalConn,
	bytesToBackend *atomic.Int64,
	resizeOps *atomic.Int64,
) error {
	for {
		messageType, payload, err := wsConn.ReadMessage()
		if err != nil {
			return err
		}

		switch messageType {
		case websocket.BinaryMessage:
			n, writeErr := terminalConn.Write(payload)
			if n > 0 {
				bytesToBackend.Add(int64(n))
			}
			if writeErr != nil {
				return writeErr
			}
			if n != len(payload) {
				return io.ErrShortWrite
			}
		case websocket.TextMessage:
			resize, decodeErr := decodeResizeControlMessage(payload)
			if decodeErr != nil {
				return decodeErr
			}
			if resizeErr := terminalConn.Resize(resize.Cols, resize.Rows); resizeErr != nil {
				return fmt.Errorf("resize terminal: %w", resizeErr)
			}
			resizeOps.Add(1)
		case websocket.CloseMessage:
			return nil
		}
	}
}

func (b *TerminalBridge) pingLoop(
	ctx context.Context,
	writeControl func(messageType int, payload []byte) error,
) error {
	ticker := time.NewTicker(b.pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := writeControl(websocket.PingMessage, nil); err != nil {
				return err
			}
		}
	}
}

func decodeResizeControlMessage(payload []byte) (resizeControlMessage, error) {
	var msg resizeControlMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return resizeControlMessage{}, fmt.Errorf("decode control message: %w", err)
	}
	if msg.Type != "resize" {
		return resizeControlMessage{}, fmt.Errorf("unsupported control message type %q", msg.Type)
	}
	if msg.Cols <= 0 || msg.Rows <= 0 {
		return resizeControlMessage{}, fmt.Errorf("invalid resize dimensions cols=%d rows=%d", msg.Cols, msg.Rows)
	}
	return msg, nil
}

func isWebSocketUpgrade(r *http.Request) bool {
	if r == nil {
		return false
	}
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

func isExpectedBridgeClosure(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) {
		return true
	}
	if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
		return true
	}
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) {
		switch closeErr.Code {
		case websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived:
			return true
		}
	}
	return false
}
