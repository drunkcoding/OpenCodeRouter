package registry

import (
	"testing"
	"time"
)

func TestSessionIndex_UpsertListRemove(t *testing.T) {
	r := New(30*time.Second, testLogger())
	r.Upsert(4096, "proj", "/home/alice/proj", "1.0")

	created := r.UpsertSession("proj", SessionMetadata{ID: "s-1", Title: "first"})
	if !created {
		t.Fatal("expected first upsert to create session")
	}

	created = r.UpsertSession("proj", SessionMetadata{ID: "s-1", Title: "updated"})
	if created {
		t.Fatal("expected second upsert to update existing session")
	}

	list := r.ListSessions("proj")
	if len(list) != 1 {
		t.Fatalf("expected 1 session, got %d", len(list))
	}
	if list[0].Title != "updated" {
		t.Fatalf("expected updated title, got %q", list[0].Title)
	}

	if !r.RemoveSession("proj", "s-1") {
		t.Fatal("expected RemoveSession to return true")
	}
	if r.RemoveSession("proj", "s-1") {
		t.Fatal("expected second RemoveSession to return false")
	}
	if len(r.ListSessions("proj")) != 0 {
		t.Fatal("expected no sessions after remove")
	}
}

func TestSessionIndex_ReplaceSessionsRemovesMissing(t *testing.T) {
	r := New(30*time.Second, testLogger())
	r.Upsert(4096, "proj", "/home/alice/proj", "1.0")

	r.ReplaceSessions("proj", []SessionMetadata{{ID: "a"}, {ID: "b"}})
	if got := len(r.ListSessions("proj")); got != 2 {
		t.Fatalf("expected 2 sessions, got %d", got)
	}

	r.ReplaceSessions("proj", []SessionMetadata{{ID: "b", Title: "keep"}})
	list := r.ListSessions("proj")
	if len(list) != 1 {
		t.Fatalf("expected 1 session after replacement, got %d", len(list))
	}
	if list[0].ID != "b" {
		t.Fatalf("expected only session b to remain, got %q", list[0].ID)
	}
}

func TestSessionIndex_RemovedWhenBackendPruned(t *testing.T) {
	r := New(20*time.Millisecond, testLogger())
	r.Upsert(4096, "proj", "/home/alice/proj", "1.0")
	r.UpsertSession("proj", SessionMetadata{ID: "s-1"})

	time.Sleep(40 * time.Millisecond)
	r.Prune()

	if len(r.ListSessions("proj")) != 0 {
		t.Fatal("expected sessions to be cleared when backend is pruned")
	}
}

func TestSessionIndex_RemovedWhenProjectChangesOnPort(t *testing.T) {
	r := New(30*time.Second, testLogger())
	r.Upsert(4096, "old", "/home/alice/old", "1.0")
	r.UpsertSession("old", SessionMetadata{ID: "s-1"})

	r.Upsert(4096, "new", "/home/alice/new", "1.0")

	if len(r.ListSessions("old")) != 0 {
		t.Fatal("expected old project sessions removed after port project change")
	}
}
