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

package azuredisk

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
)

// TestBootstrapVolumeAttachmentTracking tests the bootstrap functionality with pagination
func TestBootstrapVolumeAttachmentTracking(t *testing.T) {
	ctx := context.Background()
	
	t.Run("successful bootstrap with no VolumeAttachments", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		freezeOrch := NewFreezeOrchestrator(client, "best-effort", 5)

		err := freezeOrch.BootstrapVolumeAttachmentTracking(ctx)
		if err != nil {
			t.Fatalf("BootstrapVolumeAttachmentTracking failed: %v", err)
		}

		if !freezeOrch.isVolumeAttachmentTrackerInitialized() {
			t.Error("tracker should be initialized after bootstrap")
		}

		if freezeOrch.vaTrackerInitFailed.Load() {
			t.Error("tracker should not be marked as failed after successful bootstrap")
		}
	})

	t.Run("successful bootstrap with multiple VolumeAttachments", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		freezeOrch := NewFreezeOrchestrator(client, "best-effort", 5)

		// Create PVs and VolumeAttachments
		for i := 0; i < 10; i++ {
			pvName := ptr.To("test-pv-" + string(rune(i)))
			volumeHandle := "test-volume-" + string(rune(i))

			// Create PV
			pv := &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: *pvName,
				},
				Spec: corev1.PersistentVolumeSpec{
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							VolumeHandle: volumeHandle,
						},
					},
				},
			}
			_, err := client.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{})
			if err != nil {
				t.Fatalf("failed to create PV: %v", err)
			}

			// Create VolumeAttachment
			va := &storagev1.VolumeAttachment{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-va-" + string(rune(i)),
				},
				Spec: storagev1.VolumeAttachmentSpec{
					NodeName: "test-node",
					Source: storagev1.VolumeAttachmentSource{
						PersistentVolumeName: pvName,
					},
				},
				Status: storagev1.VolumeAttachmentStatus{
					Attached: true,
				},
			}
			_, err = client.StorageV1().VolumeAttachments().Create(ctx, va, metav1.CreateOptions{})
			if err != nil {
				t.Fatalf("failed to create VA: %v", err)
			}
		}

		err := freezeOrch.BootstrapVolumeAttachmentTracking(ctx)
		if err != nil {
			t.Fatalf("BootstrapVolumeAttachmentTracking failed: %v", err)
		}

		if !freezeOrch.isVolumeAttachmentTrackerInitialized() {
			t.Error("tracker should be initialized after bootstrap")
		}

		// Verify all VolumeAttachments are tracked
		freezeOrch.volumeAttachmentsMu.RLock()
		trackedCount := len(freezeOrch.volumeAttachments)
		freezeOrch.volumeAttachmentsMu.RUnlock()

		if trackedCount != 10 {
			t.Errorf("expected 10 tracked VolumeAttachments, got %d", trackedCount)
		}
	})

	t.Run("bootstrap skips VolumeAttachments without PV", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		freezeOrch := NewFreezeOrchestrator(client, "best-effort", 5)

		pvName := "test-pv"

		// Create VolumeAttachment without corresponding PV
		va := &storagev1.VolumeAttachment{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-va",
			},
			Spec: storagev1.VolumeAttachmentSpec{
				NodeName: "test-node",
				Source: storagev1.VolumeAttachmentSource{
					PersistentVolumeName: &pvName,
				},
			},
		}
		_, err := client.StorageV1().VolumeAttachments().Create(ctx, va, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("failed to create VA: %v", err)
		}

		err = freezeOrch.BootstrapVolumeAttachmentTracking(ctx)
		if err != nil {
			t.Fatalf("BootstrapVolumeAttachmentTracking failed: %v", err)
		}

		// Verify VA was skipped (not tracked)
		freezeOrch.volumeAttachmentsMu.RLock()
		trackedCount := len(freezeOrch.volumeAttachments)
		freezeOrch.volumeAttachmentsMu.RUnlock()

		if trackedCount != 0 {
			t.Errorf("expected 0 tracked VolumeAttachments, got %d", trackedCount)
		}
	})

	t.Run("bootstrap skips VolumeAttachments without CSI volumeHandle", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		freezeOrch := NewFreezeOrchestrator(client, "best-effort", 5)

		pvName := "test-pv"

		// Create PV without CSI volumeHandle
		pv := &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name: pvName,
			},
			Spec: corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/tmp",
					},
				},
			},
		}
		_, err := client.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("failed to create PV: %v", err)
		}

		// Create VolumeAttachment
		va := &storagev1.VolumeAttachment{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-va",
			},
			Spec: storagev1.VolumeAttachmentSpec{
				NodeName: "test-node",
				Source: storagev1.VolumeAttachmentSource{
					PersistentVolumeName: &pvName,
				},
			},
		}
		_, err = client.StorageV1().VolumeAttachments().Create(ctx, va, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("failed to create VA: %v", err)
		}

		err = freezeOrch.BootstrapVolumeAttachmentTracking(ctx)
		if err != nil {
			t.Fatalf("BootstrapVolumeAttachmentTracking failed: %v", err)
		}

		// Verify VA was skipped (not tracked)
		freezeOrch.volumeAttachmentsMu.RLock()
		trackedCount := len(freezeOrch.volumeAttachments)
		freezeOrch.volumeAttachmentsMu.RUnlock()

		if trackedCount != 0 {
			t.Errorf("expected 0 tracked VolumeAttachments, got %d", trackedCount)
		}
	})

	t.Run("bootstrap is idempotent", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		freezeOrch := NewFreezeOrchestrator(client, "best-effort", 5)

		// First bootstrap
		err := freezeOrch.BootstrapVolumeAttachmentTracking(ctx)
		if err != nil {
			t.Fatalf("first BootstrapVolumeAttachmentTracking failed: %v", err)
		}

		// Second bootstrap should skip
		err = freezeOrch.BootstrapVolumeAttachmentTracking(ctx)
		if err != nil {
			t.Fatalf("second BootstrapVolumeAttachmentTracking failed: %v", err)
		}

		if !freezeOrch.isVolumeAttachmentTrackerInitialized() {
			t.Error("tracker should still be initialized after second bootstrap")
		}
	})
}

