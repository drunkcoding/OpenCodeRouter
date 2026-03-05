package probe

import (
	"strings"
	"testing"

	"opencoderouter/internal/tui/model"
)

func TestExtractLatestConversationBlockText(t *testing.T) {
	raw := []byte(`{
		"messages": [
			{"info": {"role": "user"}, "parts": [{"type": "text", "text": "first"}]},
			{"info": {"role": "assistant"}, "parts": [{"type": "text", "text": "latest answer"}]}
		]
	}`)

	got, err := extractLatestConversationBlock(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "latest answer" {
		t.Fatalf("unexpected block: %q", got)
	}
}

func TestExtractLatestConversationBlockToolFallback(t *testing.T) {
	raw := []byte(`{
		"messages": [
			{"info": {"role": "assistant"}, "parts": [
				{"type": "text", "text": "ignored", "ignored": true},
				{"type": "tool", "state": {"status": "completed", "output": "tool output"}}
			]}
		]
	}`)

	got, err := extractLatestConversationBlock(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "tool output" {
		t.Fatalf("unexpected fallback block: %q", got)
	}
}

func TestExtractLatestConversationBlockMalformed(t *testing.T) {
	if _, err := extractLatestConversationBlock([]byte(`{"messages":`)); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestExtractLatestConversationBlockEmptyIsAllowed(t *testing.T) {
	got, err := extractLatestConversationBlock([]byte("  \n\t  "))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty block, got %q", got)
	}
}

func TestExtractLatestConversationBlockNoisyWrapper(t *testing.T) {
	raw := []byte("prefix noise\n{\"messages\":[{\"info\":{\"role\":\"assistant\"},\"parts\":[{\"type\":\"text\",\"text\":\"wrapped answer\"}]}]}\ntrailer noise")

	got, err := extractLatestConversationBlock(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "wrapped answer" {
		t.Fatalf("unexpected block: %q", got)
	}
}

func TestClampConversationBlock(t *testing.T) {
	input := "\x1b[31mhello\x1b[0m\r\n"
	for i := 0; i < 20; i++ {
		input += "line\n"
	}

	got := clampConversationBlock(input)
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("ansi sequence not removed: %q", got)
	}
	if !strings.Contains(got, "...") {
		t.Fatalf("expected truncation marker in %q", got)
	}
}

func TestBuildInspectRemoteCmdUsesExport(t *testing.T) {
	host := model.Host{Name: "dev-1", OpencodeBin: "opencode"}
	session := model.Session{ID: "ses_123", Directory: "/tmp/project"}

	cmd := buildInspectRemoteCmd(host, session)
	if !strings.Contains(cmd, "export 'ses_123'") {
		t.Fatalf("missing export invocation: %q", cmd)
	}
	if !strings.Contains(cmd, "cd '/tmp/project'") {
		t.Fatalf("missing directory cd: %q", cmd)
	}
}
