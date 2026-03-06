package cache

import "testing"

func TestSessionLRUOrderAndSize(t *testing.T) {
	lru := newSessionLRU()
	lru.Ensure("a")
	lru.Ensure("b")
	lru.Ensure("c")

	if oldest, ok := lru.Oldest(); !ok || oldest != "a" {
		t.Fatalf("unexpected oldest before touch: %q, ok=%v", oldest, ok)
	}

	lru.Touch("a")
	if oldest, ok := lru.Oldest(); !ok || oldest != "b" {
		t.Fatalf("unexpected oldest after touch: %q, ok=%v", oldest, ok)
	}

	lru.SetSize("a", 10)
	lru.AddSize("b", 20)
	lru.SetSize("c", 30)
	if total := lru.TotalSize(); total != 60 {
		t.Fatalf("unexpected total size: got %d want 60", total)
	}

	lru.Remove("b")
	if oldest, ok := lru.Oldest(); !ok || oldest != "c" {
		t.Fatalf("unexpected oldest after remove: %q, ok=%v", oldest, ok)
	}
	if total := lru.TotalSize(); total != 40 {
		t.Fatalf("unexpected total size after remove: got %d want 40", total)
	}
}
