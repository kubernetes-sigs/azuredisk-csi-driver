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
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"k8s.io/klog/v2"
)

const (
	// FreezeState constants
	FreezeStateSkipped    = "skipped"
	FreezeStateUserFrozen = "user-frozen"
	FreezeStateFrozen     = "frozen"
	FreezeStateFreezing   = "freezing"
	FreezeStateUnfrozen   = "unfrozen"
	FreezeStateFailed     = "failed"

	// Command constants
	CommandFreeze   = "freeze"
	CommandUnfreeze = "unfreeze"

	// State file directory
	StateFileDir = "/tmp"

	// FsFreeze command paths
	FsFreezeCommand = "/sbin/fsfreeze"
	SyncCommand     = "/bin/sync"
)

// errno values for fsfreeze/fsunfreeze operations
const (
	EBADF      = syscall.Errno(9)  // Invalid file descriptor
	ENOTTY     = syscall.Errno(25) // Inappropriate ioctl
	EPERM      = syscall.Errno(1)  // Operation not permitted
	EOPNOTSUPP = syscall.Errno(95) // Operation not supported
	EBUSY      = syscall.Errno(16) // Device or resource busy (already frozen)
	EINVAL     = syscall.Errno(22) // Invalid argument
	EINTR      = syscall.Errno(4)  // Interrupted system call
	EIO        = syscall.Errno(5)  // I/O error
)

// FreezeState represents the state of a volume freeze operation
type FreezeState struct {
	Command   string    `json:"command"`
	State     string    `json:"state"`
	Timestamp time.Time `json:"timestamp"`
}

// FreezeManager handles filesystem freeze/unfreeze operations
type FreezeManager struct {
	stateDir string
	mu       sync.RWMutex
	// Track ongoing operations to prevent concurrent freeze/unfreeze on same path
	operations map[string]*sync.Mutex
}

// NewFreezeManager creates a new FreezeManager
func NewFreezeManager(stateDir string) *FreezeManager {
	if stateDir == "" {
		stateDir = StateFileDir
	}
	return &FreezeManager{
		stateDir:   stateDir,
		operations: make(map[string]*sync.Mutex),
	}
}

// getOperationLock returns a mutex for the given volume UUID to ensure serial operations
func (fm *FreezeManager) getOperationLock(volumeUUID string) *sync.Mutex {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	if lock, exists := fm.operations[volumeUUID]; exists {
		return lock
	}

	lock := &sync.Mutex{}
	fm.operations[volumeUUID] = lock
	return lock
}

// Freeze freezes the filesystem at the given mount path
func (fm *FreezeManager) Freeze(ctx context.Context, mountPath string, volumeUUID string) (string, error) {
	lock := fm.getOperationLock(volumeUUID)
	lock.Lock()
	defer lock.Unlock()

	klog.V(2).Infof("Freeze: attempting to freeze filesystem at %s for volume %s", mountPath, volumeUUID)

	// Check if already frozen (from state file)
	state, err := fm.loadState(volumeUUID)
	if err == nil && state != nil {
		if state.Command == CommandFreeze {
			if state.State == FreezeStateFrozen {
				klog.V(2).Infof("Freeze: filesystem already frozen at %s", mountPath)
				return FreezeStateFrozen, nil
			}
			// If state shows freezing but no state, we need to unfreeze first and retry
			klog.Warningf("Freeze: found stale freeze state for %s, attempting unfreeze first", mountPath)
			if err := fm.execUnfreeze(ctx, mountPath); err != nil {
				klog.Errorf("Freeze: failed to unfreeze stale state: %v", err)
			}
		}
	}

	// Create state file with freezing state
	freezeState := &FreezeState{
		Command:   CommandFreeze,
		State:     FreezeStateFreezing,
		Timestamp: time.Now(),
	}
	if err := fm.saveState(volumeUUID, freezeState); err != nil {
		return FreezeStateFailed, fmt.Errorf("failed to save freeze state: %v", err)
	}

	// Execute sync before freeze
	if err := fm.execSync(ctx); err != nil {
		klog.Warningf("Freeze: sync failed but continuing: %v", err)
	}

	// Execute fsfreeze
	resultState, err := fm.execFreeze(ctx, mountPath)

	// Update state file
	freezeState.State = resultState
	freezeState.Timestamp = time.Now()
	if saveErr := fm.saveState(volumeUUID, freezeState); saveErr != nil {
		klog.Errorf("Freeze: failed to save final state: %v", saveErr)
	}

	if err != nil {
		return resultState, err
	}

	klog.V(2).Infof("Freeze: successfully frozen filesystem at %s with state %s", mountPath, resultState)
	return resultState, nil
}

