package azureutils

import (
	"fmt"
	"reflect"
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

// NewTimedcache creates a new TimedCache.
func NewTimedcache(ttl time.Duration, getter GetFunc) (*TimedCache, error) {
	if getter == nil {
		return nil, fmt.Errorf("getter is not provided")
	}

	return &TimedCache{
		Getter: getter,
		// switch to using NewStore instead of NewTTLStore so that we can
		// reuse entries for calls that are fine with reading expired/stalled data.
		// with NewTTLStore, entries are not returned if they have already expired.
		Store: cache.NewStore(cacheKeyFunc),
		TTL:   ttl,
	}, nil
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

// Get returns the requested item by key with deep copy.
func (t *TimedCache) GetWithDeepCopy(key string, crt AzureCacheReadType) (interface{}, error) {
	data, err := t.get(key, crt)
	copied := Copy(data)
	return copied, err
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

type deepCopyInterface interface {
	DeepCopy() interface{}
}

// Copy deepcopies from v.
func Copy(src interface{}) interface{} {
	if src == nil {
		return nil
	}

	if fromSyncMap, ok := src.(*sync.Map); ok {
		to := copySyncMap(fromSyncMap)
		return to
	}

	return copyNormal(src)
}

// copySyncMap copies with sync.Map but not nested
// Targets are vmssVMCache, vmssFlexVMCache, etc.
func copySyncMap(from *sync.Map) *sync.Map {
	to := &sync.Map{}

	from.Range(func(k, v interface{}) bool {
		vm, ok := v.(*sync.Map)
		if ok {
			to.Store(k, copySyncMap(vm))
		} else {
			to.Store(k, copyNormal(v))
		}
		return true
	})

	return to
}

func copyNormal(src interface{}) interface{} {
	if src == nil {
		return nil
	}

	from := reflect.ValueOf(src)

	to := reflect.New(from.Type()).Elem()

	copy(from, to)

	return to.Interface()
}

func copy(from, to reflect.Value) {
	// Check if DeepCopy() is already implemented for the interface
	if from.CanInterface() {
		if deepcopy, ok := from.Interface().(deepCopyInterface); ok {
			to.Set(reflect.ValueOf(deepcopy.DeepCopy()))
			return
		}
	}

	switch from.Kind() {
	case reflect.Pointer:
		fromValue := from.Elem()
		if !fromValue.IsValid() {
			return
		}

		to.Set(reflect.New(fromValue.Type()))
		copy(fromValue, to.Elem())

	case reflect.Interface:
		if from.IsNil() {
			return
		}

		fromValue := from.Elem()
		toValue := reflect.New(fromValue.Type()).Elem()
		copy(fromValue, toValue)
		to.Set(toValue)

	case reflect.Struct:
		for i := 0; i < from.NumField(); i++ {
			if from.Type().Field(i).PkgPath != "" {
				// It is an unexported field.
				continue
			}
			copy(from.Field(i), to.Field(i))
		}

	case reflect.Slice:
		if from.IsNil() {
			return
		}

		to.Set(reflect.MakeSlice(from.Type(), from.Len(), from.Cap()))
		for i := 0; i < from.Len(); i++ {
			copy(from.Index(i), to.Index(i))
		}

	case reflect.Map:
		if from.IsNil() {
			return
		}

		to.Set(reflect.MakeMap(from.Type()))
		for _, key := range from.MapKeys() {
			fromValue := from.MapIndex(key)
			toValue := reflect.New(fromValue.Type()).Elem()
			copy(fromValue, toValue)
			copiedKey := Copy(key.Interface())
			to.SetMapIndex(reflect.ValueOf(copiedKey), toValue)
		}

	default:
		to.Set(from)
	}
}
