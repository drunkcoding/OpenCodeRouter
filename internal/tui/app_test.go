package tui

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opencoderouter/internal/tui/components"
	"opencoderouter/internal/tui/config"
	"opencoderouter/internal/tui/model"

	tea "charm.land/bubbletea/v2"
)

type fakeDiscoverer struct {
	hosts []model.Host
	err   error
}

func (f fakeDiscoverer) Discover(_ context.Context) ([]model.Host, error) {
	return append([]model.Host(nil), f.hosts...), f.err
}

type fakeProber struct{}

func (fakeProber) ProbeHosts(_ context.Context, hosts []model.Host) ([]model.Host, error) {
	if len(hosts) == 0 {
		return nil, nil
	}
	host := hosts[0]
	host.Status = model.HostStatusOnline
	host.Projects = []model.Project{{
		Name: "alpha",
		Sessions: []model.Session{{
			ID:           "session-1",
			Project:      "alpha",
			Title:        "Smoke session",
			LastActivity: time.Now(),
			Status:       model.SessionStatusActive,
			Activity:     model.ActivityActive,
			MessageCount: 1,
			Agents:       []string{"coder"},
		}},
	}}
	return []model.Host{host}, nil
}

func TestAppSmoke(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Display.Animation = false

	app := NewApp(cfg, fakeDiscoverer{hosts: []model.Host{{Name: "dev-1", Label: "dev-1"}}}, fakeProber{}, nil)
	initCmd := app.Init()
	if initCmd == nil {
		t.Fatal("expected init command")
	}

	if msg := initCmd(); msg != nil {
		_, _ = app.Update(msg)
	}

	_, _ = app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	view := app.View()
	if strings.TrimSpace(view.Content) == "" {
		t.Fatal("expected non-empty view output")
	}
}

func TestNewApp_NilLoggerDefaultsToDiscard(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()

	app := NewApp(cfg, fakeDiscoverer{}, fakeProber{}, nil)
	if app == nil {
		t.Fatal("expected app to be constructed")
	}
	if app.logger == nil {
		t.Fatal("expected app.logger to be non-nil when input logger is nil")
	}
}

func TestNewApp_LoggerPropagated(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	app := NewApp(cfg, fakeDiscoverer{}, fakeProber{}, logger)
	if app.logger == nil {
		t.Fatal("expected app logger to be initialized")
	}

	_ = app.Init()
	if !strings.Contains(buf.String(), "component=app") {
		t.Fatal("expected app logger output to include component field")
	}
}

func setupReloadSessionsMockSSH(t *testing.T, mode string) (model.Host, string) {
	t.Helper()

	binDir := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "ssh-args.txt")
	sshPath := filepath.Join(binDir, "ssh")

	sshScript := `#!/bin/sh
set -eu

if [ -n "${RELOAD_SESSIONS_ARGS_FILE:-}" ]; then
  : >"$RELOAD_SESSIONS_ARGS_FILE"
  for arg in "$@"; do
    printf '%s\n' "$arg" >>"$RELOAD_SESSIONS_ARGS_FILE"
  done
fi

case "${RELOAD_SESSIONS_MOCK_MODE:-success}" in
  success)
    printf 'reload:killed:2\n'
    printf 'reload:remaining:0\n'
    exit 0
    ;;
  none)
    printf 'reload:killed:0\n'
    printf 'reload:remaining:0\n'
    exit 0
    ;;
  residual)
    printf 'reload:killed:1\n'
    printf 'reload:remaining:1\n'
    exit 0
    ;;
  sshfail)
    printf 'ssh transport failed\n' >&2
    exit 255
    ;;
  denied)
    printf 'Permission denied (publickey).\n' >&2
    exit 255
    ;;
  *)
    printf 'unsupported mode %s\n' "${RELOAD_SESSIONS_MOCK_MODE:-}" >&2
    exit 9
    ;;
esac
`

	if err := os.WriteFile(sshPath, []byte(sshScript), 0o755); err != nil {
		t.Fatalf("write mock ssh: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RELOAD_SESSIONS_MOCK_MODE", mode)
	t.Setenv("RELOAD_SESSIONS_ARGS_FILE", argsFile)

	return model.Host{Name: "mock-host"}, argsFile
}

