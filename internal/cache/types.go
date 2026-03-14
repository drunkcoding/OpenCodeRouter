package cache

import "time"

// EntryType identifies the kind of scrollback record.
type EntryType string

const (
	EntryTypeAgentMessage   EntryType = "agent_message"
	EntryTypeToolCall       EntryType = "tool_call"
	EntryTypeTerminalOutput EntryType = "terminal_output"
	EntryTypeFileDiff       EntryType = "file_diff"
	EntryTypeSystemEvent    EntryType = "system_event"
)

// Entry is a single scrollback item associated with a session.
type Entry struct {
	Timestamp time.Time      `json:"timestamp"`
	Type      EntryType      `json:"type"`
	Content   []byte         `json:"content"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// EvictionPolicy controls how entries are evicted when limits are reached.
type EvictionPolicy string

const (
	EvictionPolicyLRU  EvictionPolicy = "LRU"
	EvictionPolicyFIFO EvictionPolicy = "FIFO"
)

// CacheConfig configures scrollback cache limits and local storage location.
type CacheConfig struct {
	MaxEntriesPerSession int
	MaxTotalSize         int64
	EvictionPolicy       EvictionPolicy
	StoragePath          string
}

// ScrollbackCache defines contract-first persistence APIs for session scrollback.
type ScrollbackCache interface {
	Append(sessionID string, entry Entry) error
	Get(sessionID string, offset, limit int) ([]Entry, error)
	Trim(sessionID string, maxEntries int) error
	Clear(sessionID string) error
	Close() error
}
