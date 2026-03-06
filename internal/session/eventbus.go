package session

import (
	"errors"
	"sync"
)

const defaultEventBusBuffer = 64

var (
	ErrEventBusClosed = errors.New("session event bus closed")
	ErrNilEvent       = errors.New("session event is nil")
)

type eventSubscriber struct {
	ch     chan Event
	filter EventFilter
}

type ChannelEventBus struct {
	mu          sync.RWMutex
	subscribers map[uint64]*eventSubscriber
	nextID      uint64
	buffer      int
	closed      bool
}

func NewEventBus(buffer int) *ChannelEventBus {
	if buffer <= 0 {
		buffer = defaultEventBusBuffer
	}

	return &ChannelEventBus{
		subscribers: make(map[uint64]*eventSubscriber),
		buffer:      buffer,
	}
}

func (b *ChannelEventBus) Subscribe(filter EventFilter) (<-chan Event, func(), error) {
	if b == nil {
		return nil, nil, ErrEventBusClosed
	}

	sub := &eventSubscriber{
		ch:     make(chan Event, b.buffer),
		filter: filter,
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, nil, ErrEventBusClosed
	}
	id := b.nextID
	b.nextID++
	b.subscribers[id] = sub
	b.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			b.unsubscribe(id)
		})
	}

	return sub.ch, unsubscribe, nil
}

func (b *ChannelEventBus) Publish(event Event) error {
	if b == nil {
		return ErrEventBusClosed
	}
	if event == nil {
		return ErrNilEvent
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		return ErrEventBusClosed
	}

	for _, sub := range b.subscribers {
		if !eventMatchesFilter(event, sub.filter) {
			continue
		}
		select {
		case sub.ch <- event:
		default:
		}
	}

	return nil
}

func (b *ChannelEventBus) Close() {
	if b == nil {
		return
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true

	for id, sub := range b.subscribers {
		delete(b.subscribers, id)
		close(sub.ch)
	}
	b.mu.Unlock()
}

func (b *ChannelEventBus) unsubscribe(id uint64) {
	if b == nil {
		return
	}

	b.mu.Lock()
	sub, ok := b.subscribers[id]
	if ok {
		delete(b.subscribers, id)
	}
	b.mu.Unlock()

	if ok {
		close(sub.ch)
	}
}

func eventMatchesFilter(event Event, filter EventFilter) bool {
	if filter.SessionID != "" && event.SessionID() != filter.SessionID {
		return false
	}
	if len(filter.Types) == 0 {
		return true
	}

	et := event.Type()
	for _, allowed := range filter.Types {
		if et == allowed {
			return true
		}
	}

	return false
}