func setupDeleteSessionMockSSH(t *testing.T, mode string) (model.Host, string) {
	t.Helper()

	binDir := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "ssh-delete-args.txt")
	sshPath := filepath.Join(binDir, "ssh")

	sshScript := `#!/bin/sh
set -eu

if [ -n "${DELETE_SESSION_ARGS_FILE:-}" ]; then
  {
    printf '__CALL__\n'
    for arg in "$@"; do
      printf '%s\n' "$arg"
    done
  } >>"$DELETE_SESSION_ARGS_FILE"
fi

remote_cmd=""
for arg in "$@"; do
  remote_cmd="$arg"
done

	case "${DELETE_SESSION_MOCK_MODE:-success}" in
  success)
    case "$remote_cmd" in
      *" session delete "*)
        exit 0
        ;;
      *" export "*)
        printf '{"id":"session-1","title":"example"}\n'
        exit 0
        ;;
      *"delete:session-grep:remaining:"*)
        printf 'delete:session-grep:killed:1\n'
        printf 'delete:session-grep:remaining:0\n'
        exit 0
        ;;
    esac
    exit 0
    ;;
  exportfail)
    case "$remote_cmd" in
      *" session delete "*)
        exit 0
        ;;
      *" export "*)
        printf 'export failed\n' >&2
        exit 17
        ;;
      *"delete:session-grep:remaining:"*)
        printf 'delete:session-grep:killed:0\n'
        printf 'delete:session-grep:remaining:0\n'
        exit 0
        ;;
    esac
    exit 0
    ;;
  deletefail)
    case "$remote_cmd" in
      *" session delete "*)
        printf 'delete failed\n' >&2
        exit 19
        ;;
      *" export "*)
        printf '{"id":"session-1","title":"example"}\n'
        exit 0
        ;;
      *"delete:session-grep:remaining:"*)
        printf 'delete:session-grep:killed:0\n'
        printf 'delete:session-grep:remaining:0\n'
        exit 0
        ;;
    esac
    exit 0
    ;;
  cleanupfail)
    case "$remote_cmd" in
      *" session delete "*)
        exit 0
        ;;
      *" export "*)
        printf '{"id":"session-1","title":"example"}\n'
        exit 0
        ;;
      *"delete:session-grep:remaining:"*)
        printf 'delete:session-grep:killed:0\n'
        printf 'delete:session-grep:remaining:1\n'
        printf 'still running\n' >&2
        exit 21
        ;;
    esac
    exit 0
    ;;
  *)
    printf 'unsupported mode %s\n' "${DELETE_SESSION_MOCK_MODE:-}" >&2
    exit 9
    ;;
esac
`

	if err := os.WriteFile(sshPath, []byte(sshScript), 0o755); err != nil {
		t.Fatalf("write mock ssh: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DELETE_SESSION_MOCK_MODE", mode)
	t.Setenv("DELETE_SESSION_ARGS_FILE", argsFile)

	return model.Host{Name: "mock-host"}, argsFile
}

func TestReloadSessionsCmd_Success(t *testing.T) {
	app := NewApp(config.DefaultConfig(), fakeDiscoverer{}, fakeProber{}, nil)
	host, argsFile := setupReloadSessionsMockSSH(t, "success")
	directory := "/tmp/project-alpha"

	msg := app.reloadSessionsCmd(host, directory)()
	finished, ok := msg.(model.ReloadSessionsFinishedMsg)
	if !ok {
		t.Fatalf("message type = %T, want model.ReloadSessionsFinishedMsg", msg)
	}

	if finished.Err != nil {
		t.Fatalf("Err = %v, want nil", finished.Err)
	}
	if finished.HostName != host.Name {
		t.Fatalf("HostName = %q, want %q", finished.HostName, host.Name)
	}
	if finished.Directory != directory {
		t.Fatalf("Directory = %q, want %q", finished.Directory, directory)
	}
	if finished.KilledCount != 2 {
		t.Fatalf("KilledCount = %d, want 2", finished.KilledCount)
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args file: %v", err)
	}

	argsText := strings.TrimSpace(string(rawArgs))
	if argsText == "" {
		t.Fatal("expected ssh invocation args to be captured")
	}
	args := strings.Split(argsText, "\n")
	if len(args) < 7 {
		t.Fatalf("ssh args length = %d, want >= 7", len(args))
	}

	wantPrefix := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-t", host.Name}
	for i, want := range wantPrefix {
		if args[i] != want {
			t.Fatalf("ssh arg[%d] = %q, want %q", i, args[i], want)
		}
	}

	if !strings.Contains(argsText, "pgrep -f 'opencode serve'") {
		t.Fatalf("remote command missing strict opencode serve search: %q", argsText)
	}
	if !strings.Contains(argsText, "/proc/$pid/cwd") {
		t.Fatalf("remote command missing cwd inspection: %q", argsText)
	}
	if !strings.Contains(argsText, "kill") {
		t.Fatalf("remote command missing kill call: %q", argsText)
	}
	if !strings.Contains(argsText, "reload:killed:") {
		t.Fatalf("remote command missing reload marker: %q", argsText)
	}
	if !strings.Contains(argsText, "remaining=0") {
		t.Fatalf("remote command missing post-kill verification sweep: %q", argsText)
	}
	if !strings.Contains(argsText, "reload:remaining:") {
		t.Fatalf("remote command missing remaining marker: %q", argsText)
	}
}

