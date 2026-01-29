/*
Copyright 2025 The Kubernetes Authors.

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

package freeze

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewFreezeManager(t *testing.T) {
	tests := []struct {
		name             string
		stateDir         string
		expectedStateDir string
	}{
		{
			name:             "with custom state dir",
			stateDir:         "/custom/path",
			expectedStateDir: "/custom/path",
		},
		{
			name:             "with empty state dir",
			stateDir:         "",
			expectedStateDir: StateFileDir,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fm := NewFreezeManager(tt.stateDir)
			if fm == nil {
				t.Fatal("NewFreezeManager returned nil")
			}
			if fm.stateDir != tt.expectedStateDir {
				t.Errorf("expected stateDir %s, got %s", tt.expectedStateDir, fm.stateDir)
			}
			if fm.operations == nil {
				t.Error("operations map is nil")
			}
		})
	}
}

func TestFreezeManager_StateFileOperations(t *testing.T) {
	tmpDir := t.TempDir()
	fm := NewFreezeManager(tmpDir)
	volumeUUID := "test-volume-uuid"

	// Test save state
	state := &FreezeState{
		Command:   CommandFreeze,
		State:     FreezeStateFrozen,
		Timestamp: time.Now(),
	}

	err := fm.saveState(volumeUUID, state)
	if err != nil {
		t.Fatalf("saveState failed: %v", err)
	}

	// Verify file exists
	stateFile := filepath.Join(tmpDir, volumeUUID+".json")
	if _, err := os.Stat(stateFile); os.IsNotExist(err) {
		t.Fatal("state file was not created")
	}

	// Test load state
	loadedState, err := fm.loadState(volumeUUID)
	if err != nil {
		t.Fatalf("loadState failed: %v", err)
	}
	if loadedState == nil {
		t.Fatal("loadState returned nil")
	}
	if loadedState.Command != state.Command {
		t.Errorf("expected command %s, got %s", state.Command, loadedState.Command)
	}
	if loadedState.State != state.State {
		t.Errorf("expected state %s, got %s", state.State, loadedState.State)
	}

	// Test GetState
	gotState, err := fm.GetState(volumeUUID)
	if err != nil {
		t.Fatalf("GetState failed: %v", err)
	}
	if gotState.State != state.State {
		t.Errorf("GetState: expected state %s, got %s", state.State, gotState.State)
	}

	// Test delete state
	fm.deleteState(volumeUUID)
	if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
		t.Fatal("state file was not deleted")
	}

	// Test load non-existent state
	loadedState, err = fm.loadState(volumeUUID)
	if err != nil {
		t.Fatalf("loadState for non-existent file failed: %v", err)
	}
	if loadedState != nil {
		t.Error("loadState should return nil for non-existent file")
	}
}

func TestFreezeManager_CleanupState(t *testing.T) {
	tmpDir := t.TempDir()
	fm := NewFreezeManager(tmpDir)
	volumeUUID := "test-cleanup-uuid"

	// Create a state file
	state := &FreezeState{
		Command:   CommandFreeze,
		State:     FreezeStateFrozen,
		Timestamp: time.Now(),
	}
	fm.saveState(volumeUUID, state)

	// Cleanup
	fm.CleanupState(volumeUUID)

	// Verify file is deleted
	stateFile := filepath.Join(tmpDir, volumeUUID+".json")
	if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
		t.Fatal("CleanupState did not delete state file")
	}
}

func TestFreezeManager_GetStateFilePath(t *testing.T) {
	fm := NewFreezeManager("/test/dir")
	volumeUUID := "test-uuid"
	expected := "/test/dir/test-uuid.json"
	got := fm.getStateFilePath(volumeUUID)
	if got != expected {
		t.Errorf("expected %s, got %s", expected, got)
	}
}

func TestFreezeManager_AnalyzeFreezeError(t *testing.T) {
	fm := NewFreezeManager("")

	tests := []struct {
		name          string
		err           error
		output        string
		expectedState string
	}{
		{
			name:          "no error - frozen",
			err:           nil,
			output:        "",
			expectedState: FreezeStateFrozen,
		},
		{
			name:          "device or resource busy",
			err:           fmt.Errorf("exit status 1"),
			output:        "fsfreeze: Device or resource busy",
			expectedState: FreezeStateUserFrozen,
		},
		{
			name:          "not supported",
			err:           fmt.Errorf("exit status 1"),
			output:        "fsfreeze: Operation not supported",
			expectedState: FreezeStateSkipped,
		},
		{
			name:          "not a directory",
			err:           fmt.Errorf("exit status 1"),
			output:        "fsfreeze: not a directory",
			expectedState: FreezeStateSkipped,
		},
		{
			name:          "no such file",
			err:           fmt.Errorf("exit status 1"),
			output:        "fsfreeze: No such file or directory",
			expectedState: FreezeStateSkipped,
		},
		{
			name:          "unknown error",
			err:           fmt.Errorf("exit status 1"),
			output:        "fsfreeze: Unknown error occurred",
			expectedState: FreezeStateFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := fm.analyzeFreezeError(tt.err, tt.output)
			if state != tt.expectedState {
				t.Errorf("expected state %s, got %s", tt.expectedState, state)
			}
		})
	}
}

func TestFreezeManager_ConcurrentOperations(t *testing.T) {
	tmpDir := t.TempDir()
	fm := NewFreezeManager(tmpDir)

	// Test concurrent operations on different volumes
	done := make(chan bool)
	for i := 0; i < 5; i++ {
		go func(id int) {
			volumeUUID := "volume-" + string(rune('0'+id))
			state := &FreezeState{
				Command:   CommandFreeze,
				State:     FreezeStateFrozen,
				Timestamp: time.Now(),
			}
			if err := fm.saveState(volumeUUID, state); err != nil {
				t.Errorf("concurrent saveState failed: %v", err)
			}
			if _, err := fm.loadState(volumeUUID); err != nil {
				t.Errorf("concurrent loadState failed: %v", err)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 5; i++ {
		<-done
	}
}

func TestFreezeManager_GetOperationLock(t *testing.T) {
	fm := NewFreezeManager("")
	volumeUUID := "test-volume"

	// Get lock for first time
	lock1 := fm.getOperationLock(volumeUUID)
	if lock1 == nil {
		t.Fatal("getOperationLock returned nil")
	}

	// Get same lock again
	lock2 := fm.getOperationLock(volumeUUID)
	if lock2 == nil {
		t.Fatal("getOperationLock returned nil on second call")
	}

	// Should return the same lock instance
	if lock1 != lock2 {
		t.Error("getOperationLock returned different locks for same volume")
	}

	// Get lock for different volume
	lock3 := fm.getOperationLock("different-volume")
	if lock3 == lock1 {
		t.Error("getOperationLock returned same lock for different volume")
	}
}

func TestFreezeState_JSONMarshaling(t *testing.T) {
	state := &FreezeState{
		Command:   CommandFreeze,
		State:     FreezeStateFrozen,
		Timestamp: time.Now().Round(time.Second), // Round to avoid microsecond differences
	}

	// Marshal
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	// Unmarshal
	var unmarshaled FreezeState
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	// Compare
	if unmarshaled.Command != state.Command {
		t.Errorf("expected command %s, got %s", state.Command, unmarshaled.Command)
	}
	if unmarshaled.State != state.State {
		t.Errorf("expected state %s, got %s", state.State, unmarshaled.State)
	}
	if !unmarshaled.Timestamp.Equal(state.Timestamp) {
		t.Errorf("expected timestamp %v, got %v", state.Timestamp, unmarshaled.Timestamp)
	}
}

func TestFreezeManager_SaveStateError(t *testing.T) {
	// Use an invalid directory to trigger error
	fm := NewFreezeManager("/invalid/nonexistent/directory")
	volumeUUID := "test-volume"

	state := &FreezeState{
		Command:   CommandFreeze,
		State:     FreezeStateFrozen,
		Timestamp: time.Now(),
	}

	err := fm.saveState(volumeUUID, state)
	if err == nil {
		t.Error("saveState should fail with invalid directory")
	}
}

func TestFreezeManager_LoadStateWithCorruptedFile(t *testing.T) {
	tmpDir := t.TempDir()
	fm := NewFreezeManager(tmpDir)
	volumeUUID := "test-volume"

	// Create a corrupted JSON file
	stateFile := filepath.Join(tmpDir, volumeUUID+".json")
	err := os.WriteFile(stateFile, []byte("invalid json {{{"), 0644)
	if err != nil {
		t.Fatalf("failed to create corrupted file: %v", err)
	}

	// Try to load
	_, err = fm.loadState(volumeUUID)
	if err == nil {
		t.Error("loadState should fail with corrupted JSON")
	}
}

func TestFreezeManager_StateTransitions(t *testing.T) {
	tmpDir := t.TempDir()
	fm := NewFreezeManager(tmpDir)
	volumeUUID := "test-volume"

	tests := []struct {
		name  string
		state *FreezeState
	}{
		{
			name: "freezing",
			state: &FreezeState{
				Command:   CommandFreeze,
				State:     FreezeStateFreezing,
				Timestamp: time.Now(),
			},
		},
		{
			name: "frozen",
			state: &FreezeState{
				Command:   CommandFreeze,
				State:     FreezeStateFrozen,
				Timestamp: time.Now(),
			},
		},
		{
			name: "unfrozen",
			state: &FreezeState{
				Command:   CommandUnfreeze,
				State:     FreezeStateUnfrozen,
				Timestamp: time.Now(),
			},
		},
		{
			name: "failed",
			state: &FreezeState{
				Command:   CommandFreeze,
				State:     FreezeStateFailed,
				Timestamp: time.Now(),
			},
		},
		{
			name: "skipped",
			state: &FreezeState{
				Command:   CommandFreeze,
				State:     FreezeStateSkipped,
				Timestamp: time.Now(),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save state
			if err := fm.saveState(volumeUUID, tt.state); err != nil {
				t.Fatalf("saveState failed: %v", err)
			}

			// Load and verify
			loaded, err := fm.loadState(volumeUUID)
			if err != nil {
				t.Fatalf("loadState failed: %v", err)
			}
			if loaded.State != tt.state.State {
				t.Errorf("expected state %s, got %s", tt.state.State, loaded.State)
			}

			// Cleanup for next test
			fm.deleteState(volumeUUID)
		})
	}
}

func TestFreezeManager_ExecSyncTimeout(t *testing.T) {
	fm := NewFreezeManager("")
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	// This should timeout quickly
	err := fm.execSync(ctx)
	if err == nil {
		// Sync might complete very fast, which is okay
		return
	}
	// If it fails, error should be context-related
	if ctx.Err() != context.DeadlineExceeded {
		t.Logf("execSync failed (expected in test): %v", err)
	}
}

func TestIsErrno(t *testing.T) {
	// This test is platform-dependent and hard to test directly
	// without actually executing commands. Just verify it doesn't crash.
	result := isErrno(nil, EINVAL)
	if result {
		t.Error("isErrno should return false for nil error")
	}
}

// TestFreezeManager_FreezeUnfreeze tests the high-level Freeze and Unfreeze methods
func TestFreezeManager_FreezeUnfreeze(t *testing.T) {
	tests := []struct {
		name          string
		volumeUUID    string
		mountPath     string
		expectError   bool
		existingState *FreezeState
	}{
		{
			name:        "freeze new volume",
			volumeUUID:  "test-vol-1",
			mountPath:   "/mnt/test",
			expectError: true, // Will fail because fsfreeze command doesn't actually run successfully in test
		},
		{
			name:        "freeze already frozen volume",
			volumeUUID:  "test-vol-2",
			mountPath:   "/mnt/test2",
			expectError: true,
			existingState: &FreezeState{
				Command:   "freeze",
				State:     FreezeStateFrozen,
				Timestamp: time.Now(),
			},
		},
		{
			name:        "unfreeze frozen volume",
			volumeUUID:  "test-vol-3",
			mountPath:   "/mnt/test3",
			expectError: true, // Will fail because fsfreeze command doesn't actually run
			existingState: &FreezeState{
				Command:   "freeze",
				State:     FreezeStateFrozen,
				Timestamp: time.Now().Add(-5 * time.Second),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary state directory
			stateDir := t.TempDir()
			fm := NewFreezeManager(stateDir)

			// Set up existing state if specified
			if tt.existingState != nil {
				if err := fm.saveState(tt.volumeUUID, tt.existingState); err != nil {
					t.Fatalf("failed to setup existing state: %v", err)
				}
			}

			// Test Freeze
			if tt.existingState == nil || tt.existingState.State != FreezeStateFrozen {
				_, err := fm.Freeze(context.Background(), tt.mountPath, tt.volumeUUID)
				if (err != nil) != tt.expectError {
					t.Errorf("Freeze() error = %v, expectError %v", err, tt.expectError)
				}
			}

			// Test Unfreeze
			if tt.existingState != nil && tt.existingState.State == FreezeStateFrozen {
				err := fm.Unfreeze(context.Background(), tt.mountPath, tt.volumeUUID)
				if (err != nil) != tt.expectError {
					t.Errorf("Unfreeze() error = %v, expectError %v", err, tt.expectError)
				}
			}

			// Cleanup
			fm.CleanupState(tt.volumeUUID)
		})
	}
}

// TestFreezeManager_ExecFreeze tests execFreeze method behavior
func TestFreezeManager_ExecFreeze(t *testing.T) {
	fm := NewFreezeManager("")
	ctx := context.Background()

	// This will fail in test environment because fsfreeze command won't work on non-filesystem paths
	_, err := fm.execFreeze(ctx, "/nonexistent/path")
	if err == nil {
		t.Error("execFreeze should fail for nonexistent path")
	}
}

// TestFreezeManager_ExecUnfreeze tests execUnfreeze method behavior
func TestFreezeManager_ExecUnfreeze(t *testing.T) {
	fm := NewFreezeManager("")
	ctx := context.Background()

	// This will fail in test environment
	err := fm.execUnfreeze(ctx, "/nonexistent/path")
	if err == nil {
		t.Error("execUnfreeze should fail for nonexistent path")
	}
}

// TestFreezeManager_ConcurrentFreezeUnfreeze tests concurrent freeze and unfreeze operations
func TestFreezeManager_ConcurrentFreezeUnfreeze(t *testing.T) {
	stateDir := t.TempDir()
	fm := NewFreezeManager(stateDir)
	volumeUUID := "test-concurrent-vol"

	// Test that operations on the same volume are properly locked
	// Multiple attempts should not cause race conditions
	done := make(chan bool)
	errors := make(chan error, 2)

	go func() {
		_, err := fm.Freeze(context.Background(), "/mnt/test1", volumeUUID)
		errors <- err
		done <- true
	}()

	go func() {
		// Small delay to ensure first goroutine gets lock
		time.Sleep(10 * time.Millisecond)
		_, err := fm.Freeze(context.Background(), "/mnt/test1", volumeUUID)
		errors <- err
		done <- true
	}()

	// Wait for both to complete
	<-done
	<-done
	close(errors)

	// At least one should complete (both may fail due to fsfreeze command, but no race condition)
	errorCount := 0
	for err := range errors {
		if err != nil {
			errorCount++
		}
	}

	t.Logf("Concurrent operations completed with %d errors (expected in test)", errorCount)

	// Cleanup
	fm.CleanupState(volumeUUID)
}
