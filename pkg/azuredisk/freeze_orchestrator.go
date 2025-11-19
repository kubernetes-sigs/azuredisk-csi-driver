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
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	snapshotclientset "github.com/kubernetes-csi/external-snapshotter/client/v4/clientset/versioned"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/pager"
	"k8s.io/klog/v2"
	"k8s.io/utils/lru"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk/freeze"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
)

const (
	// Maximum number of volumes to cache for skip freeze optimization
	maxSkipFreezeCacheSize = 100
)

// SKUs that should skip freeze checks (can be expanded in the future)
// This is specifically for block volumes with known SKUs where snapshot operations take longer time
// (e.g., PremiumV2_LRS snapshots can take 5+ minutes). By caching these volumes, we optimize the
// retry path to avoid querying VolumeAttachments and other Kubernetes resources on subsequent calls.
var skusToSkipFreeze = []string{
	"PremiumV2_LRS",
}

// FreezeOrchestrator manages the freeze/unfreeze workflow from controller side
type FreezeOrchestrator struct {
	kubeClient               kubernetes.Interface
	snapshotClient           snapshotclientset.Interface
	snapshotConsistencyMode  string
	freezeWaitTimeoutMinutes int64

	// Track ongoing snapshots to handle concurrent requests
	ongoingSnapshots map[string]*snapshotTracking
	mu               sync.RWMutex

	// Per-volume mutexes to serialize snapshot operations at volume granularity
	volumeLocks   map[string]*sync.Mutex
	volumeLocksMu sync.Mutex

	// LRU cache of volumes that should skip freeze (e.g., block volumes with PremiumV2_LRS SKU)
	// Uses LRU eviction policy with O(1) lookup and update performance
	skipFreezeCache *lru.Cache
	skipFreezeMu    sync.RWMutex

	// VolumeAttachment tracking
	// Maps volumeHandle -> *storagev1.VolumeAttachment
	volumeAttachments   map[string]*storagev1.VolumeAttachment
	volumeAttachmentsMu sync.RWMutex
	informerFactory     informers.SharedInformerFactory
	informerStopCh      chan struct{}
	informerStopChMu    sync.Mutex

	// Bootstrap state tracking
	bootstrapComplete    bool
	vaTrackerInitialized atomic.Bool
	vaTrackerInitFailed  atomic.Bool
}

type snapshotTracking struct {
	volumeHandle         string
	snapshotName         string
	snapshotNamespace    string
	freezeRequiredTime   time.Time
	volumeAttachmentName string
}

// NewFreezeOrchestrator creates a new freeze orchestrator
func NewFreezeOrchestrator(
	kubeClient kubernetes.Interface,
	snapshotConsistencyMode string,
	freezeWaitTimeoutMinutes int64,
) *FreezeOrchestrator {
	if freezeWaitTimeoutMinutes <= 0 {
		freezeWaitTimeoutMinutes = azureconstants.DefaultFreezeWaitTimeoutMinutes
	}

	freezeOrch := &FreezeOrchestrator{
		kubeClient:               kubeClient,
		snapshotClient:           nil, // Will be set by driver if available
		snapshotConsistencyMode:  snapshotConsistencyMode,
		freezeWaitTimeoutMinutes: freezeWaitTimeoutMinutes,
		ongoingSnapshots:         make(map[string]*snapshotTracking),
		volumeLocks:              make(map[string]*sync.Mutex),
		skipFreezeCache:          lru.New(maxSkipFreezeCacheSize),
		bootstrapComplete:        false,
		volumeAttachments:        make(map[string]*storagev1.VolumeAttachment),
		// vaTrackerInitialized defaults to false (atomic.Bool zero value)
	}

	return freezeOrch
}

// SetSnapshotClient sets the snapshot client
func (freezeOrch *FreezeOrchestrator) SetSnapshotClient(snapshotClient snapshotclientset.Interface) {
	freezeOrch.snapshotClient = snapshotClient
}

func (freezeOrch *FreezeOrchestrator) isSnapshotStrictMode() bool {
	return freezeOrch.snapshotConsistencyMode == "strict"
}