func TestReloadSessionsCmd_NoProcessFound(t *testing.T) {
	app := NewApp(config.DefaultConfig(), fakeDiscoverer{}, fakeProber{}, nil)
	host, _ := setupReloadSessionsMockSSH(t, "none")
	directory := "/tmp/project-beta"

	msg := app.reloadSessionsCmd(host, directory)()
	finished, ok := msg.(model.ReloadSessionsFinishedMsg)
	if !ok {
		t.Fatalf("message type = %T, want model.ReloadSessionsFinishedMsg", msg)
	}

	if finished.Err != nil {
		t.Fatalf("Err = %v, want nil", finished.Err)
	}
	if finished.HostName != host.Name {
		t.Fatalf("HostName = %q, want %q", finished.HostName, host.Name)
	}
	if finished.Directory != directory {
		t.Fatalf("Directory = %q, want %q", finished.Directory, directory)
	}
	if finished.KilledCount != 0 {
		t.Fatalf("KilledCount = %d, want 0", finished.KilledCount)
	}
}

func TestReloadSessionsCmd_ResidualProcessRemaining(t *testing.T) {
	app := NewApp(config.DefaultConfig(), fakeDiscoverer{}, fakeProber{}, nil)
	host, _ := setupReloadSessionsMockSSH(t, "residual")
	directory := "/tmp/project-residual"

	msg := app.reloadSessionsCmd(host, directory)()
	finished, ok := msg.(model.ReloadSessionsFinishedMsg)
	if !ok {
		t.Fatalf("message type = %T, want model.ReloadSessionsFinishedMsg", msg)
	}

	if finished.Err == nil {
		t.Fatal("Err = nil, want non-nil when residual processes remain")
	}
	if !strings.Contains(strings.ToLower(finished.Err.Error()), "remain") {
		t.Fatalf("Err = %q, want residual/remain context", finished.Err.Error())
	}
	if finished.HostName != host.Name {
		t.Fatalf("HostName = %q, want %q", finished.HostName, host.Name)
	}
	if finished.Directory != directory {
		t.Fatalf("Directory = %q, want %q", finished.Directory, directory)
	}
	if finished.KilledCount != 1 {
		t.Fatalf("KilledCount = %d, want 1", finished.KilledCount)
	}
}

func TestReloadSessionsCmd_SSHFailure(t *testing.T) {
	app := NewApp(config.DefaultConfig(), fakeDiscoverer{}, fakeProber{}, nil)
	host, _ := setupReloadSessionsMockSSH(t, "sshfail")
	directory := "/tmp/project-gamma"

	msg := app.reloadSessionsCmd(host, directory)()
	finished, ok := msg.(model.ReloadSessionsFinishedMsg)
	if !ok {
		t.Fatalf("message type = %T, want model.ReloadSessionsFinishedMsg", msg)
	}

	if finished.Err == nil {
		t.Fatal("Err = nil, want non-nil")
	}
	if finished.HostName != host.Name {
		t.Fatalf("HostName = %q, want %q", finished.HostName, host.Name)
	}
	if finished.Directory != directory {
		t.Fatalf("Directory = %q, want %q", finished.Directory, directory)
	}
	if finished.KilledCount != 0 {
		t.Fatalf("KilledCount = %d, want 0", finished.KilledCount)
	}
}