// Unfreeze unfreezes the filesystem at the given mount path
func (fm *FreezeManager) Unfreeze(ctx context.Context, mountPath string, volumeUUID string) error {
	lock := fm.getOperationLock(volumeUUID)
	lock.Lock()
	defer lock.Unlock()

	klog.V(2).Infof("Unfreeze: attempting to unfreeze filesystem at %s for volume %s", mountPath, volumeUUID)

	// Check state file
	state, err := fm.loadState(volumeUUID)
	if err != nil || state == nil {
		klog.V(2).Infof("Unfreeze: no state file found, nothing to unfreeze at %s", mountPath)
		return nil
	}

	if state.State != FreezeStateFrozen {
		klog.V(2).Infof("Unfreeze: filesystem not in frozen state (%s), skipping unfreeze at %s", state.State, mountPath)
		// Clean up state file
		fm.deleteState(volumeUUID)
		return nil
	}

	// Execute fsfreeze --unfreeze
	if err := fm.execUnfreeze(ctx, mountPath); err != nil {
		// For EINVAL, treat as success (already unfrozen)
		if isErrno(err, EINVAL) {
			klog.V(2).Infof("Unfreeze: filesystem already unfrozen at %s", mountPath)
			fm.deleteState(volumeUUID)
			return nil
		}
		return fmt.Errorf("failed to unfreeze filesystem: %v", err)
	}

	// Update state to unfrozen
	state.State = FreezeStateUnfrozen
	state.Timestamp = time.Now()
	if err := fm.saveState(volumeUUID, state); err != nil {
		klog.Errorf("Unfreeze: failed to save unfrozen state: %v", err)
	}

	// Clean up state file after successful unfreeze
	fm.deleteState(volumeUUID)

	klog.V(2).Infof("Unfreeze: successfully unfrozen filesystem at %s", mountPath)
	return nil
}

// execSync runs the sync command to flush buffers
func (fm *FreezeManager) execSync(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, SyncCommand)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sync command failed: %v, output: %s", err, string(output))
	}
	return nil
}

// execFreeze executes the fsfreeze --freeze command
func (fm *FreezeManager) execFreeze(ctx context.Context, mountPath string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, FsFreezeCommand, "--freeze", mountPath)
	output, err := cmd.CombinedOutput()

	if err != nil {
		klog.Errorf("Freeze: fsfreeze command failed for %s: %v, output: %s", mountPath, err, string(output))

		// Analyze the error
		state := fm.analyzeFreezeError(err, string(output))
		return state, fmt.Errorf("fsfreeze failed with state %s: %v", state, err)
	}

	return FreezeStateFrozen, nil
}

// execUnfreeze executes the fsfreeze --unfreeze command
func (fm *FreezeManager) execUnfreeze(ctx context.Context, mountPath string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, FsFreezeCommand, "--unfreeze", mountPath)
	output, err := cmd.CombinedOutput()

	if err != nil {
		klog.Errorf("Unfreeze: fsfreeze unfreeze command failed for %s: %v, output: %s", mountPath, err, string(output))
		return fmt.Errorf("fsfreeze --unfreeze failed: %v, output: %s", err, string(output))
	}

	return nil
}

