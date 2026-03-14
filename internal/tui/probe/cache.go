package probe

import (
	"time"

	"opencoderouter/internal/remote"
)

type CacheStore = remote.CacheStore

func NewCacheStore(ttl time.Duration) *CacheStore {
	return remote.NewCacheStore(ttl)
}
