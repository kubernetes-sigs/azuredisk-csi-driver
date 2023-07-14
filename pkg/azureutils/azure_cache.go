package azureutils

import (
	"sync"
	"time"

	"k8s.io/client-go/tools/cache"
)

// AzureCacheReadType defines the read type for cache data
type AzureCacheReadType int

const (
	// CacheReadTypeDefault returns data from cache if cache entry not expired
	// if cache entry expired, then it will refetch the data using getter
	// save the entry in cache and then return
	CacheReadTypeDefault AzureCacheReadType = iota
	// CacheReadTypeUnsafe returns data from cache even if the cache entry is
	// active/expired. If entry doesn't exist in cache, then data is fetched
	// using getter, saved in cache and returned
	CacheReadTypeUnsafe
	// CacheReadTypeForceRefresh force refreshes the cache even if the cache entry
	// is not expired
	CacheReadTypeForceRefresh
)

// GetFunc defines a getter function for timedCache.
type GetFunc func(key string) (interface{}, error)

// AzureCacheEntry is the internal structure stores inside TTLStore.
type AzureCacheEntry struct {
	Key  string
	Data interface{}

	// The lock to ensure not updating same entry simultaneously.
	Lock sync.Mutex
	// time when entry was fetched and created
	CreatedOn time.Time
}

// cacheKeyFunc defines the key function required in TTLStore.
func cacheKeyFunc(obj interface{}) (string, error) {
	return obj.(*AzureCacheEntry).Key, nil
}

// TimedCache is a cache with TTL.
type TimedCache struct {
	Store  cache.Store
	Lock   sync.Mutex
	Getter GetFunc
	TTL    time.Duration
}

// getInternal returns AzureCacheEntry by key. If the key is not cached yet,
// it returns a AzureCacheEntry with nil data.
func (t *TimedCache) getInternal(key string) (*AzureCacheEntry, error) {
	entry, exists, err := t.Store.GetByKey(key)
	if err != nil {
		return nil, err
	}
	// if entry exists, return the entry
	if exists {
		return entry.(*AzureCacheEntry), nil
	}

	// lock here to ensure if entry doesn't exist, we add a new entry
	// avoiding overwrites
	t.Lock.Lock()
	defer t.Lock.Unlock()

	// Another goroutine might have written the same key.
	entry, exists, err = t.Store.GetByKey(key)
	if err != nil {
		return nil, err
	}
	if exists {
		return entry.(*AzureCacheEntry), nil
	}

	// Still not found, add new entry with nil data.
	// Note the data will be filled later by getter.
	newEntry := &AzureCacheEntry{
		Key:  key,
		Data: nil,
	}
	_ = t.Store.Add(newEntry)
	return newEntry, nil
}

// Get returns the requested item by key.
func (t *TimedCache) Get(key string, crt AzureCacheReadType) (interface{}, error) {
	return t.get(key, crt)
}

func (t *TimedCache) get(key string, crt AzureCacheReadType) (interface{}, error) {
	entry, err := t.getInternal(key)
	if err != nil {
		return nil, err
	}

	entry.Lock.Lock()
	defer entry.Lock.Unlock()

	// entry exists and if cache is not force refreshed
	if entry.Data != nil && crt != CacheReadTypeForceRefresh {
		// allow unsafe read, so return data even if expired
		if crt == CacheReadTypeUnsafe {
			return entry.Data, nil
		}
		// if cached data is not expired, return cached data
		if crt == CacheReadTypeDefault && time.Since(entry.CreatedOn) < t.TTL {
			return entry.Data, nil
		}
	}
	// Data is not cached yet, cache data is expired or requested force refresh
	// cache it by getter. entry is locked before getting to ensure concurrent
	// gets don't result in multiple ARM calls.
	data, err := t.Getter(key)
	if err != nil {
		return nil, err
	}

	// set the data in cache and also set the last update time
	// to now as the data was recently fetched
	entry.Data = data
	entry.CreatedOn = time.Now().UTC()

	return entry.Data, nil
}