// analyzeFreezeError analyzes the error from fsfreeze and returns appropriate state
func (fm *FreezeManager) analyzeFreezeError(err error, output string) string {
	if err == nil {
		return FreezeStateFrozen
	}

	// Check for specific errno values
	if exitErr, ok := err.(*exec.ExitError); ok {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			errno := syscall.Errno(status.ExitStatus())

			switch {
			case errno == EBADF || errno == ENOTTY:
				// Invalid file descriptor or inappropriate ioctl - skip
				klog.V(2).Infof("Freeze: mount not valid for freeze (errno %d), skipping", errno)
				return FreezeStateSkipped

			case errno == EBUSY:
				// Already frozen - could be user-frozen or double freeze
				klog.V(2).Infof("Freeze: filesystem already frozen (EBUSY)")
				return FreezeStateUserFrozen

			case errno == EINVAL:
				// Invalid argument - could be unmounting
				klog.V(2).Infof("Freeze: invalid argument (errno %d), skipping", errno)
				return FreezeStateSkipped

			case errno == EINTR:
				// Interrupted - can retry; but lets mark it failed
				klog.V(2).Infof("Freeze: interrupted (errno %d), marking as failed for retry", errno)
				return FreezeStateFailed

			case errno == EIO:
				// I/O error - sync or journal flush failure
				klog.Errorf("Freeze: I/O error (errno %d), marking as failed", errno)
				return FreezeStateFailed

			case errno == EPERM || errno == EOPNOTSUPP:
				// Permission or not supported
				klog.Errorf("Freeze: permission denied or not supported (errno %d)", errno)
				return FreezeStateSkipped
			}
		}
	}

	// Check output for specific error messages
	outputLower := strings.ToLower(output)
	if strings.Contains(outputLower, "device or resource busy") {
		return FreezeStateUserFrozen
	}
	if strings.Contains(outputLower, "not supported") || strings.Contains(outputLower, "not a directory") {
		return FreezeStateSkipped
	}
	if strings.Contains(outputLower, "not found") || strings.Contains(outputLower, "no such file") {
		return FreezeStateSkipped
	}

	// Default to failed
	return FreezeStateFailed
}

// isErrno checks if the error matches the given errno
func isErrno(err error, errno syscall.Errno) bool {
	if exitErr, ok := err.(*exec.ExitError); ok {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			return syscall.Errno(status.ExitStatus()) == errno
		}
	}
	return false
}

// saveState saves the freeze state to a file
func (fm *FreezeManager) saveState(volumeUUID string, state *FreezeState) error {
	stateFile := fm.getStateFilePath(volumeUUID)

	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to marshal state: %v", err)
	}

	// Ensure directory exists
	if err := os.MkdirAll(fm.stateDir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %v", err)
	}

	// Write atomically using temp file
	tempFile := stateFile + ".tmp"
	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write state file: %v", err)
	}

	if err := os.Rename(tempFile, stateFile); err != nil {
		os.Remove(tempFile)
		return fmt.Errorf("failed to rename state file: %v", err)
	}

	return nil
}

// loadState loads the freeze state from a file
func (fm *FreezeManager) loadState(volumeUUID string) (*FreezeState, error) {
	stateFile := fm.getStateFilePath(volumeUUID)

	data, err := os.ReadFile(stateFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read state file: %v", err)
	}

	var state FreezeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to unmarshal state: %v", err)
	}

	return &state, nil
}

// deleteState deletes the state file
func (fm *FreezeManager) deleteState(volumeUUID string) {
	stateFile := fm.getStateFilePath(volumeUUID)
	if err := os.Remove(stateFile); err != nil && !os.IsNotExist(err) {
		klog.Errorf("Failed to delete state file %s: %v", stateFile, err)
	}
}

// getStateFilePath returns the path to the state file for a volume
func (fm *FreezeManager) getStateFilePath(volumeUUID string) string {
	return filepath.Join(fm.stateDir, volumeUUID+".json")
}

// GetState returns the current state for a volume
func (fm *FreezeManager) GetState(volumeUUID string) (*FreezeState, error) {
	return fm.loadState(volumeUUID)
}

// ListFrozenVolumes returns a list of volume UUIDs that are currently frozen
func (fm *FreezeManager) ListFrozenVolumes() (map[string]*FreezeState, error) {
	frozenVolumes := make(map[string]*FreezeState)

	// Read all state files from state directory
	files, err := os.ReadDir(fm.stateDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read state directory: %v", err)
	}

	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}

		// Extract volume UUID from filename
		volumeUUID := strings.TrimSuffix(file.Name(), ".json")

		// Load state
		state, err := fm.loadState(volumeUUID)
		if err != nil {
			klog.Warningf("Failed to load state for volume %s: %v", volumeUUID, err)
			continue
		}

		// Only include frozen volumes
		if state != nil && state.Command == CommandFreeze && state.State == FreezeStateFrozen {
			frozenVolumes[volumeUUID] = state
		}
	}

	return frozenVolumes, nil
}

// CleanupState removes the state file for a volume
func (fm *FreezeManager) CleanupState(volumeUUID string) {
	fm.deleteState(volumeUUID)
}
