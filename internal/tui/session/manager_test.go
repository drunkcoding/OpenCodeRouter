package session

import (
	"log/slog"
	"reflect"
	"sync"
	"testing"

	"opencoderouter/internal/tui/model"

	tea "charm.land/bubbletea/v2"
)

type resizeCall struct {
	width  int
	height int
}

type fakeTerminal struct {
	mu          sync.Mutex
	closed      bool
	closeCalls  int
	resizeCalls []resizeCall
}

func (f *fakeTerminal) View() string {
	return ""
}

func (f *fakeTerminal) WriteInput(_ []byte) error {
	return nil
}

func (f *fakeTerminal) Resize(width, height int) error {
	f.mu.Lock()
	f.resizeCalls = append(f.resizeCalls, resizeCall{width: width, height: height})
	f.mu.Unlock()
	return nil
}

func (f *fakeTerminal) Close() error {
	f.mu.Lock()
	f.closed = true
	f.closeCalls++
	f.mu.Unlock()
	return nil
}

func (f *fakeTerminal) IsClosed() bool {
	f.mu.Lock()
	closed := f.closed
	f.mu.Unlock()
	return closed
}

func (f *fakeTerminal) Err() error {
	return nil
}

func (f *fakeTerminal) resizeCallsSnapshot() []resizeCall {
	f.mu.Lock()
	calls := append([]resizeCall(nil), f.resizeCalls...)
	f.mu.Unlock()
	return calls
}

func (f *fakeTerminal) closeCallsSnapshot() int {
	f.mu.Lock()
	calls := f.closeCalls
	f.mu.Unlock()
	return calls
}

type fakeFactory struct {
	mu        sync.Mutex
	calls     map[string]int
	terminals map[string][]*fakeTerminal
}

func newFakeFactory() *fakeFactory {
	return &fakeFactory{
		calls:     make(map[string]int),
		terminals: make(map[string][]*fakeTerminal),
	}
}

func (f *fakeFactory) newTerminal(_ model.Host, session model.Session, _ int, _ int, _ func(tea.Msg), _ *slog.Logger) (Terminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls[session.ID]++
	t := &fakeTerminal{}
	f.terminals[session.ID] = append(f.terminals[session.ID], t)
	return t, nil
}

func (f *fakeFactory) callCount(sessionID string) int {
	f.mu.Lock()
	count := f.calls[sessionID]
	f.mu.Unlock()
	return count
}

func (f *fakeFactory) firstTerminal(sessionID string) *fakeTerminal {
	f.mu.Lock()
	defer f.mu.Unlock()

	list := f.terminals[sessionID]
	if len(list) == 0 {
		return nil
	}
	return list[0]
}

func (f *fakeFactory) allTerminals(sessionID string) []*fakeTerminal {
	f.mu.Lock()
	defer f.mu.Unlock()

	list := f.terminals[sessionID]
	out := make([]*fakeTerminal, 0, len(list))
	out = append(out, list...)
	return out
}

func TestManagerCRUDGetHasActiveIDs(t *testing.T) {
	factory := newFakeFactory()
	m := NewManager(nil, slog.Default())
	m.newTerminal = factory.newTerminal

	host := model.Host{Name: "host-1"}
	session := model.Session{ID: "sess-1"}

	created, err := m.Attach(host, session, 80, 24)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if created == nil {
		t.Fatal("expected created terminal")
	}

	if !m.Has(session.ID) {
		t.Fatalf("expected Has(%q) to be true", session.ID)
	}

	got := m.Get(session.ID)
	if got == nil {
		t.Fatalf("expected Get(%q) to return terminal", session.ID)
	}
	if got != created {
		t.Fatal("expected Get to return attached terminal")
	}

	ids := m.ActiveIDs()
	if !reflect.DeepEqual(ids, []string{session.ID}) {
		t.Fatalf("active ids = %v, want [%s]", ids, session.ID)
	}

	m.Remove(session.ID)

	if m.Has(session.ID) {
		t.Fatalf("expected Has(%q) to be false after remove", session.ID)
	}
	if m.Get(session.ID) != nil {
		t.Fatalf("expected Get(%q) to return nil after remove", session.ID)
	}
	if ids := m.ActiveIDs(); len(ids) != 0 {
		t.Fatalf("expected no active ids after remove, got %v", ids)
	}

	fake := factory.firstTerminal(session.ID)
	if fake == nil {
		t.Fatal("expected terminal instance to be tracked by fake factory")
	}
	if !fake.IsClosed() {
		t.Fatal("expected removed terminal to be closed")
	}
	if calls := fake.closeCallsSnapshot(); calls != 1 {
		t.Fatalf("close calls = %d, want 1", calls)
	}
}

func TestManagerAttachDuplicateReuse(t *testing.T) {
	factory := newFakeFactory()
	m := NewManager(nil, slog.Default())
	m.newTerminal = factory.newTerminal

	host := model.Host{Name: "host-1"}
	session := model.Session{ID: "sess-1"}

	first, err := m.Attach(host, session, 80, 24)
	if err != nil {
		t.Fatalf("first attach: %v", err)
	}
	second, err := m.Attach(host, session, 120, 40)
	if err != nil {
		t.Fatalf("second attach: %v", err)
	}

	if first != second {
		t.Fatal("expected duplicate attach to reuse existing terminal")
	}
	if calls := factory.callCount(session.ID); calls != 1 {
		t.Fatalf("factory call count = %d, want 1", calls)
	}

	if ids := m.ActiveIDs(); !reflect.DeepEqual(ids, []string{session.ID}) {
		t.Fatalf("active ids = %v, want [%s]", ids, session.ID)
	}
}

