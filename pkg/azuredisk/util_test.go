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

package azuredisk

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestLockMapLockUnlock verifies that LockEntry provides mutual exclusion for
// the same entry, so concurrent critical sections do not race.
func TestLockMapLockUnlock(t *testing.T) {
	lm := newLockMap()
	const goroutines = 20
	const incrementsPerGoroutine = 100

	var counter int
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < incrementsPerGoroutine; j++ {
				err := lm.LockEntry(context.Background(), "entry")
				assert.NoError(t, err, "LockEntry should succeed with a live context")
				// non-atomic increment is safe only because the lock serializes access
				counter++
				lm.UnlockEntry("entry")
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, goroutines*incrementsPerGoroutine, counter, "lock should serialize all increments")
}

// TestLockMapDifferentEntriesDoNotBlock verifies that locks on different
// entries are independent and do not block each other.
func TestLockMapDifferentEntriesDoNotBlock(t *testing.T) {
	lm := newLockMap()

	assert.NoError(t, lm.LockEntry(context.Background(), "entry-a"))
	defer lm.UnlockEntry("entry-a")

	// A different entry must be acquirable even though entry-a is held.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := lm.LockEntry(ctx, "entry-b")
	assert.NoError(t, err, "a different entry should not be blocked by entry-a")
	lm.UnlockEntry("entry-b")
}

// TestLockMapContextTimeout verifies that LockEntry propagates the context
// error when the entry is already held, and that a failed acquire leaves the
// entry free (our code must not mark it held on error).
func TestLockMapContextTimeout(t *testing.T) {
	lm := newLockMap()

	// Hold the entry so the next acquire must block and then fail.
	assert.NoError(t, lm.LockEntry(context.Background(), "entry"))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := lm.LockEntry(ctx, "entry")
	assert.ErrorIs(t, err, context.DeadlineExceeded, "blocked LockEntry should return the context error on timeout")

	// The failed acquire must not have taken the entry. After the holder
	// releases, the entry must be immediately acquirable again.
	lm.UnlockEntry("entry")
	assert.NoError(t, lm.LockEntry(context.Background(), "entry"), "entry should be free after the holder releases")
	lm.UnlockEntry("entry")
}

// TestLockMapCanceledContext verifies that LockEntry returns the context error
// when the context is already canceled, and leaves the entry free.
func TestLockMapCanceledContext(t *testing.T) {
	lm := newLockMap()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := lm.LockEntry(ctx, "entry")
	assert.ErrorIs(t, err, context.Canceled, "LockEntry should return the context error for an already-canceled context")

	// Even though acquisition failed, the entry must remain free.
	assert.NoError(t, lm.LockEntry(context.Background(), "entry"), "entry should still be acquirable after a canceled acquire")
	lm.UnlockEntry("entry")
}
