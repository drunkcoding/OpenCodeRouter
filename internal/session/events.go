package session

import "time"

type EventType string

const (
	EventTypeSessionCreated       EventType = "session.created"
	EventTypeSessionStopped       EventType = "session.stopped"
	EventTypeSessionHealthChanged EventType = "session.health_changed"
	EventTypeSessionAttached      EventType = "session.attached"
	EventTypeSessionDetached      EventType = "session.detached"
)

type Event interface {
	Type() EventType
	Timestamp() time.Time
	SessionID() string
}

type EventFilter struct {
	SessionID string
	Types     []EventType
}

type SessionCreated struct {
	At      time.Time
	Session SessionHandle
}

func (e SessionCreated) Type() EventType      { return EventTypeSessionCreated }
func (e SessionCreated) Timestamp() time.Time { return e.At }
func (e SessionCreated) SessionID() string    { return e.Session.ID }

type SessionStopped struct {
	At      time.Time
	Session SessionHandle
	Reason  string
}

func (e SessionStopped) Type() EventType      { return EventTypeSessionStopped }
func (e SessionStopped) Timestamp() time.Time { return e.At }
func (e SessionStopped) SessionID() string    { return e.Session.ID }

type SessionHealthChanged struct {
	At       time.Time
	Session  SessionHandle
	Previous HealthStatus
	Current  HealthStatus
}

func (e SessionHealthChanged) Type() EventType      { return EventTypeSessionHealthChanged }
func (e SessionHealthChanged) Timestamp() time.Time { return e.At }
func (e SessionHealthChanged) SessionID() string    { return e.Session.ID }

type SessionAttached struct {
	At              time.Time
	Session         SessionHandle
	AttachedClients int
	ClientID        string
}

func (e SessionAttached) Type() EventType      { return EventTypeSessionAttached }
func (e SessionAttached) Timestamp() time.Time { return e.At }
func (e SessionAttached) SessionID() string    { return e.Session.ID }

type SessionDetached struct {
	At              time.Time
	Session         SessionHandle
	AttachedClients int
	ClientID        string
}

func (e SessionDetached) Type() EventType      { return EventTypeSessionDetached }
func (e SessionDetached) Timestamp() time.Time { return e.At }
func (e SessionDetached) SessionID() string    { return e.Session.ID }

type EventBus interface {
	Subscribe(filter EventFilter) (<-chan Event, func(), error)
	Publish(event Event) error
}
