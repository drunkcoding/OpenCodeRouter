package cache

import (
	"container/list"
	"sync"
)

type sessionLRU struct {
	mu       sync.Mutex
	order    *list.List
	nodes    map[string]*list.Element
	fileSize map[string]int64
	total    int64
}

func newSessionLRU() *sessionLRU {
	return &sessionLRU{
		order:    list.New(),
		nodes:    make(map[string]*list.Element),
		fileSize: make(map[string]int64),
	}
}

func (l *sessionLRU) Ensure(sessionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ensureLocked(sessionID)
}

func (l *sessionLRU) Touch(sessionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	elem := l.ensureLocked(sessionID)
	l.order.MoveToBack(elem)
}

func (l *sessionLRU) SetSize(sessionID string, size int64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if size < 0 {
		size = 0
	}
	old := l.fileSize[sessionID]
	l.fileSize[sessionID] = size
	l.total += size - old
}

func (l *sessionLRU) AddSize(sessionID string, delta int64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	current := l.fileSize[sessionID]
	next := current + delta
	if next < 0 {
		next = 0
	}
	l.fileSize[sessionID] = next
	l.total += next - current
}

func (l *sessionLRU) Remove(sessionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if elem, ok := l.nodes[sessionID]; ok {
		l.order.Remove(elem)
		delete(l.nodes, sessionID)
	}
	if size, ok := l.fileSize[sessionID]; ok {
		l.total -= size
		delete(l.fileSize, sessionID)
	}
}

func (l *sessionLRU) Oldest() (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	front := l.order.Front()
	if front == nil {
		return "", false
	}
	key, ok := front.Value.(string)
	if !ok || key == "" {
		return "", false
	}
	return key, true
}

func (l *sessionLRU) TotalSize() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.total
}

func (l *sessionLRU) ensureLocked(sessionID string) *list.Element {
	if elem, ok := l.nodes[sessionID]; ok {
		return elem
	}
	elem := l.order.PushBack(sessionID)
	l.nodes[sessionID] = elem
	return elem
}
