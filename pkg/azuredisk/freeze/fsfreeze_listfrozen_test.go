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
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFreezeManager_ListFrozenVolumes(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "freeze-list-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	fm := NewFreezeManager(tmpDir)

	t.Run("empty directory returns empty map", func(t *testing.T) {
		frozenVolumes, err := fm.ListFrozenVolumes()
		if err != nil {
			t.Errorf("ListFrozenVolumes failed: %v", err)
		}
		if len(frozenVolumes) != 0 {
			t.Errorf("expected empty map, got %d volumes", len(frozenVolumes))
		}
	})

	t.Run("returns only frozen volumes", func(t *testing.T) {
		// Create frozen volume state
		frozenState := &FreezeState{
			Command:   CommandFreeze,
			State:     FreezeStateFrozen,
			Timestamp: time.Now(),
		}
		if err := fm.saveState("frozen-vol-1", frozenState); err != nil {
			t.Fatalf("failed to save frozen state: %v", err)
		}

		// Create unfrozen volume state
		unfrozenState := &FreezeState{
			Command:   CommandUnfreeze,
			State:     FreezeStateUnfrozen,
			Timestamp: time.Now(),
		}
		if err := fm.saveState("unfrozen-vol-1", unfrozenState); err != nil {
			t.Fatalf("failed to save unfrozen state: %v", err)
		}

		// Create failed freeze state
		failedState := &FreezeState{
			Command:   CommandFreeze,
			State:     FreezeStateFailed,
			Timestamp: time.Now(),
		}
		if err := fm.saveState("failed-vol-1", failedState); err != nil {
			t.Fatalf("failed to save failed state: %v", err)
		}

		frozenVolumes, err := fm.ListFrozenVolumes()
		if err != nil {
			t.Errorf("ListFrozenVolumes failed: %v", err)
		}

		// Should only return frozen-vol-1
		if len(frozenVolumes) != 1 {
			t.Errorf("expected 1 frozen volume, got %d", len(frozenVolumes))
		}

		if _, exists := frozenVolumes["frozen-vol-1"]; !exists {
			t.Error("frozen-vol-1 should be in the list")
		}

		if _, exists := frozenVolumes["unfrozen-vol-1"]; exists {
			t.Error("unfrozen-vol-1 should not be in the list")
		}

		if _, exists := frozenVolumes["failed-vol-1"]; exists {
			t.Error("failed-vol-1 should not be in the list")
		}
	})

	t.Run("returns multiple frozen volumes", func(t *testing.T) {
		// Clean up previous test
		os.RemoveAll(tmpDir)
		os.MkdirAll(tmpDir, 0755)

		// Create multiple frozen volumes
		for i := 1; i <= 5; i++ {
			state := &FreezeState{
				Command:   CommandFreeze,
				State:     FreezeStateFrozen,
				Timestamp: time.Now(),
			}
			volumeID := fmt.Sprintf("frozen-vol-%d", i)
			if err := fm.saveState(volumeID, state); err != nil {
				t.Fatalf("failed to save state for volume %s: %v", volumeID, err)
			}
		}

		frozenVolumes, err := fm.ListFrozenVolumes()
		if err != nil {
			t.Errorf("ListFrozenVolumes failed: %v", err)
		}

		if len(frozenVolumes) != 5 {
			t.Errorf("expected 5 frozen volumes, got %d", len(frozenVolumes))
		}

		// Verify all volumes have correct state
		for volumeID, state := range frozenVolumes {
			if state.Command != CommandFreeze {
				t.Errorf("volume %s: expected command freeze, got %s", volumeID, state.Command)
			}
			if state.State != FreezeStateFrozen {
				t.Errorf("volume %s: expected state frozen, got %s", volumeID, state.State)
			}
		}
	})

	t.Run("handles corrupted state files gracefully", func(t *testing.T) {
		// Clean up previous test
		os.RemoveAll(tmpDir)
		os.MkdirAll(tmpDir, 0755)

		// Create a valid frozen volume
		validState := &FreezeState{
			Command:   CommandFreeze,
			State:     FreezeStateFrozen,
			Timestamp: time.Now(),
		}
		if err := fm.saveState("valid-vol", validState); err != nil {
			t.Fatalf("failed to save valid state: %v", err)
		}

		// Create corrupted state file
		corruptedFile := filepath.Join(tmpDir, "corrupted-vol.json")
		if err := os.WriteFile(corruptedFile, []byte("invalid json{{{"), 0644); err != nil {
			t.Fatalf("failed to write corrupted file: %v", err)
		}

		frozenVolumes, err := fm.ListFrozenVolumes()
		if err != nil {
			t.Errorf("ListFrozenVolumes should not fail on corrupted files: %v", err)
		}

		// Should only return the valid volume
		if len(frozenVolumes) != 1 {
			t.Errorf("expected 1 frozen volume, got %d", len(frozenVolumes))
		}

		if _, exists := frozenVolumes["valid-vol"]; !exists {
			t.Error("valid-vol should be in the list")
		}

		if _, exists := frozenVolumes["corrupted-vol"]; exists {
			t.Error("corrupted-vol should not be in the list")
		}
	})

	t.Run("ignores non-json files", func(t *testing.T) {
		// Clean up previous test
		os.RemoveAll(tmpDir)
		os.MkdirAll(tmpDir, 0755)

		// Create frozen volume
		frozenState := &FreezeState{
			Command:   CommandFreeze,
			State:     FreezeStateFrozen,
			Timestamp: time.Now(),
		}
		if err := fm.saveState("frozen-vol", frozenState); err != nil {
			t.Fatalf("failed to save frozen state: %v", err)
		}

		// Create non-json files
		txtFile := filepath.Join(tmpDir, "test.txt")
		if err := os.WriteFile(txtFile, []byte("some text"), 0644); err != nil {
			t.Fatalf("failed to write txt file: %v", err)
		}

		logFile := filepath.Join(tmpDir, "test.log")
		if err := os.WriteFile(logFile, []byte("some log"), 0644); err != nil {
			t.Fatalf("failed to write log file: %v", err)
		}

		frozenVolumes, err := fm.ListFrozenVolumes()
		if err != nil {
			t.Errorf("ListFrozenVolumes failed: %v", err)
		}

		// Should only return frozen-vol
		if len(frozenVolumes) != 1 {
			t.Errorf("expected 1 frozen volume, got %d", len(frozenVolumes))
		}
	})

	t.Run("returns error for non-existent state directory", func(t *testing.T) {
		nonExistentDir := filepath.Join(tmpDir, "non-existent")
		fmNonExistent := NewFreezeManager(nonExistentDir)

		frozenVolumes, err := fmNonExistent.ListFrozenVolumes()
		if err == nil {
			t.Error("ListFrozenVolumes should return error for non-existent directory")
		}

		if frozenVolumes != nil {
			t.Errorf("expected nil map for non-existent directory, got %d volumes", len(frozenVolumes))
		}
	})
}