func (d *Driver) CheckOrRequestFreeze(ctx context.Context, sourceVolumeID, snapshotName string, snapshotNamespace *string) (error, func(bool)) {

	if snapshotNamespace == nil {
		return nil, nil
	}

	namespace := *snapshotNamespace

	// Step 1: Check or request filesystem freeze if enabled
	// Follow CSI pattern: return ready_to_use=false when waiting, ready_to_use=true when done
	var freezeState string
	var freezeComplete bool
	var freezeSkipped bool
	if d.shouldEnableFreeze() {
		orchestrator := d.getFreezeOrchestrator()
		if orchestrator != nil {
			var err error
			freezeState, freezeComplete, err = orchestrator.CheckOrRequestFreeze(ctx, sourceVolumeID, snapshotName, namespace)
			if err != nil {
				// Error in strict mode or unable to set annotation
				klog.Errorf("Failed to check/request freeze for snapshot %s: %v", snapshotName, err)
				orchestrator.ReleaseFreeze(ctx, sourceVolumeID, snapshotName, namespace)
				return status.Errorf(codes.FailedPrecondition,
					"snapshot consistency check failed: %v", err), nil
			} else if !freezeComplete {
				// Freeze not yet complete - return error to trigger retry
				// CSI external-snapshotter will retry CreateSnapshot
				klog.V(2).Infof("Freeze in progress for volume %s, snapshot %s, waiting for completion", sourceVolumeID, snapshotName)

				// Return Unavailable to signal retry is needed
				return status.Error(codes.Unavailable,
					fmt.Sprintf("waiting for filesystem freeze to complete for volume %s", sourceVolumeID)), nil
			} else if freezeState == "" { // freezeComplete and error is nil
				// Freeze complete (or skipped) - proceed with snapshot
				freezeSkipped = true
				klog.V(2).Infof("Freeze skipped for snapshot %s (not a filesystem volume)", snapshotName)
			} else { // freezeComplete, error is nil and freezeState is set
				klog.V(2).Infof("Freeze complete for snapshot %s with state: %s", snapshotName, freezeState)
			}
		}
	} else {
		freezeSkipped = true
	}

	// Defer unfreeze to ensure it happens after snapshot creation
	return nil, func(snapshotCreated bool) {
		if !freezeSkipped && d.shouldEnableFreeze() {
			orchestrator := d.getFreezeOrchestrator()
			if orchestrator != nil {
				if err := orchestrator.ReleaseFreeze(context.Background(), sourceVolumeID, snapshotName, namespace); err != nil {
					klog.Errorf("Failed to release freeze for snapshot %s: %v", snapshotName, err)
				}

				if snapshotCreated && freezeState != "" {
					// Create a kubernetes warning event for freeze state
					d.logFreezeEvent(sourceVolumeID, snapshotName,
						fmt.Sprintf("Snapshot created but filesystem freeze %s. Data consistency is not guaranteed.", freezeState),
						orchestrator.isSnapshotStrictMode())
				}
			}
		}
	}
}