// TestStartVolumeAttachmentInformer tests the informer startup
func TestStartVolumeAttachmentInformer(t *testing.T) {
	ctx := context.Background()

	t.Run("informer requires initialized tracker", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		freezeOrch := NewFreezeOrchestrator(client, "best-effort", 5)

		err := freezeOrch.StartVolumeAttachmentInformer(ctx)
		if err == nil {
			t.Error("expected error when starting informer without initialized tracker")
		}
	})

	t.Run("successful informer start", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		freezeOrch := NewFreezeOrchestrator(client, "best-effort", 5)

		// Bootstrap first
		err := freezeOrch.BootstrapVolumeAttachmentTracking(ctx)
		if err != nil {
			t.Fatalf("BootstrapVolumeAttachmentTracking failed: %v", err)
		}

		// Start informer in goroutine (since it blocks on WaitForCacheSync)
		errCh := make(chan error, 1)
		go func() {
			errCh <- freezeOrch.StartVolumeAttachmentInformer(ctx)
		}()

		// Give it time to start
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("StartVolumeAttachmentInformer failed: %v", err)
			}
		case <-time.After(2 * time.Second):
			// Expected - informer is running in background
		}

		// Cleanup
		freezeOrch.StopVolumeAttachmentInformer()
	})
}

// TestVolumeAttachmentInformerEventHandlers tests the Add/Update/Delete handlers
func TestVolumeAttachmentInformerEventHandlers(t *testing.T) {
	ctx := context.Background()

	t.Run("handleVolumeAttachmentAdd", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		freezeOrch := NewFreezeOrchestrator(client, "best-effort", 5)

		pvName := "test-pv"
		volumeHandle := "test-volume"

		// Create PV
		pv := &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name: pvName,
			},
			Spec: corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					CSI: &corev1.CSIPersistentVolumeSource{
						VolumeHandle: volumeHandle,
					},
				},
			},
		}
		_, err := client.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("failed to create PV: %v", err)
		}

		// Create VolumeAttachment
		va := &storagev1.VolumeAttachment{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-va",
			},
			Spec: storagev1.VolumeAttachmentSpec{
				NodeName: "test-node",
				Source: storagev1.VolumeAttachmentSource{
					PersistentVolumeName: &pvName,
				},
			},
			Status: storagev1.VolumeAttachmentStatus{
				Attached: true,
			},
		}

		// Call handler
		freezeOrch.handleVolumeAttachmentAdd(ctx, va)

		// Verify VA was added to tracker
		trackedVA, exists := freezeOrch.getVolumeAttachmentFromTracker(volumeHandle)
		if !exists {
			t.Error("VolumeAttachment should be tracked")
		}
		if trackedVA.Name != va.Name {
			t.Errorf("expected VA name %s, got %s", va.Name, trackedVA.Name)
		}
	})

	t.Run("handleVolumeAttachmentUpdate", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		freezeOrch := NewFreezeOrchestrator(client, "best-effort", 5)

		pvName := "test-pv"
		volumeHandle := "test-volume"

		// Create PV
		pv := &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name: pvName,
			},
			Spec: corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					CSI: &corev1.CSIPersistentVolumeSource{
						VolumeHandle: volumeHandle,
					},
				},
			},
		}
		_, err := client.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("failed to create PV: %v", err)
		}

		// Create VolumeAttachment with attached=false
		va := &storagev1.VolumeAttachment{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-va",
			},
			Spec: storagev1.VolumeAttachmentSpec{
				NodeName: "test-node",
				Source: storagev1.VolumeAttachmentSource{
					PersistentVolumeName: &pvName,
				},
			},
			Status: storagev1.VolumeAttachmentStatus{
				Attached: false,
			},
		}

		freezeOrch.handleVolumeAttachmentAdd(ctx, va)

		// Update to attached=true
		va.Status.Attached = true
		freezeOrch.handleVolumeAttachmentUpdate(ctx, va)

		// Verify updated VA is tracked with new status
		trackedVA, exists := freezeOrch.getVolumeAttachmentFromTracker(volumeHandle)
		if !exists {
			t.Error("VolumeAttachment should be tracked")
		}
		if !trackedVA.Status.Attached {
			t.Error("VolumeAttachment status should be updated to attached")
		}
	})

	t.Run("handleVolumeAttachmentDelete", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		freezeOrch := NewFreezeOrchestrator(client, "best-effort", 5)

		pvName := "test-pv"
		volumeHandle := "test-volume"

		// Create PV
		pv := &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name: pvName,
			},
			Spec: corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					CSI: &corev1.CSIPersistentVolumeSource{
						VolumeHandle: volumeHandle,
					},
				},
			},
		}
		_, err := client.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("failed to create PV: %v", err)
		}

		// Create VolumeAttachment
		va := &storagev1.VolumeAttachment{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-va",
			},
			Spec: storagev1.VolumeAttachmentSpec{
				NodeName: "test-node",
				Source: storagev1.VolumeAttachmentSource{
					PersistentVolumeName: &pvName,
				},
			},
		}

		// Add to tracker
		freezeOrch.handleVolumeAttachmentAdd(ctx, va)

		// Verify it's tracked
		_, exists := freezeOrch.getVolumeAttachmentFromTracker(volumeHandle)
		if !exists {
			t.Error("VolumeAttachment should be tracked before delete")
		}

		// Delete
		freezeOrch.handleVolumeAttachmentDelete(ctx, va)

		// Verify it's removed from tracker
		_, exists = freezeOrch.getVolumeAttachmentFromTracker(volumeHandle)
		if exists {
			t.Error("VolumeAttachment should be removed from tracker after delete")
		}
	})

	t.Run("handleVolumeAttachmentDelete when PV is also deleted", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		freezeOrch := NewFreezeOrchestrator(client, "best-effort", 5)

		pvName := "test-pv"
		volumeHandle := "test-volume"

		// Create PV
		pv := &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name: pvName,
			},
			Spec: corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					CSI: &corev1.CSIPersistentVolumeSource{
						VolumeHandle: volumeHandle,
					},
				},
			},
		}
		_, err := client.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("failed to create PV: %v", err)
		}

		// Create VolumeAttachment
		va := &storagev1.VolumeAttachment{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-va",
			},
			Spec: storagev1.VolumeAttachmentSpec{
				NodeName: "test-node",
				Source: storagev1.VolumeAttachmentSource{
					PersistentVolumeName: &pvName,
				},
			},
		}

		// Add to tracker
		freezeOrch.handleVolumeAttachmentAdd(ctx, va)

		// Delete PV
		err = client.CoreV1().PersistentVolumes().Delete(ctx, pvName, metav1.DeleteOptions{})
		if err != nil {
			t.Fatalf("failed to delete PV: %v", err)
		}

		// Delete VA (PV doesn't exist now)
		freezeOrch.handleVolumeAttachmentDelete(ctx, va)

		// Verify VA is still removed from tracker (using name lookup)
		_, exists := freezeOrch.getVolumeAttachmentFromTracker(volumeHandle)
		if exists {
			t.Error("VolumeAttachment should be removed from tracker even when PV is deleted")
		}
	})
}

