package tui

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"opencoderouter/internal/tui/config"
	"opencoderouter/internal/tui/model"
	"opencoderouter/internal/tui/session"

	tea "charm.land/bubbletea/v2"
)

type appResizeCall struct {
	width  int
	height int
}

type fakeAppTerminal struct {
	mu          sync.Mutex
	writes      [][]byte
	resizeCalls []appResizeCall
	closed      bool
	err         error
	viewOutput  string
}

func (f *fakeAppTerminal) View() string {
	f.mu.Lock()
	view := f.viewOutput
	f.mu.Unlock()
	return view
}

func (f *fakeAppTerminal) WriteInput(data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.closed {
		return f.err
	}

	f.writes = append(f.writes, append([]byte(nil), data...))
	return nil
}

func (f *fakeAppTerminal) Resize(width, height int) error {
	f.mu.Lock()
	f.resizeCalls = append(f.resizeCalls, appResizeCall{width: width, height: height})
	f.mu.Unlock()
	return nil
}

func (f *fakeAppTerminal) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

func (f *fakeAppTerminal) IsClosed() bool {
	f.mu.Lock()
	closed := f.closed
	f.mu.Unlock()
	return closed
}

func (f *fakeAppTerminal) Err() error {
	f.mu.Lock()
	err := f.err
	f.mu.Unlock()
	return err
}

func (f *fakeAppTerminal) writesSnapshot() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([][]byte, 0, len(f.writes))
	for _, write := range f.writes {
		out = append(out, append([]byte(nil), write...))
	}
	return out
}

type attachCall struct {
	host       model.Host
	session    model.Session
	width      int
	height     int
	callNumber int
}

type fakeAppSessionManager struct {
	mu sync.Mutex

	terminals      map[string]*fakeAppTerminal
	attachCalls    []attachCall
	resizeCalls    []appResizeCall
	shutdownCalls  int
	cleanupCalls   int
	attachCallSeed int
	attachErr      error
}

func newFakeAppSessionManager() *fakeAppSessionManager {
	return &fakeAppSessionManager{terminals: make(map[string]*fakeAppTerminal)}
}

func (f *fakeAppSessionManager) Attach(host model.Host, sessionData model.Session, width, height int) (session.Terminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.attachCallSeed++
	f.attachCalls = append(f.attachCalls, attachCall{
		host:       host,
		session:    sessionData,
		width:      width,
		height:     height,
		callNumber: f.attachCallSeed,
	})

	if f.attachErr != nil {
		return nil, f.attachErr
	}

	if existing := f.terminals[sessionData.ID]; existing != nil {
		return existing, nil
	}

	created := &fakeAppTerminal{viewOutput: "terminal:" + sessionData.ID}
	f.terminals[sessionData.ID] = created
	return created, nil
}

func (f *fakeAppSessionManager) Get(sessionID string) session.Terminal {
	f.mu.Lock()
	terminal := f.terminals[sessionID]
	f.mu.Unlock()
	if terminal == nil {
		return nil
	}
	return terminal
}

func (f *fakeAppSessionManager) ResizeAll(width, height int) {
	f.mu.Lock()
	f.resizeCalls = append(f.resizeCalls, appResizeCall{width: width, height: height})
	terminals := make([]*fakeAppTerminal, 0, len(f.terminals))
	for _, terminal := range f.terminals {
		terminals = append(terminals, terminal)
	}
	f.mu.Unlock()

	for _, terminal := range terminals {
		_ = terminal.Resize(width, height)
	}
}

func (f *fakeAppSessionManager) Shutdown() {
	f.mu.Lock()
	f.shutdownCalls++
	terminals := make([]*fakeAppTerminal, 0, len(f.terminals))
	for _, terminal := range f.terminals {
		terminals = append(terminals, terminal)
	}
	f.terminals = make(map[string]*fakeAppTerminal)
	f.mu.Unlock()

	for _, terminal := range terminals {
		_ = terminal.Close()
	}
}