// CheckOrRequestFreeze checks freeze status or requests freeze if needed
// Returns: (freezeState, isReadyToSnapshot, error)
// - freezeState: current state ("", "frozen", "skipped", etc.)
// - isReadyToSnapshot: true if snapshot can proceed, false if need to wait
// for best effort we set isReadyToSnapshot to true even if freeze failed
func (freezeOrch *FreezeOrchestrator) CheckOrRequestFreeze(ctx context.Context, volumeHandle string, snapshotName string, snapshotNamespace string) (string, bool, error) {
	klog.V(4).Infof("CheckOrRequestFreeze: volume %s snapshot %s", volumeHandle, snapshotName)

	// Check if this volume is in the skip list (cached from previous checks)
	if freezeOrch.shouldSkipFreezeFromCache(volumeHandle) {
		klog.V(4).Infof("CheckOrRequestFreeze: volume %s found in skip freeze cache, skipping freeze", volumeHandle)
		return "", true, nil
	}

	// Acquire volume-level lock at the beginning to serialize all operations for this volume
	volumeLock := freezeOrch.getVolumeLock(volumeHandle)
	volumeLock.Lock()
	defer volumeLock.Unlock()

	// Check if we already have tracking for this snapshot (retry case)
	tracking, exists := freezeOrch.getTracking(snapshotName)
	var targetVA *storagev1.VolumeAttachment
	var err error

	if exists {
		// This is a retry - we already validated it's a filesystem volume
		// Skip PV lookup and get VolumeAttachment directly
		klog.V(4).Infof("CheckOrRequestFreeze: found existing tracking for snapshot %s, using cached VolumeAttachment name", snapshotName)
		targetVA, err = freezeOrch.kubeClient.StorageV1().VolumeAttachments().Get(ctx, tracking.volumeAttachmentName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				// VolumeAttachment not found (404) - volume may have been detached/deleted
				// Remove tracking and allow snapshot to proceed
				freezeOrch.removeTracking(snapshotName, volumeHandle)
				klog.V(4).Infof("CheckOrRequestFreeze: VolumeAttachment %s not found for snapshot %s, removed tracking and skipping freeze", tracking.volumeAttachmentName, snapshotName)
				return "", true, nil
			}
			// Other errors should be returned
			return "", false, fmt.Errorf("failed to get VolumeAttachment: %v", err)
		}
	} else {
		// First call - need to validate PV and find VolumeAttachment
		// Get PV to check if it's a filesystem volume
		pv, err := freezeOrch.getPVFromVolumeHandle(ctx, volumeHandle)
		if err != nil {
			// PV not found - skip freeze gracefully (volume may be deleted/detached)
			klog.V(4).Infof("CheckOrRequestFreeze: failed to get PV for volume %s, skipping freeze: %v", volumeHandle, err)
			return "", true, nil
		}

		// Only freeze filesystem volumes
		if pv.Spec.VolumeMode != nil && *pv.Spec.VolumeMode == corev1.PersistentVolumeBlock {
			klog.V(4).Infof("CheckOrRequestFreeze: volume %s is block mode, skipping freeze", volumeHandle)
			// Check if this volume uses a SKU that should skip freeze
			if freezeOrch.shouldSkipFreezeBySKU(pv) {
				klog.V(4).Infof("CheckOrRequestFreeze: volume %s uses a SKU that should skip freeze, adding to cache", volumeHandle)
				freezeOrch.addToSkipFreezeCache(volumeHandle)
			}
			return "", true, nil
		}

		// Find VolumeAttachment for this volume
		targetVA, err = freezeOrch.isVolumeAttached(ctx, volumeHandle)
		if err != nil {
			return "", false, fmt.Errorf("failed while checking if volume is attached: %v", err)
		}

		if targetVA == nil {
			klog.V(4).Infof("CheckOrRequestFreeze: no attached VolumeAttachment found for volume %s, skipping freeze", volumeHandle)
			return "", true, nil
		}
	}

	// Check if freeze-required annotation already exists
	freezeRequired := ""
	freezeState := ""
	if targetVA.Annotations != nil {
		freezeRequired = targetVA.Annotations[freeze.AnnotationFreezeRequired]
		freezeState = targetVA.Annotations[freeze.AnnotationFreezeState]
	}

	// Case 1: No freeze-required annotation - need to set it
	if freezeRequired == "" {
		freezeTime := time.Now()
		if err := freezeOrch.setFreezeRequiredAnnotation(ctx, targetVA, freezeTime); err != nil {
			if apierrors.IsNotFound(err) {
				// VolumeAttachment not found (404) - volume may have been detached/deleted
				klog.V(4).Infof("CheckOrRequestFreeze: VolumeAttachment not found for volume %s, skipping freeze", volumeHandle)
				return "", true, nil
			}
			return "", false, fmt.Errorf("failed to set freeze-required annotation on VolumeAttachment: %v", err)
		}

		// Set freeze-required annotation on VolumeSnapshot
		if freezeOrch.snapshotClient != nil && snapshotNamespace != "" {
			if err := freezeOrch.setFreezeRequiredAnnotationOnSnapshot(ctx, snapshotName, snapshotNamespace, freezeTime); err != nil {
				// Rollback VolumeAttachment annotation
				freezeOrch.removeFreezeRequiredAnnotation(ctx, targetVA)
				if apierrors.IsNotFound(err) {
					// VolumeAttachment not found (404) - volume may have been detached/deleted
					klog.V(4).Infof("CheckOrRequestFreeze: VolumeSnapshot %s not found for volume %s, skipping freeze", snapshotName, volumeHandle)
					return "", true, nil
				}
				return "", false, fmt.Errorf("failed to set freeze-required annotation on VolumeSnapshot: %v", err)
			}
		}

		// Track this snapshot (within volume lock scope)
		freezeOrch.addTracking(snapshotName, &snapshotTracking{
			volumeHandle:         volumeHandle,
			snapshotName:         snapshotName,
			snapshotNamespace:    snapshotNamespace,
			freezeRequiredTime:   freezeTime,
			volumeAttachmentName: targetVA.Name,
		})

		klog.V(2).Infof("CheckOrRequestFreeze: freeze requested for volume %s snapshot %s, will retry", volumeHandle, snapshotName)
		return "", false, nil // Return false to indicate retry needed
	}

	// Case 2: freeze-required exists but no freeze-state yet - still waiting
	if freezeState == "" {
		// Check timeout
		freezeTime, err := time.Parse(time.RFC3339, freezeRequired)
		if err != nil {
			klog.Warningf("CheckOrRequestFreeze: failed to parse freeze time %s: %v", freezeRequired, err)
			freezeTime = time.Now()
		}

		if freezeOrch.hasTimedOut(freezeTime) {
			klog.Warningf("CheckOrRequestFreeze: freeze timed out for snapshot %s", snapshotName)
			// In strict mode, this is an error; in best-effort, proceed
			if freezeOrch.isSnapshotStrictMode() {
				return freeze.FreezeStateSkipped, false, fmt.Errorf("freeze operation timed out in strict mode")
			}
			return freeze.FreezeStateSkipped, true, nil
		}

		klog.V(4).Infof("CheckOrRequestFreeze: waiting for freeze state for snapshot %s", snapshotName)
		return "", false, nil // Still waiting
	}

	// Case 3: freeze-state exists - check if we should proceed
	klog.V(2).Infof("CheckOrRequestFreeze: freeze state for snapshot %s: %s", snapshotName, freezeState)

	// Copy freeze-state annotation to VolumeSnapshot (but never remove it)
	if freezeOrch.snapshotClient != nil && snapshotNamespace != "" {
		if err := freezeOrch.setFreezeStateAnnotationOnSnapshot(ctx, snapshotName, snapshotNamespace, freezeState); err != nil {
			klog.Warningf("CheckOrRequestFreeze: failed to set freeze-state annotation on VolumeSnapshot: %v", err)
			// Not a fatal error - continue with snapshot
		}
	}

	shouldProceed := freezeOrch.shouldProceedWithState(freezeState)

	if !shouldProceed && freezeOrch.isSnapshotStrictMode() {
		return freezeState, false, fmt.Errorf("freeze failed with state %s in strict mode", freezeState)
	}

	return freezeState, true, nil
}

// hasTimedOut checks if freeze request has timed out
func (freezeOrch *FreezeOrchestrator) hasTimedOut(freezeTime time.Time) bool {
	var timeout time.Duration
	if freezeOrch.isSnapshotStrictMode() {
		if freezeOrch.freezeWaitTimeoutMinutes == 0 {
			// Indefinite wait for strict mode with 0 timeout
			return false
		}
		timeout = time.Duration(freezeOrch.freezeWaitTimeoutMinutes) * time.Minute
	} else {
		// best-effort mode
		waitTime := freezeOrch.freezeWaitTimeoutMinutes
		if waitTime < 2 {
			waitTime = 2
		}
		timeout = time.Duration(waitTime) * time.Minute
	}

	return time.Since(freezeTime) > timeout
}