func TestManagerShutdownClosesAllAndClearsMap(t *testing.T) {
	factory := newFakeFactory()
	m := NewManager(nil, slog.Default())
	m.newTerminal = factory.newTerminal

	host := model.Host{Name: "host-1"}
	sessions := []model.Session{{ID: "sess-2"}, {ID: "sess-1"}, {ID: "sess-3"}}

	for _, session := range sessions {
		if _, err := m.Attach(host, session, 80, 24); err != nil {
			t.Fatalf("attach %s: %v", session.ID, err)
		}
	}

	m.Shutdown()

	if ids := m.ActiveIDs(); len(ids) != 0 {
		t.Fatalf("expected no active ids after shutdown, got %v", ids)
	}

	for _, session := range sessions {
		if m.Has(session.ID) {
			t.Fatalf("expected Has(%q) to be false after shutdown", session.ID)
		}
		if m.Get(session.ID) != nil {
			t.Fatalf("expected Get(%q) to be nil after shutdown", session.ID)
		}

		fake := factory.firstTerminal(session.ID)
		if fake == nil {
			t.Fatalf("missing fake terminal for %s", session.ID)
		}
		if !fake.IsClosed() {
			t.Fatalf("expected terminal %s to be closed", session.ID)
		}
		if calls := fake.closeCallsSnapshot(); calls != 1 {
			t.Fatalf("close calls for %s = %d, want 1", session.ID, calls)
		}
	}
}

func TestManagerResizeAllPropagatesToActiveSessions(t *testing.T) {
	factory := newFakeFactory()
	m := NewManager(nil, slog.Default())
	m.newTerminal = factory.newTerminal

	host := model.Host{Name: "host-1"}
	ids := []string{"sess-1", "sess-2"}

	for _, id := range ids {
		if _, err := m.Attach(host, model.Session{ID: id}, 80, 24); err != nil {
			t.Fatalf("attach %s: %v", id, err)
		}
	}

	m.ResizeAll(120, 40)

	for _, id := range ids {
		fake := factory.firstTerminal(id)
		if fake == nil {
			t.Fatalf("missing fake terminal for %s", id)
		}
		if calls := fake.resizeCallsSnapshot(); !reflect.DeepEqual(calls, []resizeCall{{width: 120, height: 40}}) {
			t.Fatalf("resize calls for %s = %#v, want []resizeCall{{120,40}}", id, calls)
		}
	}
}

func TestManagerCleanupClosedRemovesClosedAndNilSessions(t *testing.T) {
	factory := newFakeFactory()
	m := NewManager(nil, slog.Default())
	m.newTerminal = factory.newTerminal

	host := model.Host{Name: "host-1"}
	ids := []string{"sess-1", "sess-2", "sess-3"}

	for _, id := range ids {
		if _, err := m.Attach(host, model.Session{ID: id}, 80, 24); err != nil {
			t.Fatalf("attach %s: %v", id, err)
		}
	}

	closedTerminal := factory.firstTerminal("sess-2")
	if closedTerminal == nil {
		t.Fatal("missing fake terminal for sess-2")
	}
	if err := closedTerminal.Close(); err != nil {
		t.Fatalf("close sess-2 fake terminal: %v", err)
	}

	m.mu.Lock()
	m.sessions["nil-entry"] = nil
	m.mu.Unlock()

	m.CleanupClosed()

	if m.Get("sess-1") == nil {
		t.Fatal("expected sess-1 to remain after cleanup")
	}
	if m.Get("sess-3") == nil {
		t.Fatal("expected sess-3 to remain after cleanup")
	}
	if m.Get("sess-2") != nil {
		t.Fatal("expected closed sess-2 to be removed by cleanup")
	}
	if m.Get("nil-entry") != nil {
		t.Fatal("expected nil-entry to be removed by cleanup")
	}

	if got := m.ActiveIDs(); !reflect.DeepEqual(got, []string{"sess-1", "sess-3"}) {
		t.Fatalf("active ids after cleanup = %v, want [sess-1 sess-3]", got)
	}
}

func TestManagerConcurrentAttachGetThreadSafety(t *testing.T) {
	factory := newFakeFactory()
	m := NewManager(nil, slog.Default())
	m.newTerminal = factory.newTerminal

	host := model.Host{Name: "host-1"}
	sessionData := model.Session{ID: "sess-concurrent"}

	const workers = 32
	start := make(chan struct{})
	var wg sync.WaitGroup

	results := make(chan Terminal, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			attached, err := m.Attach(host, sessionData, 80, 24)
			if err != nil {
				t.Errorf("attach failed: %v", err)
				return
			}

			if got := m.Get(sessionData.ID); got == nil {
				t.Error("Get returned nil during concurrent attach/get")
			}

			results <- attached
		}()
	}

	close(start)
	wg.Wait()
	close(results)

	stored := m.Get(sessionData.ID)
	if stored == nil {
		t.Fatal("expected stored terminal after concurrent attaches")
	}

	for attached := range results {
		if attached == nil {
			t.Fatal("concurrent Attach returned nil terminal")
		}
		if attached != stored {
			t.Fatal("expected all concurrent Attach calls to return stored terminal")
		}
	}

	if got := m.ActiveIDs(); !reflect.DeepEqual(got, []string{sessionData.ID}) {
		t.Fatalf("active ids = %v, want [%s]", got, sessionData.ID)
	}

	created := factory.allTerminals(sessionData.ID)
	if len(created) == 0 {
		t.Fatal("expected at least one terminal to be created")
	}

	openCount := 0
	for _, terminal := range created {
		if terminal == nil {
			continue
		}
		if terminal.closeCallsSnapshot() == 0 {
			openCount++
		}
	}

	if openCount != 1 {
		t.Fatalf("open terminal count = %d, want 1", openCount)
	}

	m.Shutdown()
}