func TestReloadSessionsCmd_PermissionDenied(t *testing.T) {
	app := NewApp(config.DefaultConfig(), fakeDiscoverer{}, fakeProber{}, nil)
	host, _ := setupReloadSessionsMockSSH(t, "denied")
	directory := "/tmp/project-delta"

	msg := app.reloadSessionsCmd(host, directory)()
	finished, ok := msg.(model.ReloadSessionsFinishedMsg)
	if !ok {
		t.Fatalf("message type = %T, want model.ReloadSessionsFinishedMsg", msg)
	}

	if finished.Err == nil {
		t.Fatal("Err = nil, want non-nil")
	}
	if finished.HostName != host.Name {
		t.Fatalf("HostName = %q, want %q", finished.HostName, host.Name)
	}
	if finished.Directory != directory {
		t.Fatalf("Directory = %q, want %q", finished.Directory, directory)
	}
	if finished.KilledCount != 0 {
		t.Fatalf("KilledCount = %d, want 0", finished.KilledCount)
	}
}

func TestKillSessionCmd_SaveContextExportsThenDeletes(t *testing.T) {
	app := NewApp(config.DefaultConfig(), fakeDiscoverer{}, fakeProber{}, nil)
	host, argsFile := setupDeleteSessionMockSSH(t, "success")
	t.Setenv("HOME", t.TempDir())

	msg := app.killSessionCmd(host, "session-1", "/tmp/project-alpha", true)()
	finished, ok := msg.(model.KillSessionFinishedMsg)
	if !ok {
		t.Fatalf("message type = %T, want model.KillSessionFinishedMsg", msg)
	}
	if finished.Err != nil {
		t.Fatalf("Err = %v, want nil", finished.Err)
	}
	if strings.TrimSpace(finished.SavedExportPath) == "" {
		t.Fatal("expected SavedExportPath when save context is enabled")
	}

	exportJSON, err := os.ReadFile(finished.SavedExportPath)
	if err != nil {
		t.Fatalf("read saved export: %v", err)
	}
	if !strings.Contains(string(exportJSON), "\"id\":\"session-1\"") {
		t.Fatalf("unexpected saved export payload: %q", string(exportJSON))
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args file: %v", err)
	}
	argsText := string(rawArgs)
	if strings.Count(argsText, "__CALL__") != 3 {
		t.Fatalf("expected three ssh calls (export + delete + cleanup), got %d in %q", strings.Count(argsText, "__CALL__"), argsText)
	}
	if !strings.Contains(argsText, " export ") {
		t.Fatalf("expected export command invocation, got %q", argsText)
	}
	if !strings.Contains(argsText, "session delete") {
		t.Fatalf("expected delete command invocation, got %q", argsText)
	}
	if !strings.Contains(argsText, "delete:session-grep:remaining:") {
		t.Fatalf("expected cleanup verification command invocation, got %q", argsText)
	}
	if !strings.Contains(argsText, "grep -F -- \"$SESSION_ID\"") {
		t.Fatalf("expected cleanup grep by session id pattern, got %q", argsText)
	}
	if !strings.Contains(argsText, "kill -15") {
		t.Fatalf("expected cleanup SIGTERM kill -15 pattern, got %q", argsText)
	}
	if strings.Index(argsText, " export ") > strings.Index(argsText, "session delete") {
		t.Fatalf("expected export command before delete command, got %q", argsText)
	}
	if strings.Index(argsText, "session delete") > strings.Index(argsText, "delete:session-grep:remaining:") {
		t.Fatalf("expected cleanup command after delete command, got %q", argsText)
	}
}

func TestKillSessionCmd_DeleteWithoutSaveSkipsExport(t *testing.T) {
	app := NewApp(config.DefaultConfig(), fakeDiscoverer{}, fakeProber{}, nil)
	host, argsFile := setupDeleteSessionMockSSH(t, "success")

	msg := app.killSessionCmd(host, "session-1", "/tmp/project-alpha", false)()
	finished, ok := msg.(model.KillSessionFinishedMsg)
	if !ok {
		t.Fatalf("message type = %T, want model.KillSessionFinishedMsg", msg)
	}
	if finished.Err != nil {
		t.Fatalf("Err = %v, want nil", finished.Err)
	}
	if finished.SavedExportPath != "" {
		t.Fatalf("SavedExportPath = %q, want empty when save disabled", finished.SavedExportPath)
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args file: %v", err)
	}
	argsText := string(rawArgs)
	if strings.Count(argsText, "__CALL__") != 2 {
		t.Fatalf("expected two ssh calls (delete + cleanup), got %d in %q", strings.Count(argsText, "__CALL__"), argsText)
	}
	if strings.Contains(argsText, " export ") {
		t.Fatalf("did not expect export command invocation when save disabled, got %q", argsText)
	}
	if !strings.Contains(argsText, "session delete") {
		t.Fatalf("expected delete command invocation, got %q", argsText)
	}
	if !strings.Contains(argsText, "delete:session-grep:remaining:") {
		t.Fatalf("expected cleanup verification command invocation, got %q", argsText)
	}
}

