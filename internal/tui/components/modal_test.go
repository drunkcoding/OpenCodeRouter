package components

import (
	"strings"
	"testing"

	"opencoderouter/internal/tui/model"
	"opencoderouter/internal/tui/theme"

	tea "charm.land/bubbletea/v2"
)

func TestModalOpenConfirmReloadShowsReloadContent(t *testing.T) {
	modal := NewModalLayer(theme.Minimal())
	directory := "/srv/workspaces/payments-service"

	modal.OpenConfirmReload("dev-host", directory)

	if !modal.Active() {
		t.Fatalf("expected modal to be active")
	}
	if modal.Type() != ModalTypeConfirmReload {
		t.Fatalf("expected modal type %q, got %q", ModalTypeConfirmReload, modal.Type())
	}

	view := modal.View()
	for _, want := range []string{"Reload OpenCode", "payments-service", "dev-host", directory, "y confirm • n cancel"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected modal view to contain %q, got %q", want, view)
		}
	}
}

func TestModalConfirmReloadEmitsConfirmMessage(t *testing.T) {
	modal := NewModalLayer(theme.Minimal())
	directory := "/tmp/project-a"
	modal.OpenConfirmReload("host-a", directory)

	next, cmd := modal.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatalf("expected confirm cmd")
	}
	if next.Active() {
		t.Fatalf("expected modal to close after confirmation")
	}

	msg := cmd()
	confirm, ok := msg.(model.ModalConfirmReloadMsg)
	if !ok {
		t.Fatalf("expected ModalConfirmReloadMsg, got %T", msg)
	}
	if confirm.HostName != "host-a" || confirm.Directory != directory {
		t.Fatalf("unexpected confirm payload: %#v", confirm)
	}
}

func TestModalConfirmReloadCancelClosesWithoutCmd(t *testing.T) {
	modal := NewModalLayer(theme.Minimal())
	modal.OpenConfirmReload("host-a", "/tmp/project-a")

	next, cmd := modal.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if cmd != nil {
		t.Fatalf("expected no cmd on cancel")
	}
	if next.Active() {
		t.Fatalf("expected modal to close on cancel")
	}
}