// TestIsVolumeAttached_TrackerStates tests isVolumeAttached with different tracker states
func TestIsVolumeAttached_TrackerStates(t *testing.T) {
	ctx := context.Background()

	t.Run("returns error when tracker not initialized", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		freezeOrch := NewFreezeOrchestrator(client, "best-effort", 5)

		volumeHandle := "test-volume"

		va, err := freezeOrch.isVolumeAttached(ctx, volumeHandle)
		if err == nil {
			t.Error("expected error when tracker not initialized")
		}
		if va != nil {
			t.Error("should return nil VA when tracker not initialized")
		}
	})

	t.Run("returns nil when tracker initialization failed", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		freezeOrch := NewFreezeOrchestrator(client, "best-effort", 5)

		// Mark tracker as failed
		freezeOrch.vaTrackerInitFailed.Store(true)

		volumeHandle := "test-volume"

		va, err := freezeOrch.isVolumeAttached(ctx, volumeHandle)
		if err != nil {
			t.Errorf("should not return error when tracker init failed: %v", err)
		}
		if va != nil {
			t.Error("should return nil VA when tracker init failed (allows fallback)")
		}
	})

	t.Run("returns VA when tracker initialized and volume attached", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		freezeOrch := NewFreezeOrchestrator(client, "best-effort", 5)

		pvName := "test-pv"
		volumeHandle := "test-volume"

		// Create PV
		pv := &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name: pvName,
			},
			Spec: corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					CSI: &corev1.CSIPersistentVolumeSource{
						VolumeHandle: volumeHandle,
					},
				},
			},
		}
		_, err := client.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("failed to create PV: %v", err)
		}

		// Create VolumeAttachment
		vaToCreate := &storagev1.VolumeAttachment{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-va",
			},
			Spec: storagev1.VolumeAttachmentSpec{
				NodeName: "test-node",
				Source: storagev1.VolumeAttachmentSource{
					PersistentVolumeName: &pvName,
				},
			},
			Status: storagev1.VolumeAttachmentStatus{
				Attached: true,
			},
		}

		// Bootstrap tracker
		err = freezeOrch.BootstrapVolumeAttachmentTracking(ctx)
		if err != nil {
			t.Fatalf("BootstrapVolumeAttachmentTracking failed: %v", err)
		}

		// Add VA to tracker manually (simulating informer event)
		freezeOrch.handleVolumeAttachmentAdd(ctx, vaToCreate)

		// Test
		va, err := freezeOrch.isVolumeAttached(ctx, volumeHandle)
		if err != nil {
			t.Errorf("isVolumeAttached failed: %v", err)
		}
		if va == nil {
			t.Error("should return VA when attached")
		}
		if va != nil && !va.Status.Attached {
			t.Error("VA status should be attached")
		}
	})

	t.Run("returns nil when volume not attached", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		freezeOrch := NewFreezeOrchestrator(client, "best-effort", 5)

		pvName := "test-pv"
		volumeHandle := "test-volume"

		// Create PV
		pv := &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name: pvName,
			},
			Spec: corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					CSI: &corev1.CSIPersistentVolumeSource{
						VolumeHandle: volumeHandle,
					},
				},
			},
		}
		_, err := client.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("failed to create PV: %v", err)
		}

		// Create VolumeAttachment with attached=false
		vaToCreate := &storagev1.VolumeAttachment{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-va",
			},
			Spec: storagev1.VolumeAttachmentSpec{
				NodeName: "test-node",
				Source: storagev1.VolumeAttachmentSource{
					PersistentVolumeName: &pvName,
				},
			},
			Status: storagev1.VolumeAttachmentStatus{
				Attached: false,
			},
		}

		// Bootstrap tracker
		err = freezeOrch.BootstrapVolumeAttachmentTracking(ctx)
		if err != nil {
			t.Fatalf("BootstrapVolumeAttachmentTracking failed: %v", err)
		}

		// Add VA to tracker
		freezeOrch.handleVolumeAttachmentAdd(ctx, vaToCreate)

		// Test
		va, err := freezeOrch.isVolumeAttached(ctx, volumeHandle)
		if err != nil {
			t.Errorf("isVolumeAttached failed: %v", err)
		}
		if va != nil {
			t.Error("should return nil when VA exists but not attached")
		}
	})

	t.Run("returns nil when volume not in tracker", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		freezeOrch := NewFreezeOrchestrator(client, "best-effort", 5)

		// Bootstrap tracker
		err := freezeOrch.BootstrapVolumeAttachmentTracking(ctx)
		if err != nil {
			t.Fatalf("BootstrapVolumeAttachmentTracking failed: %v", err)
		}

		// Test with non-existent volume
		va, err := freezeOrch.isVolumeAttached(ctx, "non-existent-volume")
		if err != nil {
			t.Errorf("isVolumeAttached failed: %v", err)
		}
		if va != nil {
			t.Error("should return nil when volume not in tracker")
		}
	})
}