func TestKillSessionCmd_SaveContextExportFailureStopsDelete(t *testing.T) {
	app := NewApp(config.DefaultConfig(), fakeDiscoverer{}, fakeProber{}, nil)
	host, argsFile := setupDeleteSessionMockSSH(t, "exportfail")
	t.Setenv("HOME", t.TempDir())

	msg := app.killSessionCmd(host, "session-1", "/tmp/project-alpha", true)()
	finished, ok := msg.(model.KillSessionFinishedMsg)
	if !ok {
		t.Fatalf("message type = %T, want model.KillSessionFinishedMsg", msg)
	}
	if finished.Err == nil {
		t.Fatal("Err = nil, want export error")
	}
	if !strings.Contains(strings.ToLower(finished.Err.Error()), "export session") {
		t.Fatalf("expected export error context, got %q", finished.Err.Error())
	}
	if finished.SavedExportPath != "" {
		t.Fatalf("SavedExportPath = %q, want empty on export failure", finished.SavedExportPath)
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args file: %v", err)
	}
	argsText := string(rawArgs)
	if strings.Count(argsText, "__CALL__") != 1 {
		t.Fatalf("expected one ssh call when export fails, got %d in %q", strings.Count(argsText, "__CALL__"), argsText)
	}
	if strings.Contains(argsText, "session delete") {
		t.Fatalf("delete command should not run after export failure, got %q", argsText)
	}
	if strings.Contains(argsText, "delete:session-grep:remaining:") {
		t.Fatalf("cleanup command should not run after export failure, got %q", argsText)
	}
}

func TestKillSessionCmd_DeleteFailureReturnsSavedExportPath(t *testing.T) {
	app := NewApp(config.DefaultConfig(), fakeDiscoverer{}, fakeProber{}, nil)
	host, argsFile := setupDeleteSessionMockSSH(t, "deletefail")
	t.Setenv("HOME", t.TempDir())

	msg := app.killSessionCmd(host, "session-1", "/tmp/project-alpha", true)()
	finished, ok := msg.(model.KillSessionFinishedMsg)
	if !ok {
		t.Fatalf("message type = %T, want model.KillSessionFinishedMsg", msg)
	}
	if finished.Err == nil {
		t.Fatal("Err = nil, want delete error")
	}
	if !strings.Contains(strings.ToLower(finished.Err.Error()), "delete session") {
		t.Fatalf("expected delete error context, got %q", finished.Err.Error())
	}
	if strings.TrimSpace(finished.SavedExportPath) == "" {
		t.Fatal("expected SavedExportPath when delete fails after successful export")
	}

	if _, err := os.Stat(finished.SavedExportPath); err != nil {
		t.Fatalf("expected export file to exist despite delete failure: %v", err)
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args file: %v", err)
	}
	argsText := string(rawArgs)
	if strings.Count(argsText, "__CALL__") != 2 {
		t.Fatalf("expected two ssh calls when delete fails, got %d in %q", strings.Count(argsText, "__CALL__"), argsText)
	}
	if !strings.Contains(argsText, " export ") || !strings.Contains(argsText, "session delete") {
		t.Fatalf("expected both export and delete invocations, got %q", argsText)
	}
	if strings.Contains(argsText, "delete:session-grep:remaining:") {
		t.Fatalf("cleanup command should not run when delete fails, got %q", argsText)
	}
}

func TestKillSessionCmd_CleanupFailureReturnsSavedExportPath(t *testing.T) {
	app := NewApp(config.DefaultConfig(), fakeDiscoverer{}, fakeProber{}, nil)
	host, argsFile := setupDeleteSessionMockSSH(t, "cleanupfail")
	t.Setenv("HOME", t.TempDir())

	msg := app.killSessionCmd(host, "session-1", "/tmp/project-alpha", true)()
	finished, ok := msg.(model.KillSessionFinishedMsg)
	if !ok {
		t.Fatalf("message type = %T, want model.KillSessionFinishedMsg", msg)
	}
	if finished.Err == nil {
		t.Fatal("Err = nil, want cleanup verification error")
	}
	if !strings.Contains(strings.ToLower(finished.Err.Error()), "verify remote session process cleanup") {
		t.Fatalf("expected cleanup verification error context, got %q", finished.Err.Error())
	}
	if strings.TrimSpace(finished.SavedExportPath) == "" {
		t.Fatal("expected SavedExportPath when cleanup fails after successful export")
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args file: %v", err)
	}
	argsText := string(rawArgs)
	if strings.Count(argsText, "__CALL__") != 3 {
		t.Fatalf("expected three ssh calls when cleanup fails, got %d in %q", strings.Count(argsText, "__CALL__"), argsText)
	}
	if !strings.Contains(argsText, " export ") || !strings.Contains(argsText, "session delete") || !strings.Contains(argsText, "delete:session-grep:remaining:") {
		t.Fatalf("expected export, delete, and cleanup invocations, got %q", argsText)
	}
}