func (f *fakeAppSessionManager) CleanupClosed() {
	f.mu.Lock()
	f.cleanupCalls++
	for id, terminal := range f.terminals {
		if terminal == nil || terminal.IsClosed() {
			delete(f.terminals, id)
		}
	}
	f.mu.Unlock()
}

func (f *fakeAppSessionManager) Remove(sessionID string) {
	f.mu.Lock()
	terminal := f.terminals[sessionID]
	delete(f.terminals, sessionID)
	f.mu.Unlock()
	if terminal != nil {
		_ = terminal.Close()
	}
}

func (f *fakeAppSessionManager) attachCallsSnapshot() []attachCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]attachCall(nil), f.attachCalls...)
}

func (f *fakeAppSessionManager) resizeCallsSnapshot() []appResizeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]appResizeCall(nil), f.resizeCalls...)
}

func (f *fakeAppSessionManager) terminal(sessionID string) *fakeAppTerminal {
	f.mu.Lock()
	terminal := f.terminals[sessionID]
	f.mu.Unlock()
	return terminal
}

func (f *fakeAppSessionManager) shutdownCallCount() int {
	f.mu.Lock()
	count := f.shutdownCalls
	f.mu.Unlock()
	return count
}

func (f *fakeAppSessionManager) cleanupCallCount() int {
	f.mu.Lock()
	count := f.cleanupCalls
	f.mu.Unlock()
	return count
}

func newTerminalIntegrationApp(t *testing.T) (*AppModel, *fakeAppSessionManager, model.Host, model.Session) {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.Display.Animation = false

	app := NewApp(cfg, fakeDiscoverer{}, fakeProber{}, nil)
	manager := newFakeAppSessionManager()
	app.sessionManager = manager

	_, _ = app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	sessionData := model.Session{
		ID:        "sess-1",
		Project:   "alpha",
		Directory: "/tmp/alpha",
		Title:     "alpha session",
	}
	host := model.Host{
		Name:   "dev-1",
		Label:  "dev-1",
		Status: model.HostStatusOnline,
		Projects: []model.Project{{
			Name:     "alpha",
			Sessions: []model.Session{sessionData},
		}},
	}

	app.hosts = []model.Host{host}
	app.tree.SetHosts(app.hosts)

	_, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	_, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyDown})

	return app, manager, host, sessionData
}

func TestAppAttachKeyEntersTerminalView(t *testing.T) {
	app, manager, host, sessionData := newTerminalIntegrationApp(t)

	if app.activeView != viewTree {
		t.Fatalf("initial activeView = %v, want %v", app.activeView, viewTree)
	}

	_, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		_ = cmd
	}

	if app.activeView != viewTerminal {
		t.Fatalf("activeView after attach = %v, want %v", app.activeView, viewTerminal)
	}
	if app.activeSessionID != sessionData.ID {
		t.Fatalf("activeSessionID = %q, want %q", app.activeSessionID, sessionData.ID)
	}

	calls := manager.attachCallsSnapshot()
	if len(calls) != 1 {
		t.Fatalf("attach call count = %d, want 1", len(calls))
	}

	call := calls[0]
	if call.host.Name != host.Name {
		t.Fatalf("attach host = %q, want %q", call.host.Name, host.Name)
	}
	if call.session.ID != sessionData.ID {
		t.Fatalf("attach session = %q, want %q", call.session.ID, sessionData.ID)
	}
	if call.width != 120 || call.height != 40 {
		t.Fatalf("attach size = %dx%d, want 120x40", call.width, call.height)
	}
}