// TestGetVolumeAttachmentFromTracker tests the tracker retrieval
func TestGetVolumeAttachmentFromTracker(t *testing.T) {
	client := fake.NewSimpleClientset()
	freezeOrch := NewFreezeOrchestrator(client, "best-effort", 5)

	volumeHandle := "test-volume"
	va := &storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-va",
		},
		Spec: storagev1.VolumeAttachmentSpec{
			NodeName: "test-node",
		},
		Status: storagev1.VolumeAttachmentStatus{
			Attached: true,
		},
	}

	// Add to tracker
	freezeOrch.volumeAttachmentsMu.Lock()
	freezeOrch.volumeAttachments[volumeHandle] = va
	freezeOrch.volumeAttachmentsMu.Unlock()

	// Test retrieval
	retrieved, exists := freezeOrch.getVolumeAttachmentFromTracker(volumeHandle)
	if !exists {
		t.Error("VA should exist in tracker")
	}
	if retrieved.Name != va.Name {
		t.Errorf("expected VA name %s, got %s", va.Name, retrieved.Name)
	}

	// Verify it's a copy (DeepCopy)
	if retrieved == va {
		t.Error("should return a copy, not the original VA")
	}

	// Test non-existent volume
	_, exists = freezeOrch.getVolumeAttachmentFromTracker("non-existent")
	if exists {
		t.Error("non-existent volume should not be in tracker")
	}
}