func setupReloadWiringApp(t *testing.T, host model.Host) (*AppModel, *fakeAppSessionManager) {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.Display.Animation = false

	app := NewApp(cfg, fakeDiscoverer{}, fakeProber{}, nil)
	manager := newFakeAppSessionManager()
	app.sessionManager = manager
	app.hosts = []model.Host{host}
	app.tree.SetHosts(app.hosts)

	_, _ = app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	return app, manager
}

func ctrlRKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}
}

func TestCtrlR_ProjectSelectionOpensReloadConfirmModal(t *testing.T) {
	host := model.Host{
		Name:   "dev-1",
		Label:  "dev-1",
		Status: model.HostStatusOnline,
		Projects: []model.Project{{
			Name: "alpha",
			Sessions: []model.Session{{
				ID:        "alpha-1",
				Project:   "alpha",
				Directory: "/srv/work/alpha",
			}},
		}},
	}

	app, _ := setupReloadWiringApp(t, host)
	_, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyDown})

	_, _ = app.Update(ctrlRKey())

	if !app.modal.Active() {
		t.Fatal("expected reload confirm modal to be active")
	}
	if app.modal.Type() != components.ModalTypeConfirmReload {
		t.Fatalf("modal type = %q, want %q", app.modal.Type(), components.ModalTypeConfirmReload)
	}
	if !strings.Contains(app.modal.View(), "/srv/work/alpha") {
		t.Fatalf("reload modal should include selected project directory, got %q", app.modal.View())
	}
	if app.toast.Visible() {
		t.Fatal("project reload should open modal, not warning toast")
	}
}

func TestCtrlR_HostOnlySelectionShowsWarningToastNoModal(t *testing.T) {
	host := model.Host{
		Name:   "dev-1",
		Label:  "dev-1",
		Status: model.HostStatusOnline,
		Projects: []model.Project{{
			Name: "alpha",
			Sessions: []model.Session{{
				ID:        "alpha-1",
				Project:   "alpha",
				Directory: "/srv/work/alpha",
			}},
		}},
	}

	app, _ := setupReloadWiringApp(t, host)

	_, cmd := app.Update(ctrlRKey())
	if cmd == nil {
		t.Fatal("expected warning toast command for host-only reload attempt")
	}

	if app.modal.Active() {
		t.Fatal("host-only reload should not open confirm modal")
	}
	if !app.toast.Visible() {
		t.Fatal("expected warning toast for host-only reload attempt")
	}

	toastView := strings.ToLower(app.toast.View())
	if !strings.Contains(toastView, "warning:") {
		t.Fatalf("expected warning toast, got %q", toastView)
	}
	if !strings.Contains(toastView, "select a project or session") {
		t.Fatalf("expected host-only guidance in toast, got %q", toastView)
	}
}

func TestCtrlR_SessionSelectionResolvesParentProjectDirectory(t *testing.T) {
	host := model.Host{
		Name:   "dev-1",
		Label:  "dev-1",
		Status: model.HostStatusOnline,
		Projects: []model.Project{{
			Name: "alpha",
			Sessions: []model.Session{{
				ID:        "alpha-1",
				Project:   "alpha",
				Directory: "/srv/work/alpha/.opencode/sessions/alpha-1",
			}},
		}},
	}

	app, _ := setupReloadWiringApp(t, host)
	_, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	_, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyDown})

	_, _ = app.Update(ctrlRKey())
	if !app.modal.Active() {
		t.Fatal("expected reload confirm modal on session selection")
	}

	_, cmd := app.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil {
		t.Fatal("expected confirm command from reload modal")
	}

	msg := cmd()
	confirm, ok := msg.(model.ModalConfirmReloadMsg)
	if !ok {
		t.Fatalf("message type = %T, want model.ModalConfirmReloadMsg", msg)
	}
	if confirm.HostName != host.Name {
		t.Fatalf("HostName = %q, want %q", confirm.HostName, host.Name)
	}
	if confirm.Directory != "/srv/work/alpha" {
		t.Fatalf("Directory = %q, want %q", confirm.Directory, "/srv/work/alpha")
	}
}

