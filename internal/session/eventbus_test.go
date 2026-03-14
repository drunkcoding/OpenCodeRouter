package session

import (
	"testing"
	"time"
)

func TestEventBusSubscribePublishFilterAndUnsubscribe(t *testing.T) {
	bus := NewEventBus(8)

	ch, unsubscribe, err := bus.Subscribe(EventFilter{
		SessionID: "s-1",
		Types:     []EventType{EventTypeSessionCreated},
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	createdMatch := SessionCreated{
		At: time.Now(),
		Session: SessionHandle{
			ID: "s-1",
		},
	}

	if err := bus.Publish(createdMatch); err != nil {
		t.Fatalf("publish created: %v", err)
	}

	select {
	case event := <-ch:
		if event.Type() != EventTypeSessionCreated {
			t.Fatalf("event type = %q, want %q", event.Type(), EventTypeSessionCreated)
		}
		if event.SessionID() != "s-1" {
			t.Fatalf("session id = %q, want s-1", event.SessionID())
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for matching event")
	}

	nonMatchingType := SessionStopped{
		At: time.Now(),
		Session: SessionHandle{
			ID: "s-1",
		},
		Reason: "test",
	}
	if err := bus.Publish(nonMatchingType); err != nil {
		t.Fatalf("publish non-matching type: %v", err)
	}

	select {
	case event := <-ch:
		t.Fatalf("unexpected event for non-matching type: %v", event)
	case <-time.After(100 * time.Millisecond):
	}

	nonMatchingSession := SessionCreated{
		At: time.Now(),
		Session: SessionHandle{
			ID: "s-2",
		},
	}
	if err := bus.Publish(nonMatchingSession); err != nil {
		t.Fatalf("publish non-matching session: %v", err)
	}

	select {
	case event := <-ch:
		t.Fatalf("unexpected event for non-matching session: %v", event)
	case <-time.After(100 * time.Millisecond):
	}

	unsubscribe()

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected channel to be closed after unsubscribe")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for closed subscriber channel")
	}
}

func TestEventBusCloseAndNilPublish(t *testing.T) {
	bus := NewEventBus(2)

	if err := bus.Publish(nil); err == nil {
		t.Fatal("expected publishing nil event to fail")
	}

	bus.Close()

	if _, _, err := bus.Subscribe(EventFilter{}); err == nil {
		t.Fatal("expected subscribe on closed bus to fail")
	}

	err := bus.Publish(SessionCreated{At: time.Now(), Session: SessionHandle{ID: "s-1"}})
	if err == nil {
		t.Fatal("expected publish on closed bus to fail")
	}
}