// TestIsVolumeAttachmentTrackerInitialized tests the initialization check
func TestIsVolumeAttachmentTrackerInitialized(t *testing.T) {
	client := fake.NewSimpleClientset()
	freezeOrch := NewFreezeOrchestrator(client, "best-effort", 5)

	// Initially not initialized
	if freezeOrch.isVolumeAttachmentTrackerInitialized() {
		t.Error("tracker should not be initialized initially")
	}

	// After bootstrap
	err := freezeOrch.BootstrapVolumeAttachmentTracking(context.Background())
	if err != nil {
		t.Fatalf("BootstrapVolumeAttachmentTracking failed: %v", err)
	}

	if !freezeOrch.isVolumeAttachmentTrackerInitialized() {
		t.Error("tracker should be initialized after bootstrap")
	}
}

// TestStopVolumeAttachmentInformer tests informer cleanup
func TestStopVolumeAttachmentInformer(t *testing.T) {
	client := fake.NewSimpleClientset()
	freezeOrch := NewFreezeOrchestrator(client, "best-effort", 5)

	// Bootstrap
	err := freezeOrch.BootstrapVolumeAttachmentTracking(context.Background())
	if err != nil {
		t.Fatalf("BootstrapVolumeAttachmentTracking failed: %v", err)
	}

	// Start informer in background
	go func() {
		_ = freezeOrch.StartVolumeAttachmentInformer(context.Background())
	}()

	// Give it time to start
	time.Sleep(100 * time.Millisecond)

	// Stop informer
	freezeOrch.StopVolumeAttachmentInformer()

	// Verify stop channel is nil after stop
	if freezeOrch.informerStopCh != nil {
		t.Error("informerStopCh should be nil after stop")
	}

	// Stop again should not panic
	freezeOrch.StopVolumeAttachmentInformer()
}

