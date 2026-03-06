package model

import "time"

import sharedmodel "opencoderouter/internal/model"

// DiscoveryResultMsg is emitted when host discovery completes.
type DiscoveryResultMsg struct {
	Hosts []sharedmodel.Host
	Err   error
}

// ProbeResultMsg is emitted after probing all hosts.
type ProbeResultMsg struct {
	Hosts       []sharedmodel.Host
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
	Hosts []sharedmodel.Host
	Err   error
}

type AttachFinishedMsg struct {
	Err error
}

type ToastExpiredMsg struct {
	Token uint64
}

// ModalConfirmCreateMsg is emitted when the user confirms session creation
// in an existing project directory.
type ModalConfirmCreateMsg struct {
	HostName  string
	Directory string
}

// ModalConfirmNewDirMsg is emitted when the user confirms session creation
// in a user-supplied directory path.
type ModalConfirmNewDirMsg struct {
	HostName  string
	Directory string
}

// ModalConfirmGitCloneMsg is emitted when the user confirms git clone
// and session creation on a remote host.
type ModalConfirmGitCloneMsg struct {
	HostName string
	GitURL   string
}

// ModalConfirmKillMsg is emitted when the user confirms session deletion.
type ModalConfirmKillMsg struct {
	HostName    string
	SessionID   string
	Directory   string
	SaveContext bool
}

// ModalConfirmReloadMsg is emitted when the user confirms sessions reload.
type ModalConfirmReloadMsg struct {
	HostName  string
	Directory string
}

// CreateSessionFinishedMsg is returned when interactive SSH session creation exits.
type CreateSessionFinishedMsg struct {
	Err error
}

// KillSessionFinishedMsg is returned when background SSH session delete completes.
type KillSessionFinishedMsg struct {
	Err             error
	SavedExportPath string
}

// ReloadSessionsFinishedMsg is returned when background reload command completes.
type ReloadSessionsFinishedMsg struct {
	HostName    string
	Directory   string
	Err         error
	KilledCount int
}

// GitCloneFinishedMsg is returned when interactive SSH git clone + session exits.
type GitCloneFinishedMsg struct {
	Err error
}

type TerminalOutputMsg struct {
	SessionID string
	Data      []byte
}

type TerminalInputForwardedMsg struct {
	SessionID string
	Err       error
}

type TerminalClosedMsg struct {
	SessionID string
	Err       error
}
