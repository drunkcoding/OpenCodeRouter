package registry

import (
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// ---------------------------------------------------------------------------
// Slugify
// ---------------------------------------------------------------------------

func TestSlugify(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"simple", "/home/alice/myproject", "myproject"},
		{"uppercase", "/home/alice/MyProject", "myproject"},
		{"spaces", "/home/alice/My Awesome Project", "my-awesome-project"},
		{"dots", "/home/alice/project.v2", "project-v2"},
		{"underscores", "/home/alice/my_project_name", "my-project-name"},
		{"special chars", "/home/alice/proj@#$%ect!", "proj-ect"},
		{"leading trailing special", "/home/alice/---project---", "project"},
		{"multiple hyphens", "/home/alice/a---b---c", "a-b-c"},
		{"unicode", "/home/alice/проект", "default"}, // non-latin → stripped → empty → "default"
		{"empty basename", "/", "default"},
		{"just dot", ".", "default"},
		{"relative path", "relative/path/to/project", "project"},
		{"windows-ish", `C:\Users\alice\my-project`, "c-users-alice-my-project"}, // backslash not a separator on Linux
		{"mixed", "/opt/code/Hello World v2.1!", "hello-world-v2-1"},
		{"numbers only", "/home/alice/12345", "12345"},
		{"already clean", "/home/alice/clean-slug", "clean-slug"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Slugify(tt.path)
			if got != tt.want {
				t.Errorf("Slugify(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Upsert
// ---------------------------------------------------------------------------

func TestUpsert_NewEntry(t *testing.T) {
	r := New(30*time.Second, testLogger())

	isNew := r.Upsert(4096, "myproject", "/home/alice/myproject", "1.0.0")
	if !isNew {
		t.Error("expected Upsert to return true for new entry")
	}
	if r.Len() != 1 {
		t.Errorf("expected Len() == 1, got %d", r.Len())
	}

	b, ok := r.Lookup("myproject")
	if !ok {
		t.Fatal("expected Lookup to find 'myproject'")
	}
	if b.Port != 4096 {
		t.Errorf("expected port 4096, got %d", b.Port)
	}
	if b.ProjectName != "myproject" {
		t.Errorf("expected project name 'myproject', got %q", b.ProjectName)
	}
	if b.Version != "1.0.0" {
		t.Errorf("expected version '1.0.0', got %q", b.Version)
	}
}

func TestUpsert_UpdateExisting(t *testing.T) {
	r := New(30*time.Second, testLogger())

	r.Upsert(4096, "proj", "/home/alice/proj", "1.0.0")
	isNew := r.Upsert(4096, "proj-updated", "/home/alice/proj", "2.0.0")
	if isNew {
		t.Error("expected Upsert to return false for update")
	}
	if r.Len() != 1 {
		t.Errorf("expected Len() == 1 after update, got %d", r.Len())
	}

	b, _ := r.Lookup("proj")
	if b.Version != "2.0.0" {
		t.Errorf("expected version '2.0.0' after update, got %q", b.Version)
	}
	if b.ProjectName != "proj-updated" {
		t.Errorf("expected project name 'proj-updated', got %q", b.ProjectName)
	}
}

func TestUpsert_PortReassignment(t *testing.T) {
	r := New(30*time.Second, testLogger())

	// Same project, new port.
	r.Upsert(4096, "proj", "/home/alice/proj", "1.0")
	r.Upsert(4097, "proj", "/home/alice/proj", "1.0")

	if r.Len() != 1 {
		t.Errorf("expected 1 backend after port change, got %d", r.Len())
	}

	b, ok := r.Lookup("proj")
	if !ok {
		t.Fatal("expected to find 'proj'")
	}
	if b.Port != 4097 {
		t.Errorf("expected port 4097 after reassignment, got %d", b.Port)
	}

	// Old port should no longer resolve.
	_, found := r.LookupByPort(4096)
	if found {
		t.Error("old port 4096 should not resolve after reassignment")
	}

	// New port should resolve.
	b2, found := r.LookupByPort(4097)
	if !found {
		t.Fatal("expected LookupByPort(4097) to succeed")
	}
	if b2.Slug != "proj" {
		t.Errorf("expected slug 'proj', got %q", b2.Slug)
	}
}

func TestUpsert_ProjectChangedOnSamePort(t *testing.T) {
	r := New(30*time.Second, testLogger())

	r.Upsert(4096, "old-project", "/home/alice/old-project", "1.0")
	r.Upsert(4096, "new-project", "/home/alice/new-project", "1.0")

	// Old slug should be gone.
	_, found := r.Lookup("old-project")
	if found {
		t.Error("old-project should be removed after project change on same port")
	}

	// New slug should be present.
	b, found := r.Lookup("new-project")
	if !found {
		t.Fatal("expected new-project to be registered")
	}
	if b.Port != 4096 {
		t.Errorf("expected port 4096, got %d", b.Port)
	}
}

func TestUpsert_MultipleBackends(t *testing.T) {
	r := New(30*time.Second, testLogger())

	r.Upsert(4096, "proj-a", "/home/alice/proj-a", "1.0")
	r.Upsert(4097, "proj-b", "/home/alice/proj-b", "1.0")
	r.Upsert(4098, "proj-c", "/home/alice/proj-c", "1.0")

	if r.Len() != 3 {
		t.Errorf("expected 3 backends, got %d", r.Len())
	}

	all := r.All()
	if len(all) != 3 {
		t.Errorf("All() returned %d items, expected 3", len(all))
	}
}

// ---------------------------------------------------------------------------
// LookupByPort
// ---------------------------------------------------------------------------

func TestLookupByPort_NotFound(t *testing.T) {
	r := New(30*time.Second, testLogger())
	_, ok := r.LookupByPort(9999)
	if ok {
		t.Error("expected LookupByPort to return false for unknown port")
	}
}

func TestLookup_NotFound(t *testing.T) {
	r := New(30*time.Second, testLogger())
	_, ok := r.Lookup("nonexistent")
	if ok {
		t.Error("expected Lookup to return false for unknown slug")
	}
}

// ---------------------------------------------------------------------------
// Prune
// ---------------------------------------------------------------------------

func TestPrune_RemovesStale(t *testing.T) {
	r := New(50*time.Millisecond, testLogger())

	r.Upsert(4096, "stale-proj", "/home/alice/stale-proj", "1.0")
	time.Sleep(100 * time.Millisecond)

	removed := r.Prune()
	if len(removed) != 1 {
		t.Errorf("expected 1 removal, got %d", len(removed))
	}
	if removed[0] != "stale-proj" {
		t.Errorf("expected 'stale-proj' to be removed, got %q", removed[0])
	}
	if r.Len() != 0 {
		t.Errorf("expected empty registry after prune, got %d", r.Len())
	}
}

func TestPrune_KeepsFresh(t *testing.T) {
	r := New(5*time.Second, testLogger())

	r.Upsert(4096, "fresh-proj", "/home/alice/fresh-proj", "1.0")

	removed := r.Prune()
	if len(removed) != 0 {
		t.Errorf("expected 0 removals, got %d", len(removed))
	}
	if r.Len() != 1 {
		t.Errorf("expected 1 backend to survive prune, got %d", r.Len())
	}
}

func TestPrune_MixedStaleAndFresh(t *testing.T) {
	r := New(50*time.Millisecond, testLogger())

	r.Upsert(4096, "stale", "/home/alice/stale", "1.0")
	time.Sleep(100 * time.Millisecond)
	r.Upsert(4097, "fresh", "/home/alice/fresh", "1.0")

	removed := r.Prune()
	if len(removed) != 1 {
		t.Errorf("expected 1 removal, got %d", len(removed))
	}
	if r.Len() != 1 {
		t.Errorf("expected 1 surviving backend, got %d", r.Len())
	}
	_, ok := r.Lookup("fresh")
	if !ok {
		t.Error("expected 'fresh' to survive prune")
	}
}

// ---------------------------------------------------------------------------
// All / Slugs
// ---------------------------------------------------------------------------

func TestAll_ReturnsCopies(t *testing.T) {
	r := New(30*time.Second, testLogger())
	r.Upsert(4096, "proj", "/home/alice/proj", "1.0")

	all := r.All()
	if len(all) != 1 {
		t.Fatalf("expected 1, got %d", len(all))
	}

	// Mutate the returned copy.
	all[0].Port = 9999

	// Original should be unchanged.
	b, _ := r.Lookup("proj")
	if b.Port == 9999 {
		t.Error("All() should return copies, not references")
	}
}

func TestSlugs(t *testing.T) {
	r := New(30*time.Second, testLogger())
	r.Upsert(4096, "a", "/home/alice/a", "1.0")
	r.Upsert(4097, "b", "/home/alice/b", "1.0")

	slugs := r.Slugs()
	if len(slugs) != 2 {
		t.Errorf("expected 2 slugs, got %d", len(slugs))
	}

	found := make(map[string]bool)
	for _, s := range slugs {
		found[s] = true
	}
	if !found["a"] || !found["b"] {
		t.Errorf("expected slugs 'a' and 'b', got %v", slugs)
	}
}

// ---------------------------------------------------------------------------
// Backend.Healthy
// ---------------------------------------------------------------------------

func TestBackend_Healthy(t *testing.T) {
	b := Backend{LastSeen: time.Now()}
	if !b.Healthy(5 * time.Second) {
		t.Error("recently seen backend should be healthy")
	}

	b.LastSeen = time.Now().Add(-10 * time.Second)
	if b.Healthy(5 * time.Second) {
		t.Error("old backend should not be healthy")
	}
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

func TestConcurrency(t *testing.T) {
	r := New(30*time.Second, testLogger())

	var wg sync.WaitGroup
	// Writers.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			port := 4000 + i
			r.Upsert(port, "proj", "/home/alice/proj"+string(rune('a'+i%26)), "1.0")
		}(i)
	}
	// Readers.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.All()
			r.Slugs()
			r.Len()
			r.Lookup("proj")
			r.LookupByPort(4000)
		}()
	}
	// Pruner.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Prune()
		}()
	}

	wg.Wait()
	// No race detector panic = success.
}
