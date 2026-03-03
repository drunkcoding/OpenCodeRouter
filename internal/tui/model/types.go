package model

import "time"

// ActivityState captures a high-level activity bucket for a session.
type ActivityState string

const (
	// ActivityActive marks sessions with very recent activity.
	ActivityActive ActivityState = "ACTIVE"
	// ActivityIdle marks sessions that are not fresh but still warm.
	ActivityIdle ActivityState = "IDLE"
	// ActivityCold marks sessions that have been inactive for a long period.
	ActivityCold ActivityState = "COLD"
	// ActivityUnknown marks sessions where activity cannot be derived.
	ActivityUnknown ActivityState = "UNKNOWN"
)

// HostStatus represents remote availability from probe/discovery.
type HostStatus string

const (
	// HostStatusUnknown is used before probing.
	HostStatusUnknown HostStatus = "unknown"
	// HostStatusOnline indicates successful probe and parsed sessions.
	HostStatusOnline HostStatus = "online"
	// HostStatusOffline indicates unreachable host or unavailable opencode.
	HostStatusOffline HostStatus = "offline"
	// HostStatusError indicates probe/discovery failures with details.
	HostStatusError HostStatus = "error"
	// HostStatusAuthRequired indicates the host is reachable but requires password authentication.
	HostStatusAuthRequired HostStatus = "auth_required"
)

// ProxyKind classifies how a host reaches its target.
type ProxyKind string

const (
	// ProxyKindNone means direct SSH connection.
	ProxyKindNone ProxyKind = "none"
	// ProxyKindJump means ProxyJump directive is in use.
	ProxyKindJump ProxyKind = "jump"
	// ProxyKindCommand means ProxyCommand directive is in use (opaque).
	ProxyKindCommand ProxyKind = "command"
)

// TransportStatus tracks jump-host reachability for dependent hosts.
type TransportStatus string

const (
	// TransportUnknown is the default before preflight.
	TransportUnknown TransportStatus = "unknown"
	// TransportReady means all jump dependencies are reachable.
	TransportReady TransportStatus = "ready"
	// TransportAuthRequired means a jump hop needs password auth.
	TransportAuthRequired TransportStatus = "auth_required"
	// TransportUnreachable means a jump hop is down.
	TransportUnreachable TransportStatus = "unreachable"
	// TransportBlocked means this host is blocked because a jump dependency is not ready.
	TransportBlocked TransportStatus = "blocked"
)

// SessionStatus tracks lifecycle of a remote session.
type SessionStatus string

const (
	// SessionStatusActive indicates an in-progress or recently active session.
	SessionStatusActive SessionStatus = "active"
	// SessionStatusIdle indicates an existing but currently quiet session.
	SessionStatusIdle SessionStatus = "idle"
	// SessionStatusArchived indicates a completed or archived session.
	SessionStatusArchived SessionStatus = "archived"
	// SessionStatusUnknown is used when source status is absent.
	SessionStatusUnknown SessionStatus = "unknown"
)

// Session represents a single opencode session entry.
type Session struct {
	ID           string
	Project      string
	Title        string
	LastActivity time.Time
	Status       SessionStatus
	MessageCount int
	Agents       []string
	Activity     ActivityState
}

// JumpHop represents one hop in a ProxyJump chain.
type JumpHop struct {
	// Raw is the original hop string from ssh config.
	Raw string
	// Host is the hostname or IP of the hop.
	Host string
	// User is the SSH user for this hop (empty = default).
	User string
	// Port is the SSH port (0 = default 22).
	Port int
	// AliasRef is the internal SSH alias if this hop maps to a known host.
	AliasRef string
	// External is true when the hop doesn't match any known alias.
	External bool
}

// Project groups sessions under a logical project name.
type Project struct {
	Name     string
	Sessions []Session
}

// Host stores all sessions discovered for one SSH target.
type Host struct {
	Name        string
	Address     string
	User        string
	Label       string
	Priority    int
	Status      HostStatus
	LastSeen    time.Time
	LastError   string
	OpencodeBin string
	Projects    []Project

	// ProxyJump fields
	ProxyKind      ProxyKind
	ProxyJumpRaw   string    // raw ProxyJump value from ssh -G
	ProxyCommand   string    // raw ProxyCommand value (opaque)
	JumpChain      []JumpHop // parsed hop sequence
	DependsOn      []string  // aliases this host needs as jump providers
	Dependents     []string  // aliases that use this host as a jump provider
	BlockedBy      []string  // aliases currently blocking this host
	Transport      TransportStatus
	TransportError string
}

// ActivityThresholds controls state bucketing windows.
type ActivityThresholds struct {
	Active time.Duration
	Idle   time.Duration
}

// SessionCount returns the total sessions for a host.
func (h Host) SessionCount() int {
	total := 0
	for _, p := range h.Projects {
		total += len(p.Sessions)
	}
	return total
}

// ResolveActivityState maps a timestamp to ACTIVE/IDLE/COLD.
func ResolveActivityState(lastActivity, now time.Time, thresholds ActivityThresholds) ActivityState {
	if lastActivity.IsZero() {
		return ActivityUnknown
	}
	if now.Before(lastActivity) {
		return ActivityActive
	}
	age := now.Sub(lastActivity)
	if age <= thresholds.Active {
		return ActivityActive
	}
	if age <= thresholds.Idle {
		return ActivityIdle
	}
	return ActivityCold
}