// TestCheckOrRequestFreeze_WithTrackerIntegration tests the full integration
func TestCheckOrRequestFreeze_WithTrackerIntegration(t *testing.T) {
	ctx := context.Background()

	t.Run("freeze proceeds when tracker init failed", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		freezeOrch := NewFreezeOrchestrator(client, "best-effort", 5)

		volumeHandle := "test-volume"
		snapshotName := "test-snapshot"
		pvName := "test-pv"

		// Create PV
		pv := &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name: pvName,
			},
			Spec: corev1.PersistentVolumeSpec{
				VolumeMode: ptr.To(corev1.PersistentVolumeFilesystem),
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					CSI: &corev1.CSIPersistentVolumeSource{
						VolumeHandle: volumeHandle,
					},
				},
			},
		}
		_, err := client.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("failed to create PV: %v", err)
		}

		// Mark tracker as failed
		freezeOrch.vaTrackerInitFailed.Store(true)

		// CheckOrRequestFreeze should proceed without freeze
		state, ready, err := freezeOrch.CheckOrRequestFreeze(ctx, volumeHandle, snapshotName, "default")
		if err != nil {
			t.Errorf("CheckOrRequestFreeze should not error when tracker init failed: %v", err)
		}
		if !ready {
			t.Error("should be ready to proceed when tracker init failed")
		}
		if state != "" {
			t.Errorf("expected empty state (skipped freeze), got %s", state)
		}
	})

	t.Run("freeze retries when tracker not initialized", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		freezeOrch := NewFreezeOrchestrator(client, "best-effort", 5)

		volumeHandle := "test-volume"
		snapshotName := "test-snapshot"
		pvName := "test-pv"

		// Create PV
		pv := &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name: pvName,
			},
			Spec: corev1.PersistentVolumeSpec{
				VolumeMode: ptr.To(corev1.PersistentVolumeFilesystem),
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					CSI: &corev1.CSIPersistentVolumeSource{
						VolumeHandle: volumeHandle,
					},
				},
			},
		}
		_, err := client.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("failed to create PV: %v", err)
		}

		// Tracker not initialized (neither failed nor successful)

		// CheckOrRequestFreeze should return error to trigger retry
		_, ready, err := freezeOrch.CheckOrRequestFreeze(ctx, volumeHandle, snapshotName, "default")
		if err == nil {
			t.Error("expected error when tracker not initialized")
		}
		if ready {
			t.Error("should not be ready when tracker not initialized")
		}
	})
}