// ReleaseFreeze removes the freeze-required annotation to trigger unfreeze
func (freezeOrch *FreezeOrchestrator) ReleaseFreeze(ctx context.Context, volumeHandle string, snapshotName string, snapshotNamespace string) error {
	// Acquire volume-level lock at the highest level using volumeHandle
	volumeLock := freezeOrch.getVolumeLock(volumeHandle)
	volumeLock.Lock()
	defer volumeLock.Unlock()

	tracking, exists := freezeOrch.getTracking(snapshotName)
	if !exists {
		klog.V(4).Infof("ReleaseFreeze: no tracking found for snapshot %s", snapshotName)
		return nil
	}

	// Get VolumeAttachment
	va, err := freezeOrch.kubeClient.StorageV1().VolumeAttachments().Get(ctx, tracking.volumeAttachmentName, metav1.GetOptions{})
	if err != nil {
		klog.Warningf("ReleaseFreeze: failed to get VolumeAttachment %s: %v", tracking.volumeAttachmentName, err)
		freezeOrch.removeTracking(snapshotName, volumeHandle)
		return nil // Not a fatal error
	}

	// Check if there are other ongoing snapshots for this volume
	otherSnapshots := freezeOrch.hasOtherSnapshotsForVolume(volumeHandle, snapshotName)
	if otherSnapshots {
		klog.V(2).Infof("ReleaseFreeze: other snapshots exist for volume %s, not removing freeze-required annotation yet", volumeHandle)
		freezeOrch.removeTracking(snapshotName, volumeHandle)
		return nil
	}

	// Remove freeze-required annotation from VolumeAttachment
	if err := freezeOrch.removeFreezeRequiredAnnotation(ctx, va); err != nil {
		klog.Errorf("ReleaseFreeze: failed to remove freeze-required annotation from VolumeAttachment: %v", err)
		freezeOrch.removeTracking(snapshotName, volumeHandle)
		return err
	}

	freezeOrch.removeTracking(snapshotName, volumeHandle)

	klog.V(2).Infof("ReleaseFreeze: released freeze for snapshot %s on VA %s", snapshotName, tracking.volumeAttachmentName)
	return nil
}

// setFreezeRequiredAnnotation sets the freeze-required annotation on VolumeAttachment
func (freezeOrch *FreezeOrchestrator) setFreezeRequiredAnnotation(ctx context.Context, va *storagev1.VolumeAttachment, freezeTime time.Time) error {
	vaCopy := va.DeepCopy()
	if vaCopy.Annotations == nil {
		vaCopy.Annotations = make(map[string]string)
	}
	vaCopy.Annotations[freeze.AnnotationFreezeRequired] = freezeTime.Format(time.RFC3339)

	_, err := freezeOrch.kubeClient.StorageV1().VolumeAttachments().Update(ctx, vaCopy, metav1.UpdateOptions{})
	return err
}

// removeFreezeRequiredAnnotation removes the freeze-required annotation from VolumeAttachment
func (freezeOrch *FreezeOrchestrator) removeFreezeRequiredAnnotation(ctx context.Context, va *storagev1.VolumeAttachment) error {
	vaCopy := va.DeepCopy()
	if vaCopy.Annotations == nil {
		return nil
	}
	delete(vaCopy.Annotations, freeze.AnnotationFreezeRequired)

	_, err := freezeOrch.kubeClient.StorageV1().VolumeAttachments().Update(ctx, vaCopy, metav1.UpdateOptions{})
	if err != nil && apierrors.IsNotFound(err) {
		// VolumeAttachment was deleted - not an error for cleanup
		return nil
	}
	return err
}

// setFreezeRequiredAnnotationOnSnapshot sets the freeze-required annotation on VolumeSnapshot
func (freezeOrch *FreezeOrchestrator) setFreezeRequiredAnnotationOnSnapshot(ctx context.Context, snapshotName string, namespace string, freezeTime time.Time) error {
	if freezeOrch.snapshotClient == nil {
		return fmt.Errorf("snapshot client not available")
	}

	snapshot, err := freezeOrch.snapshotClient.SnapshotV1().VolumeSnapshots(namespace).Get(ctx, snapshotName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get VolumeSnapshot: %v", err)
	}

	snapshotCopy := snapshot.DeepCopy()
	if snapshotCopy.Annotations == nil {
		snapshotCopy.Annotations = make(map[string]string)
	}
	snapshotCopy.Annotations[freeze.AnnotationFreezeRequired] = freezeTime.Format(time.RFC3339)

	_, err = freezeOrch.snapshotClient.SnapshotV1().VolumeSnapshots(namespace).Update(ctx, snapshotCopy, metav1.UpdateOptions{})
	return err
}

// setFreezeStateAnnotationOnSnapshot sets the freeze-state annotation on VolumeSnapshot
// Note: This annotation is never removed from VolumeSnapshot (unlike VolumeAttachment)
func (freezeOrch *FreezeOrchestrator) setFreezeStateAnnotationOnSnapshot(ctx context.Context, snapshotName string, namespace string, freezeState string) error {
	if freezeOrch.snapshotClient == nil {
		return fmt.Errorf("snapshot client not available")
	}

	snapshot, err := freezeOrch.snapshotClient.SnapshotV1().VolumeSnapshots(namespace).Get(ctx, snapshotName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get VolumeSnapshot: %v", err)
	}

	snapshotCopy := snapshot.DeepCopy()
	if snapshotCopy.Annotations == nil {
		snapshotCopy.Annotations = make(map[string]string)
	}
	snapshotCopy.Annotations[freeze.AnnotationFreezeState] = freezeState

	_, err = freezeOrch.snapshotClient.SnapshotV1().VolumeSnapshots(namespace).Update(ctx, snapshotCopy, metav1.UpdateOptions{})
	if err != nil && apierrors.IsNotFound(err) {
		return fmt.Errorf("VolumeSnapshot was deleted: %v", err)
	}
	return err
}

