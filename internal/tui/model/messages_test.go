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

	killConfirm := ModalConfirmKillMsg{
		HostName:    "host-a",
		SessionID:   "session-1",
		Directory:   "/srv/project",
		SaveContext: true,
	}
	if killConfirm.SessionID != "session-1" || !killConfirm.SaveContext {
		t.Fatalf("unexpected kill confirm payload: %#v", killConfirm)
	}

	killFinished := KillSessionFinishedMsg{
		Err:             nil,
		SavedExportPath: "/tmp/export.json",
	}
	if killFinished.SavedExportPath != "/tmp/export.json" {
		t.Fatalf("unexpected kill finished payload: %#v", killFinished)
	}
}
