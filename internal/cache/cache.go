package cache

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultMaxEntriesPerSession = 10000
	defaultMaxTotalSize         = 100 * 1024 * 1024
)

func NewJSONLCache(cfg CacheConfig) (ScrollbackCache, error) {
	normalized := normalizeConfig(cfg)
	if err := os.MkdirAll(normalized.StoragePath, storageDirPerm); err != nil {
		return nil, err
	}

	cache := &JSONLCache{
		config:      normalized,
		writers:     make(map[string]*sessionWriter),
		entryCounts: make(map[string]int),
		lru:         newSessionLRU(),
		logger:      slog.Default(),
	}
	if err := cache.bootstrapFromDisk(); err != nil {
		return nil, err
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()
	if err := cache.evictLocked(); err != nil {
		return nil, err
	}

	return cache, nil
}

func normalizeConfig(cfg CacheConfig) CacheConfig {
	normalized := cfg
	if normalized.MaxEntriesPerSession <= 0 {
		normalized.MaxEntriesPerSession = defaultMaxEntriesPerSession
	}
	if normalized.MaxTotalSize <= 0 {
		normalized.MaxTotalSize = defaultMaxTotalSize
	}
	if normalized.EvictionPolicy != EvictionPolicyLRU && normalized.EvictionPolicy != EvictionPolicyFIFO {
		normalized.EvictionPolicy = EvictionPolicyLRU
	}
	normalized.StoragePath = strings.TrimSpace(normalized.StoragePath)
	if normalized.StoragePath == "" {
		normalized.StoragePath = filepath.Join(".opencode", "scrollback")
	}
	return normalized
}