// getPVFromVolumeHandle gets the PV for a given volume handle
// Uses pagination to handle large clusters efficiently
func (freezeOrch *FreezeOrchestrator) getPVFromVolumeHandle(ctx context.Context, volumeHandle string) (*corev1.PersistentVolume, error) {
	// Note: Kubernetes PV API doesn't support field selectors for spec.csi.volumeHandle
	// so we need to list and filter. Using pagination for efficiency.
	var foundPV *corev1.PersistentVolume

	pvPager := pager.New(func(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
		return freezeOrch.kubeClient.CoreV1().PersistentVolumes().List(ctx, opts)
	})

	err := pvPager.EachListItem(ctx, metav1.ListOptions{}, func(obj runtime.Object) error {
		pv, ok := obj.(*corev1.PersistentVolume)
		if !ok {
			return nil
		}

		if pv.Spec.CSI != nil && pv.Spec.CSI.VolumeHandle == volumeHandle {
			foundPV = pv
			return fmt.Errorf("found") // Stop iteration
		}
		return nil
	})

	if foundPV != nil {
		return foundPV, nil
	}

	if err != nil && err.Error() != "found" {
		return nil, err
	}

	return nil, fmt.Errorf("PV not found for volume handle %s", volumeHandle)
}

// shouldProceedWithState determines if snapshot should proceed based on freeze state
func (freezeOrch *FreezeOrchestrator) shouldProceedWithState(freezeState string) bool {
	switch freezeState {
	case freeze.FreezeStateFrozen:
		return true
	case freeze.FreezeStateSkipped:
		// Skipped - proceed in best-effort, fail in strict
		return freezeOrch.snapshotConsistencyMode == "best-effort"
	case freeze.FreezeStateUserFrozen:
		// User frozen - proceed (assuming user knows what they're doing)
		return true
	case freeze.FreezeStateFailed:
		// Failed - proceed in best-effort, block in strict
		return freezeOrch.snapshotConsistencyMode == "best-effort"
	default:
		return freezeOrch.snapshotConsistencyMode == "best-effort"
	}
}

// getVolumeLock gets or creates a mutex for a specific volume
func (freezeOrch *FreezeOrchestrator) getVolumeLock(volumeHandle string) *sync.Mutex {
	freezeOrch.volumeLocksMu.Lock()
	defer freezeOrch.volumeLocksMu.Unlock()

	if lock, exists := freezeOrch.volumeLocks[volumeHandle]; exists {
		return lock
	}

	lock := &sync.Mutex{}
	freezeOrch.volumeLocks[volumeHandle] = lock
	return lock
}

// addVolumeLock creates a mutex for a specific volume
func (freezeOrch *FreezeOrchestrator) addVolumeLock(volumeHandle string) {
	freezeOrch.volumeLocksMu.Lock()
	defer freezeOrch.volumeLocksMu.Unlock()

	if _, exists := freezeOrch.volumeLocks[volumeHandle]; !exists {
		freezeOrch.volumeLocks[volumeHandle] = &sync.Mutex{}
	}
}

// releaseVolumeLock removes the mutex for a volume if no snapshots are tracked
func (freezeOrch *FreezeOrchestrator) releaseVolumeLock(volumeHandle string) {
	freezeOrch.volumeLocksMu.Lock()
	defer freezeOrch.volumeLocksMu.Unlock()

	// Check if any snapshots are still using this volume
	freezeOrch.mu.RLock()
	hasSnapshots := false
	for _, tracking := range freezeOrch.ongoingSnapshots {
		if tracking.volumeHandle == volumeHandle {
			hasSnapshots = true
			break
		}
	}
	freezeOrch.mu.RUnlock()

	// Remove lock if no snapshots reference this volume
	if !hasSnapshots {
		delete(freezeOrch.volumeLocks, volumeHandle)
	}
}

// hasOtherSnapshotsForVolume checks if there are other ongoing snapshots for the same volume
// Caller must hold the volume lock for volumeHandle
func (freezeOrch *FreezeOrchestrator) hasOtherSnapshotsForVolume(volumeHandle string, excludeSnapshot string) bool {
	// Check cache first
	freezeOrch.mu.RLock()
	defer freezeOrch.mu.RUnlock()

	hasOther := false
	for name, tracking := range freezeOrch.ongoingSnapshots {
		if name != excludeSnapshot && tracking.volumeHandle == volumeHandle {
			hasOther = true
			break
		}
	}

	return hasOther
}

// addTracking adds snapshot tracking
// Caller must hold the volume lock for tracking.volumeHandle
func (freezeOrch *FreezeOrchestrator) addTracking(snapshotName string, tracking *snapshotTracking) {
	freezeOrch.mu.Lock()
	defer freezeOrch.mu.Unlock()

	// to ensure if another thread has removed the lock while this one was already waiting on the same
	// lock reference
	freezeOrch.addVolumeLock(tracking.volumeHandle)

	freezeOrch.ongoingSnapshots[snapshotName] = tracking
}

