/*
Copyright 2020 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cache

import (
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
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

	// time when entry was fetched and created
	CreatedOn time.Time
}

// TimedCache is a cache with TTL.
type TimedCache struct {
	store  sync.Map
	group  singleflight.Group
	getter GetFunc
	ttl    time.Duration
}

// NewTimedcache creates a new TimedCache.
func NewTimedcache(ttl time.Duration, getter GetFunc) (*TimedCache, error) {
	if getter == nil {
		return nil, fmt.Errorf("getter is not provided")
	}

	return &TimedCache{
		getter: getter,
		ttl:    ttl,
	}, nil
}

// TryGet returns the value stored in the cache for a key, or nil if no
// value is present.
// The ok result indicates whether value was found in the cache.
func (t *TimedCache) TryGet(key string) (interface{}, bool) {
	if entry, ok := t.store.Load(key); ok {
		if data := entry.(*AzureCacheEntry).Data; data != nil {
			return data, true
		}
	}

	return nil, false
}

// Get returns the requested item by key.
func (t *TimedCache) Get(key string, crt AzureCacheReadType) (interface{}, error) {
	rawEntry, _ := t.store.LoadOrStore(key, &AzureCacheEntry{
		Key:  key,
		Data: nil,
	})

	entry := rawEntry.(*AzureCacheEntry)
	// entry exists and if cache is not force refreshed
	if entry.Data != nil && crt != CacheReadTypeForceRefresh {
		// allow unsafe read, so return data even if expired
		if crt == CacheReadTypeUnsafe {
			return entry.Data, nil
		}
		// if cached data is not expired, return cached data
		if crt == CacheReadTypeDefault && time.Since(entry.CreatedOn) < t.ttl {
			return entry.Data, nil
		}
	}

	// Data is not cached yet, cache data is expired or requested force refresh
	// cache it by getter. A singleflight.Group is used to coalesce multiple calls
	// into one to prevent both concurrent and storms of sequential ARM calls.
	data, err, _ := t.group.Do(key, func() (interface{}, error) {
		data, err := t.getter(key)
		if err != nil {
			return nil, err
		}

		// set the data in cache and also set the last update time
		// to now as the data was recently fetched
		t.Set(key, data)

		return data, nil
	})

	return data, err
}

// Delete removes an item from the cache.
func (t *TimedCache) Delete(key string) error {
	t.store.Delete(key)
	return nil
}

// Set sets the data cache for the key.
// It is only used for testing.
func (t *TimedCache) Set(key string, data interface{}) {
	t.store.Store(key, &AzureCacheEntry{
		Key:       key,
		Data:      data,
		CreatedOn: time.Now().UTC(),
	})
}