func TestCtrlR_IgnoredWhenReloadInProgress(t *testing.T) {
	host := model.Host{
		Name:   "dev-1",
		Label:  "dev-1",
		Status: model.HostStatusOnline,
		Projects: []model.Project{{
			Name: "alpha",
			Sessions: []model.Session{{
				ID:        "alpha-1",
				Project:   "alpha",
				Directory: "/srv/work/alpha",
			}},
		}},
	}

	app, _ := setupReloadWiringApp(t, host)
	_, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	app.reloadInProgress = true

	_, cmd := app.Update(ctrlRKey())
	if cmd != nil {
		t.Fatalf("expected ctrl+r to be ignored while reloading, got cmd %v", cmd)
	}
	if app.modal.Active() {
		t.Fatal("ctrl+r should not open modal while reload is in progress")
	}
	if app.toast.Visible() {
		t.Fatal("ctrl+r ignore path should not show toast")
	}
}

func TestReloadConfirm_DetachesActiveProjectTerminalsBeforeDispatch(t *testing.T) {
	host := model.Host{
		Name:   "dev-1",
		Label:  "dev-1",
		Status: model.HostStatusOnline,
		Projects: []model.Project{
			{
				Name: "alpha",
				Sessions: []model.Session{
					{ID: "alpha-1", Project: "alpha", Directory: "/srv/work/alpha"},
					{ID: "alpha-2", Project: "alpha", Directory: "/srv/work/alpha/.opencode/sessions/alpha-2"},
				},
			},
			{
				Name: "beta",
				Sessions: []model.Session{
					{ID: "beta-1", Project: "beta", Directory: "/srv/work/beta"},
				},
			},
		},
	}

	app, manager := setupReloadWiringApp(t, host)
	manager.terminals["alpha-1"] = &fakeAppTerminal{viewOutput: "alpha-1"}
	manager.terminals["alpha-2"] = &fakeAppTerminal{viewOutput: "alpha-2"}
	manager.terminals["beta-1"] = &fakeAppTerminal{viewOutput: "beta-1"}

	app.activeView = viewTerminal
	app.activeSessionID = "alpha-2"

	_, cmd := app.Update(model.ModalConfirmReloadMsg{HostName: host.Name, Directory: "/srv/work/alpha"})
	if cmd == nil {
		t.Fatal("expected reload dispatch command after confirmation")
	}
	if !app.reloadInProgress {
		t.Fatal("reloadInProgress should be true after reload confirmation")
	}

	if manager.Get("alpha-1") != nil {
		t.Fatal("expected alpha-1 terminal to be detached before reload command dispatch")
	}
	if manager.Get("alpha-2") != nil {
		t.Fatal("expected alpha-2 terminal to be detached before reload command dispatch")
	}
	if manager.Get("beta-1") == nil {
		t.Fatal("expected non-target project terminal to remain attached")
	}
	if app.activeView != viewTree {
		t.Fatalf("activeView = %v, want %v after detaching active terminal", app.activeView, viewTree)
	}
	if app.activeSessionID != "" {
		t.Fatalf("activeSessionID = %q, want empty after detaching active terminal", app.activeSessionID)
	}
}

func TestReloadConfirm_DispatchesReloadSessionsCmd(t *testing.T) {
	mockHost, argsFile := setupReloadSessionsMockSSH(t, "none")
	mockHost.Status = model.HostStatusOnline
	mockHost.Projects = []model.Project{{
		Name: "project-alpha",
		Sessions: []model.Session{{
			ID:        "alpha-1",
			Project:   "project-alpha",
			Directory: "/tmp/project-alpha",
		}},
	}}

	app, _ := setupReloadWiringApp(t, mockHost)

	_, cmd := app.Update(model.ModalConfirmReloadMsg{HostName: mockHost.Name, Directory: "/tmp/project-alpha"})
	if cmd == nil {
		t.Fatal("expected reloadSessionsCmd dispatch on modal confirm")
	}

	msg := cmd()
	finished, ok := msg.(model.ReloadSessionsFinishedMsg)
	if !ok {
		t.Fatalf("message type = %T, want model.ReloadSessionsFinishedMsg", msg)
	}
	if finished.Err != nil {
		t.Fatalf("Err = %v, want nil", finished.Err)
	}
	if finished.HostName != mockHost.Name {
		t.Fatalf("HostName = %q, want %q", finished.HostName, mockHost.Name)
	}
	if finished.Directory != "/tmp/project-alpha" {
		t.Fatalf("Directory = %q, want %q", finished.Directory, "/tmp/project-alpha")
	}
	if finished.KilledCount != 0 {
		t.Fatalf("KilledCount = %d, want 0", finished.KilledCount)
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args file: %v", err)
	}
	if !strings.Contains(string(rawArgs), "/tmp/project-alpha") {
		t.Fatalf("ssh command args should include target directory, got %q", string(rawArgs))
	}
}

