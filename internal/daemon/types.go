package daemon

import (
	"encoding/json"
	"net/http"
	"time"
)

type ClientConfig struct {
	Timeout           time.Duration
	MaxRetries        int
	RetryBackoff      time.Duration
	AuthToken         string
	HTTPClient        *http.Client
	StreamBuffer      int
	StreamIdleTimeout time.Duration
}

type DaemonSession struct {
	ID              string                 `json:"id"`
	Title           string                 `json:"title,omitempty"`
	Directory       string                 `json:"directory,omitempty"`
	Status          string                 `json:"status,omitempty"`
	CreatedAt       time.Time              `json:"createdAt,omitempty"`
	LastActivity    time.Time              `json:"lastActivity,omitempty"`
	DaemonPort      int                    `json:"daemonPort,omitempty"`
	AttachedClients int                    `json:"attachedClients,omitempty"`
	ProjectID       string                 `json:"projectID,omitempty"`
	Slug            string                 `json:"slug,omitempty"`
	Version         string                 `json:"version,omitempty"`
	Raw             map[string]interface{} `json:"-"`
}

type MessagePart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type MessageRequest struct {
	Parts []MessagePart `json:"parts"`
}

type MessageChunk struct {
	SessionID string                 `json:"sessionId,omitempty"`
	MessageID string                 `json:"messageId,omitempty"`
	Type      string                 `json:"type,omitempty"`
	Delta     string                 `json:"delta,omitempty"`
	Done      bool                   `json:"done,omitempty"`
	Error     string                 `json:"error,omitempty"`
	Timestamp time.Time              `json:"timestamp,omitempty"`
	RawData   string                 `json:"rawData,omitempty"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
}

type ExecuteCommandRequest struct {
	Command string `json:"command"`
}

type CommandResult struct {
	ExitCode int                    `json:"exitCode"`
	Success  bool                   `json:"success"`
	Stdout   string                 `json:"stdout,omitempty"`
	Stderr   string                 `json:"stderr,omitempty"`
	Raw      map[string]interface{} `json:"-"`
}

type FileInfo struct {
	Path    string                 `json:"path"`
	Name    string                 `json:"name,omitempty"`
	Size    int64                  `json:"size,omitempty"`
	IsDir   bool                   `json:"isDir"`
	Mode    string                 `json:"mode,omitempty"`
	ModTime time.Time              `json:"modTime,omitempty"`
	Raw     map[string]interface{} `json:"-"`
}

type FileContent struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Encoding string `json:"encoding,omitempty"`
	RawBytes []byte `json:"-"`
}

type DaemonEvent struct {
	ID        string                 `json:"id,omitempty"`
	Type      string                 `json:"type,omitempty"`
	SessionID string                 `json:"sessionId,omitempty"`
	MessageID string                 `json:"messageId,omitempty"`
	Timestamp time.Time              `json:"timestamp,omitempty"`
	Delta     string                 `json:"delta,omitempty"`
	RawData   string                 `json:"rawData,omitempty"`
	Data      json.RawMessage        `json:"data,omitempty"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
	Error     string                 `json:"error,omitempty"`
}

type HealthResponse struct {
	Healthy bool                   `json:"healthy"`
	Version string                 `json:"version,omitempty"`
	Raw     map[string]interface{} `json:"-"`
}

type DaemonConfig struct {
	Raw map[string]interface{} `json:"raw"`
}