// getTracking gets snapshot tracking
func (freezeOrch *FreezeOrchestrator) getTracking(snapshotName string) (*snapshotTracking, bool) {
	freezeOrch.mu.RLock()
	defer freezeOrch.mu.RUnlock()
	tracking, exists := freezeOrch.ongoingSnapshots[snapshotName]
	return tracking, exists
}

// removeTracking removes snapshot tracking
// Caller must hold the volume lock for volumeHandle
func (freezeOrch *FreezeOrchestrator) removeTracking(snapshotName string, volumeHandle string) {
	freezeOrch.mu.Lock()
	delete(freezeOrch.ongoingSnapshots, snapshotName)
	freezeOrch.mu.Unlock()

	// Release volume lock after removing tracking
	freezeOrch.releaseVolumeLock(volumeHandle)
}

// addToSkipFreezeCache adds a volume to the skip freeze LRU cache
func (freezeOrch *FreezeOrchestrator) addToSkipFreezeCache(volumeHandle string) {
	freezeOrch.skipFreezeMu.Lock()
	defer freezeOrch.skipFreezeMu.Unlock()

	freezeOrch.skipFreezeCache.Add(volumeHandle, nil)
	klog.V(6).Infof("Added volume %s to skip freeze LRU cache", volumeHandle)
}

// shouldSkipFreezeFromCache checks if a volume is in the skip freeze LRU cache
func (freezeOrch *FreezeOrchestrator) shouldSkipFreezeFromCache(volumeHandle string) bool {
	freezeOrch.skipFreezeMu.Lock()
	defer freezeOrch.skipFreezeMu.Unlock()

	_, exists := freezeOrch.skipFreezeCache.Get(volumeHandle)
	return exists
}

// shouldSkipFreezeBySKU checks if a volume's SKU should skip freeze
func (freezeOrch *FreezeOrchestrator) shouldSkipFreezeBySKU(pv *corev1.PersistentVolume) bool {
	if pv.Spec.CSI == nil || pv.Spec.CSI.VolumeAttributes == nil {
		return false
	}

	// Check both storageAccountType and skuName parameters (case-insensitive)
	if sku, exists := azureutils.ParseDiskParametersForKey(pv.Spec.CSI.VolumeAttributes, azureconstants.StorageAccountTypeField); exists {
		for _, skipSKU := range skusToSkipFreeze {
			if strings.EqualFold(sku, skipSKU) {
				return true
			}
		}
	}
	if sku, exists := azureutils.ParseDiskParametersForKey(pv.Spec.CSI.VolumeAttributes, azureconstants.SkuNameField); exists {
		for _, skipSKU := range skusToSkipFreeze {
			if strings.EqualFold(sku, skipSKU) {
				return true
			}
		}
	}
	return false
}

// shouldEnableFreeze checks if freeze should be enabled for this snapshot based on driver configuration
func (d *Driver) shouldEnableFreeze() bool {
	return d.enableSnapshotConsistency && d.cloud != nil && d.cloud.KubeClient != nil
}

// GetFreezeOrchestrator returns the freeze orchestrator (cached instance)
func (d *Driver) getFreezeOrchestrator() *FreezeOrchestrator {
	return d.freezeOrchestrator
}

// getFreezeStateDescription returns a human-readable description for logging
func getFreezeStateDescription(state string) string {
	switch state {
	case freeze.FreezeStateFrozen:
		return "filesystem successfully frozen"
	case freeze.FreezeStateSkipped:
		return "freeze skipped (not applicable)"
	case freeze.FreezeStateUserFrozen:
		return "filesystem already frozen (possibly by user)"
	case freeze.FreezeStateFailed:
		return "freeze operation failed"
	case freeze.FreezeStateUnfrozen:
		return "filesystem unfrozen"
	default:
		return fmt.Sprintf("unknown state: %s", state)
	}
}

// isVolumeAttached checks if any VolumeAttachment exists and is attached for the given volume
// Uses the VolumeAttachment tracker for efficient lookup
// Returns error if tracker is not initialized to trigger retry
// Returns nil (no VA found) if tracker initialization failed, allowing fallback without consistency
func (freezeOrch *FreezeOrchestrator) isVolumeAttached(ctx context.Context, volumeHandle string) (*storagev1.VolumeAttachment, error) {
	// Check if tracker initialization failed - if so, proceed without freeze
	if freezeOrch.vaTrackerInitFailed.Load() {
		klog.V(4).Infof("VolumeAttachment tracker initialization failed, skipping freeze for volume %s", volumeHandle)
		return nil, nil
	}

	// Check if tracker is initialized
	if !freezeOrch.isVolumeAttachmentTrackerInitialized() {
		return nil, fmt.Errorf("VolumeAttachment tracker not initialized yet, will retry")
	}

	// Use tracker for fast lookup
	va, exists := freezeOrch.getVolumeAttachmentFromTracker(volumeHandle)
	if !exists {
		return nil, nil
	}
	// Return the VA only if it's attached
	if va.Status.Attached {
		return va, nil
	}
	return nil, nil
}