func TestReloadFinished_SuccessUpdatesToastAndRefresh(t *testing.T) {
	host := model.Host{Name: "dev-1", Label: "dev-1", Status: model.HostStatusOnline}
	app, _ := setupReloadWiringApp(t, host)
	app.reloadInProgress = true

	_, cmd := app.Update(model.ReloadSessionsFinishedMsg{
		HostName:    host.Name,
		Directory:   "/srv/work/alpha",
		KilledCount: 2,
	})

	if app.reloadInProgress {
		t.Fatal("reloadInProgress should reset to false after reload finished")
	}
	if !app.toast.Visible() {
		t.Fatal("expected success toast after reload completion")
	}
	toastView := strings.ToLower(app.toast.View())
	if !strings.Contains(toastView, "info:") {
		t.Fatalf("expected info toast on reload success, got %q", toastView)
	}
	if !strings.Contains(toastView, "reloaded") {
		t.Fatalf("expected success reload message, got %q", toastView)
	}

	if cmd == nil {
		t.Fatal("expected combined toast+refresh command on reload success")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("command result type = %T, want tea.BatchMsg", msg)
	}
	if len(batch) != 2 {
		t.Fatalf("batch command length = %d, want 2 (toast + refresh)", len(batch))
	}
	if !batchContainsProbeResult(batch) {
		t.Fatal("expected reload success command batch to include refreshCmd")
	}
}

func TestReloadFinished_ErrorUpdatesToastAndRefresh(t *testing.T) {
	host := model.Host{Name: "dev-1", Label: "dev-1", Status: model.HostStatusOnline}
	app, _ := setupReloadWiringApp(t, host)
	app.reloadInProgress = true

	reloadErr := errors.New("reload failed: ssh timeout")
	_, cmd := app.Update(model.ReloadSessionsFinishedMsg{
		HostName:  host.Name,
		Directory: "/srv/work/alpha",
		Err:       reloadErr,
	})

	if app.reloadInProgress {
		t.Fatal("reloadInProgress should reset to false after reload error")
	}
	if app.lastError == nil || !strings.Contains(app.lastError.Error(), "ssh timeout") {
		t.Fatalf("expected lastError to capture reload failure, got %v", app.lastError)
	}
	if !app.toast.Visible() {
		t.Fatal("expected error toast after reload failure")
	}
	toastView := strings.ToLower(app.toast.View())
	if !strings.Contains(toastView, "error:") {
		t.Fatalf("expected error toast on reload failure, got %q", toastView)
	}
	if !strings.Contains(toastView, "ssh timeout") {
		t.Fatalf("expected reload error details in toast, got %q", toastView)
	}

	if cmd == nil {
		t.Fatal("expected combined toast+refresh command on reload error")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("command result type = %T, want tea.BatchMsg", msg)
	}
	if len(batch) != 2 {
		t.Fatalf("batch command length = %d, want 2 (toast + refresh)", len(batch))
	}
	if !batchContainsProbeResult(batch) {
		t.Fatal("expected reload error command batch to include refreshCmd")
	}
}

func batchContainsProbeResult(cmds tea.BatchMsg) bool {
	for _, cmd := range cmds {
		if cmd == nil {
			continue
		}

		msg, ok := runCmdWithTimeout(cmd, 100*time.Millisecond)
		if !ok {
			continue
		}

		if _, isProbe := msg.(model.ProbeResultMsg); isProbe {
			return true
		}
	}

	return false
}

func runCmdWithTimeout(cmd tea.Cmd, timeout time.Duration) (tea.Msg, bool) {
	if cmd == nil {
		return nil, false
	}

	type result struct {
		msg tea.Msg
	}
	ch := make(chan result, 1)

	go func() {
		ch <- result{msg: cmd()}
	}()

	select {
	case out := <-ch:
		return out.msg, true
	case <-time.After(timeout):
		return nil, false
	}
}
