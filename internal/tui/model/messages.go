package model

import "time"

// DiscoveryResultMsg is emitted when host discovery completes.
type DiscoveryResultMsg struct {
	Hosts []Host
	Err   error
}

// ProbeResultMsg is emitted after probing all hosts.
type ProbeResultMsg struct {
	Hosts       []Host
	Err         error
	RefreshedAt time.Time
}

// TickMsg drives countdown and animation updates.
type TickMsg struct {
	Now time.Time
}

// SSHErrorMsg carries per-host SSH failures for UI display.
type SSHErrorMsg struct {
	Host string
	Err  error
}

// SearchChangedMsg is emitted when the search input changes.
type SearchChangedMsg struct {
	Query string
}

// TransportPreflightMsg is emitted after transport preflight probing completes.
type TransportPreflightMsg struct {
	Hosts []Host
	Err   error
}

type AttachFinishedMsg struct {
	Err error
}

type ToastExpiredMsg struct {
	Token uint64
}