// logFreezeEvent creates a Kubernetes event for freeze-related issues
func (d *Driver) logFreezeEvent(volumeHandle string, snapshotName string, message string, isWarning bool) {
	if d.eventRecorder == nil {
		klog.Warningf("Event recorder not available, logging event instead: %s", message)
		if isWarning {
			klog.Warningf("Freeze event for snapshot %s (volume %s): %s", snapshotName, volumeHandle, message)
		} else {
			klog.V(2).Infof("Freeze event for snapshot %s (volume %s): %s", snapshotName, volumeHandle, message)
		}
		return
	}

	orchestrator := d.getFreezeOrchestrator()
	if orchestrator == nil || orchestrator.snapshotClient == nil {
		klog.Warningf("Snapshot client not available, logging event instead: %s", message)
		if isWarning {
			klog.Warningf("Freeze event for snapshot %s (volume %s): %s", snapshotName, volumeHandle, message)
		}
		return
	}

	// Get the snapshot tracking to find namespace
	tracking, exists := orchestrator.getTracking(snapshotName)
	if !exists {
		klog.Warningf("Snapshot tracking not found for %s, cannot create event", snapshotName)
		return
	}

	// Get VolumeSnapshot object to record event against
	snapshot, err := orchestrator.snapshotClient.SnapshotV1().VolumeSnapshots(tracking.snapshotNamespace).Get(context.Background(), snapshotName, metav1.GetOptions{})
	if err != nil {
		klog.Warningf("Failed to get VolumeSnapshot %s for event recording: %v", snapshotName, err)
		return
	}

	// Create event
	eventType := corev1.EventTypeNormal
	reason := "SnapshotFreezeInfo"
	if isWarning {
		eventType = corev1.EventTypeWarning
		reason = "SnapshotFreezeWarning"
	}

	d.eventRecorder.Event(snapshot, eventType, reason, message)
}

// BootstrapVolumeAttachmentTracking initializes the VolumeAttachment tracker by listing all existing VolumeAttachments
// This should be called during driver initialization, similar to snapshot tracking bootstrap
// Uses pagination to handle large clusters efficiently
func (freezeOrch *FreezeOrchestrator) BootstrapVolumeAttachmentTracking(ctx context.Context) error {
	if freezeOrch.vaTrackerInitialized.Load() {
		klog.V(4).Infof("VolumeAttachment tracker already initialized, skipping bootstrap")
		return nil
	}

	klog.V(2).Infof("Bootstrapping VolumeAttachment tracking...")

	count := 0

	// Use pager to efficiently list all VolumeAttachments
	vaPager := pager.New(func(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
		return freezeOrch.kubeClient.StorageV1().VolumeAttachments().List(ctx, opts)
	})

	freezeOrch.volumeAttachmentsMu.Lock()
	defer freezeOrch.volumeAttachmentsMu.Unlock()

	err := vaPager.EachListItem(ctx, metav1.ListOptions{}, func(obj runtime.Object) error {
		va, ok := obj.(*storagev1.VolumeAttachment)
		if !ok {
			return nil
		}

		if va.Spec.Source.PersistentVolumeName == nil {
			return nil
		}

		// Get PV to extract volumeHandle
		pvName := *va.Spec.Source.PersistentVolumeName
		pv, err := freezeOrch.kubeClient.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				klog.V(4).Infof("PV %s not found for VolumeAttachment %s, skipping", pvName, va.Name)
				return nil
			}
			klog.Warningf("Failed to get PV %s for VolumeAttachment %s: %v", pvName, va.Name, err)
			return nil
		}

		if pv.Spec.CSI == nil || pv.Spec.CSI.VolumeHandle == "" {
			return nil
		}

		volumeHandle := pv.Spec.CSI.VolumeHandle
		freezeOrch.volumeAttachments[volumeHandle] = va.DeepCopy()
		count++
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to list VolumeAttachments during bootstrap: %v", err)
	}

	freezeOrch.vaTrackerInitialized.Store(true)
	klog.V(2).Infof("VolumeAttachment tracking bootstrap complete: tracked %d VolumeAttachments", count)
	return nil
}

// StartVolumeAttachmentInformer starts an informer to watch VolumeAttachment resources
// This should be called after BootstrapVolumeAttachmentTracking
func (freezeOrch *FreezeOrchestrator) StartVolumeAttachmentInformer(ctx context.Context) error {
	if !freezeOrch.vaTrackerInitialized.Load() {
		return fmt.Errorf("VolumeAttachment tracker not initialized, call BootstrapVolumeAttachmentTracking first")
	}

	klog.V(2).Infof("Starting VolumeAttachment informer...")

	// Create informer factory
	freezeOrch.informerFactory = informers.NewSharedInformerFactory(freezeOrch.kubeClient, 0)
	vaInformer := freezeOrch.informerFactory.Storage().V1().VolumeAttachments().Informer()

	// Add event handlers
	_, err := vaInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			va, ok := obj.(*storagev1.VolumeAttachment)
			if !ok {
				return
			}
			freezeOrch.handleVolumeAttachmentAdd(ctx, va)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			va, ok := newObj.(*storagev1.VolumeAttachment)
			if !ok {
				return
			}
			freezeOrch.handleVolumeAttachmentUpdate(ctx, va)
		},
		DeleteFunc: func(obj interface{}) {
			va, ok := obj.(*storagev1.VolumeAttachment)
			if !ok {
				return
			}
			freezeOrch.handleVolumeAttachmentDelete(ctx, va)
		},
	})

	if err != nil {
		return fmt.Errorf("failed to add event handler to VolumeAttachment informer: %w", err)
	}

	// Start informer
	freezeOrch.informerStopChMu.Lock()
	freezeOrch.informerStopCh = make(chan struct{})
	stopCh := freezeOrch.informerStopCh
	freezeOrch.informerStopChMu.Unlock()

	go freezeOrch.informerFactory.Start(stopCh)

	// Wait for cache sync (this function is already called in a goroutine)
	klog.V(2).Infof("Waiting for VolumeAttachment informer cache to sync...")
	if !cache.WaitForCacheSync(stopCh, vaInformer.HasSynced) {
		// Only close the channel if it hasn't been closed by StopVolumeAttachmentInformer
		freezeOrch.informerStopChMu.Lock()
		if freezeOrch.informerStopCh != nil && freezeOrch.informerStopCh == stopCh {
			close(freezeOrch.informerStopCh)
			freezeOrch.informerStopCh = nil
		}
		freezeOrch.informerStopChMu.Unlock()
		return fmt.Errorf("failed to sync VolumeAttachment informer cache")
	}

	klog.V(2).Infof("VolumeAttachment informer started successfully")
	return nil
}

