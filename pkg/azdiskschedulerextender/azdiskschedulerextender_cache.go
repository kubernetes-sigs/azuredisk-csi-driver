// +build azurediskv2

/*
Copyright 2017 The Kubernetes Authors.

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

package main

import (
	"reflect"
	"sync"
)

type cacheEntry struct {
	VolumeName string
	AccessMode string
}

type cachedMapping struct {
	mu   sync.RWMutex
	memo map[string]*cacheEntry
}

func (mapping *cachedMapping) Get(key string) (*cacheEntry, bool) {
	mapping.mu.RLock()
	defer mapping.mu.RUnlock()

	cacheEntry, ok := mapping.memo[key]
	if !ok {
		return nil, false
	}
	return cacheEntry, true

}

func (mapping *cachedMapping) AddOrUpdate(key string, value *cacheEntry) {
	mapping.mu.Lock()
	defer mapping.mu.Unlock()
	// Updated value in the cluster is the source of truth
	if cachedValue, exist := mapping.memo[key]; !exist || !reflect.DeepEqual(cachedValue, value) {
		mapping.memo[key] = value
	}
}

func (mapping *cachedMapping) Delete(key string) {
	mapping.mu.Lock()
	defer mapping.mu.Unlock()
	delete(mapping.memo, key)
}
