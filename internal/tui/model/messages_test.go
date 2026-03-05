package model

import "testing"

func TestReloadMessagesCompileCoverage(t *testing.T) {
	confirm := ModalConfirmReloadMsg{
		HostName:  "host-a",
		Directory: "/srv/project",
	}
	if confirm.HostName != "host-a" || confirm.Directory != "/srv/project" {
		t.Fatalf("unexpected confirm payload: %#v", confirm)
	}

	finished := ReloadSessionsFinishedMsg{
		HostName:    "host-a",
		Directory:   "/srv/project",
		Err:         nil,
		KilledCount: 3,
	}
	if finished.HostName != "host-a" || finished.Directory != "/srv/project" || finished.KilledCount != 3 {
		t.Fatalf("unexpected finished payload: %#v", finished)
	}
}