// StopVolumeAttachmentInformer stops the VolumeAttachment informer
func (freezeOrch *FreezeOrchestrator) StopVolumeAttachmentInformer() {
	freezeOrch.informerStopChMu.Lock()
	defer freezeOrch.informerStopChMu.Unlock()

	if freezeOrch.informerStopCh != nil {
		close(freezeOrch.informerStopCh)
		freezeOrch.informerStopCh = nil
		klog.V(2).Infof("VolumeAttachment informer stopped")
	}
}

// handleVolumeAttachmentAdd handles VolumeAttachment creation events
func (freezeOrch *FreezeOrchestrator) handleVolumeAttachmentAdd(ctx context.Context, va *storagev1.VolumeAttachment) {
	if va.Spec.Source.PersistentVolumeName == nil {
		return
	}

	// Get PV to extract volumeHandle
	pvName := *va.Spec.Source.PersistentVolumeName
	pv, err := freezeOrch.kubeClient.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			klog.Warningf("Failed to get PV %s for VolumeAttachment %s: %v", pvName, va.Name, err)
		}
		return
	}

	if pv.Spec.CSI == nil || pv.Spec.CSI.VolumeHandle == "" {
		return
	}

	volumeHandle := pv.Spec.CSI.VolumeHandle

	freezeOrch.volumeAttachmentsMu.Lock()
	defer freezeOrch.volumeAttachmentsMu.Unlock()

	freezeOrch.volumeAttachments[volumeHandle] = va.DeepCopy()
	klog.V(4).Infof("VolumeAttachment %s added to tracker for volume %s", va.Name, volumeHandle)
}

// handleVolumeAttachmentUpdate handles VolumeAttachment update events
func (freezeOrch *FreezeOrchestrator) handleVolumeAttachmentUpdate(ctx context.Context, va *storagev1.VolumeAttachment) {
	// For updates, treat the same as add - just update the tracker
	freezeOrch.handleVolumeAttachmentAdd(ctx, va)
}

// handleVolumeAttachmentDelete handles VolumeAttachment deletion events
func (freezeOrch *FreezeOrchestrator) handleVolumeAttachmentDelete(ctx context.Context, va *storagev1.VolumeAttachment) {
	if va.Spec.Source.PersistentVolumeName == nil {
		return
	}

	// Get PV to extract volumeHandle
	pvName := *va.Spec.Source.PersistentVolumeName
	pv, err := freezeOrch.kubeClient.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
	if err != nil {
		// PV might be deleted as well, try to find volumeHandle from existing tracker
		freezeOrch.volumeAttachmentsMu.Lock()
		defer freezeOrch.volumeAttachmentsMu.Unlock()

		// Search for the VA in our tracker and remove it
		for volumeHandle, trackedVA := range freezeOrch.volumeAttachments {
			if trackedVA.Name == va.Name {
				delete(freezeOrch.volumeAttachments, volumeHandle)
				klog.V(4).Infof("VolumeAttachment %s removed from tracker for volume %s", va.Name, volumeHandle)
				return
			}
		}
		return
	}

	if pv.Spec.CSI == nil || pv.Spec.CSI.VolumeHandle == "" {
		return
	}

	volumeHandle := pv.Spec.CSI.VolumeHandle

	freezeOrch.volumeAttachmentsMu.Lock()
	defer freezeOrch.volumeAttachmentsMu.Unlock()

	delete(freezeOrch.volumeAttachments, volumeHandle)
	klog.V(4).Infof("VolumeAttachment %s removed from tracker for volume %s", va.Name, volumeHandle)
}

// getVolumeAttachmentFromTracker retrieves a VolumeAttachment from the tracker by volumeHandle
func (freezeOrch *FreezeOrchestrator) getVolumeAttachmentFromTracker(volumeHandle string) (*storagev1.VolumeAttachment, bool) {
	freezeOrch.volumeAttachmentsMu.RLock()
	defer freezeOrch.volumeAttachmentsMu.RUnlock()

	va, exists := freezeOrch.volumeAttachments[volumeHandle]
	if !exists {
		return nil, false
	}
	return va.DeepCopy(), true
}

// isVolumeAttachmentTrackerInitialized returns whether the VolumeAttachment tracker has been initialized
func (freezeOrch *FreezeOrchestrator) isVolumeAttachmentTrackerInitialized() bool {
	return freezeOrch.vaTrackerInitialized.Load()
}
