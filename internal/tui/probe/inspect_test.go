package probe

import (
	"context"
	"errors"
	"strings"
	"testing"

	"opencoderouter/internal/tui/model"
)

type sequenceRunner struct {
	outputs [][]byte
	errs    []error
	calls   int
}

func (r *sequenceRunner) Run(_ context.Context, _ string, _ ...string) ([]byte, error) {
	idx := r.calls
	r.calls++

	var out []byte
	if idx < len(r.outputs) {
		out = r.outputs[idx]
	}

	var err error
	if idx < len(r.errs) {
		err = r.errs[idx]
	}

	return out, err
}

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

func TestFetchSessionInspectLatestBlockRetriesOnParseError(t *testing.T) {
	runner := &sequenceRunner{
		outputs: [][]byte{
			[]byte(`{"messages":`),
			[]byte(`{"messages":[{"info":{"role":"assistant"},"parts":[{"type":"text","text":"from retry"}]}]}`),
		},
	}

	svc := &ProbeService{runner: runner}
	host := model.Host{Name: "demo-host"}
	session := model.Session{ID: "ses_retry", Directory: "/tmp/retry"}

	got, err := svc.FetchSessionInspectLatestBlock(context.Background(), host, session)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "from retry" {
		t.Fatalf("unexpected block: %q", got)
	}
	if runner.calls != 2 {
		t.Fatalf("expected 2 attempts, got %d", runner.calls)
	}
}

func TestFetchSessionInspectLatestBlockStopsAfterMaxAttempts(t *testing.T) {
	runner := &sequenceRunner{
		outputs: [][]byte{
			[]byte(`{"messages":`),
			[]byte(`{"messages":`),
			[]byte(`{"messages":`),
		},
	}

	svc := &ProbeService{runner: runner}
	host := model.Host{Name: "demo-host"}
	session := model.Session{ID: "ses_fail", Directory: "/tmp/fail"}

	_, err := svc.FetchSessionInspectLatestBlock(context.Background(), host, session)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "parse session inspect export") {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner.calls != inspectFetchMaxAttempts {
		t.Fatalf("expected %d attempts, got %d", inspectFetchMaxAttempts, runner.calls)
	}
}

func TestFetchSessionInspectLatestBlockReturnsRunnerError(t *testing.T) {
	runner := &sequenceRunner{errs: []error{errors.New("boom")}}
	svc := &ProbeService{runner: runner}
	host := model.Host{Name: "demo-host"}
	session := model.Session{ID: "ses_err", Directory: "/tmp/err"}

	_, err := svc.FetchSessionInspectLatestBlock(context.Background(), host, session)
	if err == nil {
		t.Fatal("expected fetch error")
	}
	if !strings.Contains(err.Error(), "fetch session inspect export") {
		t.Fatalf("unexpected error: %v", err)
	}
}
