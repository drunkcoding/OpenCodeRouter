package keys

import (
	"strings"
	"testing"

	"opencoderouter/internal/tui/config"
)

func TestNewKeyMapReloadSessionsDefaultAndHelp(t *testing.T) {
	km := NewKeyMap(config.KeybindingsConfig{})

	if km.ReloadSessions.Key != "ctrl+r" {
		t.Fatalf("expected default reload key ctrl+r, got %q", km.ReloadSessions.Key)
	}
	if km.ReloadSessions.Description != "reload" {
		t.Fatalf("expected reload description, got %q", km.ReloadSessions.Description)
	}

	helpText := km.HelpText()
	if !strings.Contains(helpText, "ctrl+r reload") {
		t.Fatalf("expected help text to include reload binding, got %q", helpText)
	}
}

func TestNewKeyMapReloadSessionsOverride(t *testing.T) {
	km := NewKeyMap(config.KeybindingsConfig{ReloadSessions: "ctrl+shift+r"})

	if km.ReloadSessions.Key != "ctrl+shift+r" {
		t.Fatalf("expected configured reload key, got %q", km.ReloadSessions.Key)
	}
}