func TestAppDetachKeyReturnsToTreeView(t *testing.T) {
	tests := []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{
			name: "ctrl-modified-right-bracket",
			key:  tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl},
		},
		{
			name: "canonical-control-code",
			key:  tea.KeyPressMsg{Code: 0x1d},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app, manager, _, sessionData := newTerminalIntegrationApp(t)

			if app.activeView != viewTree {
				t.Fatalf("initial activeView = %v, want %v", app.activeView, viewTree)
			}

			_, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

			if app.activeView != viewTerminal {
				t.Fatalf("activeView after attach = %v, want %v", app.activeView, viewTerminal)
			}

			if manager.Get(sessionData.ID) == nil {
				t.Fatal("expected attached terminal in manager before detach")
			}

			_, cmd := app.Update(tc.key)
			if cmd != nil {
				t.Fatalf("detach should not return command, got %v", cmd)
			}

			if app.activeView != viewTree {
				t.Fatalf("activeView after detach = %v, want %v", app.activeView, viewTree)
			}
			if app.activeSessionID != "" {
				t.Fatalf("activeSessionID after detach = %q, want empty", app.activeSessionID)
			}

			if manager.Get(sessionData.ID) == nil {
				t.Fatal("detach should not close or remove attached session")
			}
		})
	}
}

func TestAppTerminalViewForwardsKeysIncludingQ(t *testing.T) {
	app, manager, _, sessionData := newTerminalIntegrationApp(t)
	_, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	terminal := manager.terminal(sessionData.ID)
	if terminal == nil {
		t.Fatal("expected terminal instance after attach")
	}

	_, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("non-detach key should return async forwarding command")
	}
	if msg := cmd(); msg != nil {
		_, _ = app.Update(msg)
	}

	_, cmd = app.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil {
		t.Fatal("'q' in terminal mode should return async forwarding command")
	}
	if msg := cmd(); msg != nil {
		_, _ = app.Update(msg)
	}

	writes := terminal.writesSnapshot()
	if len(writes) != 2 {
		t.Fatalf("forwarded write count = %d, want 2", len(writes))
	}
	if !bytes.Equal(writes[0], []byte("a")) {
		t.Fatalf("first forwarded key = %q, want %q", string(writes[0]), "a")
	}
	if !bytes.Equal(writes[1], []byte("q")) {
		t.Fatalf("second forwarded key = %q, want %q", string(writes[1]), "q")
	}

	if app.activeView != viewTerminal {
		t.Fatalf("activeView changed unexpectedly: got %v, want %v", app.activeView, viewTerminal)
	}

	if manager.shutdownCallCount() != 0 {
		t.Fatalf("terminal 'q' should not trigger shutdown, got %d shutdown calls", manager.shutdownCallCount())
	}
}

func TestAppTerminalClosedMessageSwitchesToTreeAndCleansManager(t *testing.T) {
	app, manager, _, sessionData := newTerminalIntegrationApp(t)
	_, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	terminal := manager.terminal(sessionData.ID)
	if terminal == nil {
		t.Fatal("expected terminal instance after attach")
	}
	_ = terminal.Close()

	_, cmd := app.Update(model.TerminalClosedMsg{SessionID: sessionData.ID})
	if cmd != nil {
		_ = cmd
	}

	if app.activeView != viewTree {
		t.Fatalf("activeView after terminal close = %v, want %v", app.activeView, viewTree)
	}
	if app.activeSessionID != "" {
		t.Fatalf("activeSessionID after terminal close = %q, want empty", app.activeSessionID)
	}

	if manager.cleanupCallCount() == 0 {
		t.Fatal("expected CleanupClosed to be called on terminal close")
	}
	if manager.Get(sessionData.ID) != nil {
		t.Fatal("expected closed session to be removed from manager")
	}
}

func TestAppWindowResizePropagatesToSessionManager(t *testing.T) {
	app, manager, _, _ := newTerminalIntegrationApp(t)

	_, _ = app.Update(tea.WindowSizeMsg{Width: 200, Height: 55})

	calls := manager.resizeCallsSnapshot()
	if len(calls) == 0 {
		t.Fatal("expected ResizeAll to be called")
	}
	last := calls[len(calls)-1]
	if last.width != 200 || last.height != 55 {
		t.Fatalf("last resize call = %dx%d, want 200x55", last.width, last.height)
	}
}

