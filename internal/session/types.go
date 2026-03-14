package session

import (
	"context"
	"io"
	"time"
)

type SessionStatus string

const (
	SessionStatusUnknown SessionStatus = "unknown"
	SessionStatusActive  SessionStatus = "active"
	SessionStatusIdle    SessionStatus = "idle"
	SessionStatusStopped SessionStatus = "stopped"
	SessionStatusError   SessionStatus = "error"
)

type HealthState string

const (
	HealthStateUnknown   HealthState = "unknown"
	HealthStateHealthy   HealthState = "healthy"
	HealthStateUnhealthy HealthState = "unhealthy"
)

type CreateOpts struct {
	WorkspacePath  string
	OpenCodeBinary string
	EnvVars        map[string]string
	Labels         map[string]string
}

type SessionListFilter struct {
	WorkspacePath string
	Status        SessionStatus
	LabelSelector map[string]string
}

type SessionHandle struct {
	ID              string
	DaemonPort      int
	WorkspacePath   string
	Status          SessionStatus
	CreatedAt       time.Time
	LastActivity    time.Time
	AttachedClients int
	Labels          map[string]string
}

type HealthStatus struct {
	State     HealthState
	LastCheck time.Time
	Error     string
}

type TerminalConn interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
}

type SessionManager interface {
	Create(ctx context.Context, opts CreateOpts) (*SessionHandle, error)
	Get(id string) (*SessionHandle, error)
	List(filter SessionListFilter) ([]SessionHandle, error)
	Stop(ctx context.Context, id string) error
	Restart(ctx context.Context, id string) (*SessionHandle, error)
	Delete(ctx context.Context, id string) error
	AttachTerminal(ctx context.Context, id string) (TerminalConn, error)
	Health(ctx context.Context, id string) (HealthStatus, error)
}
