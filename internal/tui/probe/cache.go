package probe

import (
	"sync"
	"time"

	"opencoderouter/internal/tui/model"
)

type cacheEntry struct {
	host      model.Host
	expiresAt time.Time
}

// CacheStore is a simple in-memory TTL cache for probe responses.
type CacheStore struct {
	mu      sync.RWMutex
	ttl     time.Duration
	nowFunc func() time.Time
	entries map[string]cacheEntry
}

// NewCacheStore creates a new in-memory cache with the provided TTL.
func NewCacheStore(ttl time.Duration) *CacheStore {
	return &CacheStore{
		ttl:     ttl,
		nowFunc: time.Now,
		entries: make(map[string]cacheEntry),
	}
}

// Get retrieves a host from cache if the entry is still valid.
func (c *CacheStore) Get(key string) (model.Host, bool) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok {
		return model.Host{}, false
	}
	if c.nowFunc().After(entry.expiresAt) {
		c.mu.Lock()
		delete(c.entries, key)
		c.mu.Unlock()
		return model.Host{}, false
	}
	return entry.host, true
}

// Set stores a host in cache and refreshes expiry.
func (c *CacheStore) Set(key string, host model.Host) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheEntry{host: host, expiresAt: c.nowFunc().Add(c.ttl)}
}

// PurgeExpired removes expired entries and returns deleted count.
func (c *CacheStore) PurgeExpired() int {
	now := c.nowFunc()
	removed := 0
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, entry := range c.entries {
		if now.After(entry.expiresAt) {
			delete(c.entries, key)
			removed++
		}
	}
	return removed
}