func TestAppQuitShutsDownSessionManager(t *testing.T) {
	app, manager, _, _ := newTerminalIntegrationApp(t)

	_, cmd := app.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil {
		t.Fatal("expected quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("expected tea.QuitMsg from quit command")
	}

	if manager.shutdownCallCount() != 1 {
		t.Fatalf("shutdown call count = %d, want 1", manager.shutdownCallCount())
	}
}

func TestAppReattachReusesExistingBackgroundTerminal(t *testing.T) {
	app, manager, _, sessionData := newTerminalIntegrationApp(t)

	_, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	firstTerminal := manager.terminal(sessionData.ID)
	if firstTerminal == nil {
		t.Fatal("expected terminal instance after first attach")
	}

	_, _ = app.Update(tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl})
	if app.activeView != viewTree {
		t.Fatalf("activeView after detach = %v, want %v", app.activeView, viewTree)
	}

	_, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if app.activeView != viewTerminal {
		t.Fatalf("activeView after re-attach = %v, want %v", app.activeView, viewTerminal)
	}

	secondTerminal := manager.terminal(sessionData.ID)
	if secondTerminal == nil {
		t.Fatal("expected terminal instance after re-attach")
	}
	if firstTerminal != secondTerminal {
		t.Fatal("re-attach should reuse existing background terminal")
	}

	if calls := len(manager.attachCallsSnapshot()); calls != 2 {
		t.Fatalf("attach call count after re-attach = %d, want 2", calls)
	}
}

func TestAppReattachBlankCachedTerminalRefreshesTerminal(t *testing.T) {
	app, manager, _, sessionData := newTerminalIntegrationApp(t)

	_, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	firstTerminal := manager.terminal(sessionData.ID)
	if firstTerminal == nil {
		t.Fatal("expected terminal instance after first attach")
	}

	_, _ = app.Update(tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl})
	if app.activeView != viewTree {
		t.Fatalf("activeView after detach = %v, want %v", app.activeView, viewTree)
	}

	firstTerminal.viewOutput = ""

	_, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if app.activeView != viewTerminal {
		t.Fatalf("activeView after re-attach = %v, want %v", app.activeView, viewTerminal)
	}

	secondTerminal := manager.terminal(sessionData.ID)
	if secondTerminal == nil {
		t.Fatal("expected terminal instance after refresh")
	}
	if firstTerminal == secondTerminal {
		t.Fatal("blank cached terminal should be refreshed on re-attach")
	}

	if calls := len(manager.attachCallsSnapshot()); calls != 3 {
		t.Fatalf("attach call count after blank-terminal refresh = %d, want 3", calls)
	}
}

func TestAppCreateAndGitCloneSessionCommandsUseExecProcess(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}

	appFile := filepath.Join(filepath.Dir(currentFile), "app.go")
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, appFile, nil, 0)
	if err != nil {
		t.Fatalf("parse app.go: %v", err)
	}

	required := map[string]bool{
		"createSessionCmd":   false,
		"gitCloneSessionCmd": false,
	}

	ast.Inspect(parsed, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		if _, tracked := required[fn.Name.Name]; !tracked {
			return true
		}

		required[fn.Name.Name] = functionBodyUsesTeaExecProcess(fn.Body)
		return false
	})

	for name, usesExecProcess := range required {
		if !usesExecProcess {
			t.Fatalf("%s must use tea.ExecProcess", name)
		}
	}
}

func functionBodyUsesTeaExecProcess(body *ast.BlockStmt) bool {
	usesExecProcess := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel == nil || selector.Sel.Name != "ExecProcess" {
			return true
		}

		pkg, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}

		if pkg.Name == "tea" {
			usesExecProcess = true
			return false
		}

		return true
	})

	return usesExecProcess
}
