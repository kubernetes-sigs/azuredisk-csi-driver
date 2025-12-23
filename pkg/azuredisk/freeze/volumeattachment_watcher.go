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
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	watchtools "k8s.io/client-go/tools/watch"
	"k8s.io/klog/v2"
	"k8s.io/mount-utils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

const (
	// Annotation keys
	AnnotationFreezeRequired = "disk.csi.azure.com/freeze-required"
	AnnotationFreezeState    = "disk.csi.azure.com/freeze-state"

	// Event reasons
	EventReasonFreezeSuccess        = "FreezeSuccess"
	EventReasonFreezeFailed         = "FreezeFailed"
	EventReasonFreezeSkipped        = "FreezeSkipped"
	EventReasonUnfreezeSuccess      = "UnfreezeSuccess"
	EventReasonUnfreezeFailed       = "UnfreezeFailed"
	EventReasonInconsistentSnapshot = "InconsistentSnapshot"
	EventReasonFreezeTimeout        = "FreezeTimeout"
	EventReasonUserFrozenDetected   = "UserFrozenDetected"

	// Reconcile interval
	ReconcileInterval = 1 * time.Minute
)

// VolumeAttachmentWatcher watches VolumeAttachment resources and triggers freeze/unfreeze operations
type VolumeAttachmentWatcher struct {
	nodeName      string
	kubeClient    kubernetes.Interface
	freezeManager *FreezeManager
	eventRecorder record.EventRecorder
	mounter       *mount.SafeFormatAndMount
	stopCh        chan struct{}
	wg            sync.WaitGroup
	timeout       time.Duration

	// Track volumes currently being processed
	processing     map[string]bool
	processingLock sync.RWMutex
}

// NewVolumeAttachmentWatcher creates a new VolumeAttachmentWatcher
func NewVolumeAttachmentWatcher(
	nodeName string,
	kubeClient kubernetes.Interface,
	freezeManager *FreezeManager,
	eventRecorder record.EventRecorder,
	mounter *mount.SafeFormatAndMount,
	timeoutMinutes int,
) *VolumeAttachmentWatcher {
	if timeoutMinutes <= 0 {
		timeoutMinutes = azureconstants.DefaultFreezeWaitTimeoutMinutes
	}

	timeoutMinutes = max(timeoutMinutes+5, timeoutMinutes*2) // add buffer to cover CSI retry backoff

	return &VolumeAttachmentWatcher{
		nodeName:      nodeName,
		kubeClient:    kubeClient,
		freezeManager: freezeManager,
		eventRecorder: eventRecorder,
		mounter:       mounter,
		stopCh:        make(chan struct{}),
		timeout:       time.Duration(timeoutMinutes) * time.Minute,
		processing:    make(map[string]bool),
	}
}

// Start starts the watcher goroutine
func (w *VolumeAttachmentWatcher) Start(ctx context.Context) {
	w.wg.Add(1)
	go w.run(ctx)

	// Start reconciler
	w.wg.Add(1)
	go w.reconcileLoop(ctx)
}

// Stop stops the watcher
func (w *VolumeAttachmentWatcher) Stop() {
	close(w.stopCh)
	w.wg.Wait()
}

// run is the main watch loop
func (w *VolumeAttachmentWatcher) run(ctx context.Context) {
	defer w.wg.Done()

	klog.V(2).Infof("Starting VolumeAttachment watcher for node %s", w.nodeName)

	for {
		select {
		case <-ctx.Done():
			klog.V(2).Infof("VolumeAttachment watcher stopped due to context cancellation")
			return
		case <-w.stopCh:
			klog.V(2).Infof("VolumeAttachment watcher stopped")
			return
		default:
			if err := w.watchVolumeAttachments(ctx); err != nil {
				klog.Errorf("VolumeAttachment watch error: %v, restarting in 10s", err)
				time.Sleep(10 * time.Second)
			}
		}
	}
}

// watchVolumeAttachments watches VolumeAttachment resources for this node
func (w *VolumeAttachmentWatcher) watchVolumeAttachments(ctx context.Context) error {
	// Watch VolumeAttachments for this node only
	fieldSelector := fields.OneTermEqualSelector("spec.nodeName", w.nodeName).String()

	lw := &cache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			options.FieldSelector = fieldSelector
			return w.kubeClient.StorageV1().VolumeAttachments().List(ctx, options)
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			options.FieldSelector = fieldSelector
			return w.kubeClient.StorageV1().VolumeAttachments().Watch(ctx, options)
		},
	}

	_, err := watchtools.UntilWithSync(ctx, lw, &storagev1.VolumeAttachment{}, nil, func(event watch.Event) (bool, error) {
		switch event.Type {
		case watch.Added, watch.Modified:
			va, ok := event.Object.(*storagev1.VolumeAttachment)
			if !ok {
				klog.Errorf("Unexpected object type: %T", event.Object)
				return false, nil
			}
			w.handleVolumeAttachment(ctx, va)
		case watch.Deleted:
			// Handle cleanup if needed
			va, ok := event.Object.(*storagev1.VolumeAttachment)
			if !ok {
				klog.Errorf("Unexpected object type: %T", event.Object)
				return false, nil
			}
			w.handleVolumeAttachmentDeletion(ctx, va)
		}
		return false, nil
	})

	return err
}

// handleVolumeAttachment processes a VolumeAttachment update
func (w *VolumeAttachmentWatcher) handleVolumeAttachment(ctx context.Context, va *storagev1.VolumeAttachment) {
	if va == nil || va.Annotations == nil {
		return
	}

	vaName := va.Name
	freezeRequired, hasFreezeRequired := va.Annotations[AnnotationFreezeRequired]
	freezeState, hasFreezeState := va.Annotations[AnnotationFreezeState]

	klog.V(6).Infof("Handling VolumeAttachment %s: freezeRequired=%s, freezeState=%s", vaName, freezeRequired, freezeState)

	// Case 1: freeze-required is set but freeze-state is not set -> perform freeze
	if hasFreezeRequired && freezeRequired != "" && (!hasFreezeState || freezeState == "") {
		w.processFreeze(ctx, va, freezeRequired)
		return
	}

	// Case 2: freeze-required is not set but freeze-state is frozen -> perform unfreeze
	if (!hasFreezeRequired || freezeRequired == "") && freezeState == FreezeStateFrozen {
		w.processUnfreeze(ctx, va)
		return
	}

	// Case 3: Both not set but freeze-state exists -> cleanup
	if (!hasFreezeRequired || freezeRequired == "") && hasFreezeState && freezeState != "" {
		w.cleanupFreezeState(ctx, va)
		return
	}
}

// handleVolumeAttachmentDeletion handles VolumeAttachment deletion
func (w *VolumeAttachmentWatcher) handleVolumeAttachmentDeletion(ctx context.Context, va *storagev1.VolumeAttachment) {
	volumeHandle := va.Spec.Source.PersistentVolumeName
	if volumeHandle == nil || *volumeHandle == "" {
		return
	}

	// Extract volume UUID and cleanup state
	volumeUUID := extractVolumeUUID(*volumeHandle)
	if volumeUUID != "" {
		w.freezeManager.CleanupState(volumeUUID)
		klog.V(2).Infof("Cleaned up freeze state for deleted VolumeAttachment %s", va.Name)
	}
}

// processFreeze performs the freeze operation
func (w *VolumeAttachmentWatcher) processFreeze(ctx context.Context, va *storagev1.VolumeAttachment, freezeRequiredTimestamp string) {
	vaName := va.Name

	// Check if already processing
	if !w.markProcessing(vaName, true) {
		klog.V(6).Infof("Already processing freeze for VolumeAttachment %s", vaName)
		return
	}
	defer w.markProcessing(vaName, false)

	// Check timeout
	if w.isTimedOut(freezeRequiredTimestamp) {
		klog.Warningf("Freeze request for VolumeAttachment %s has timed out", vaName)
		w.updateFreezeState(ctx, va, FreezeStateSkipped)
		return
	}

	// Get mount path and volume UUID
	mountPath, volumeUUID, err := w.getMountInfo(va)
	if err != nil {
		klog.Errorf("Failed to get mount info for VolumeAttachment %s: %v", vaName, err)
		w.updateFreezeState(ctx, va, FreezeStateSkipped)
		return
	}

	if mountPath == "" {
		klog.V(2).Infof("No mount path found for VolumeAttachment %s, skipping freeze", vaName)
		w.updateFreezeState(ctx, va, FreezeStateSkipped)
		return
	}

	// Perform freeze
	klog.V(2).Infof("Freezing filesystem at %s for VolumeAttachment %s", mountPath, vaName)
	state, err := w.freezeManager.Freeze(ctx, mountPath, volumeUUID)

	// Update annotation with result
	w.updateFreezeState(ctx, va, state)

	// Send appropriate event
	if err != nil {
		eventType := corev1.EventTypeWarning
		reason := EventReasonFreezeFailed
		message := fmt.Sprintf("Failed to freeze filesystem at %s: %v", mountPath, err)

		if state == FreezeStateSkipped {
			reason = EventReasonFreezeSkipped
			message = fmt.Sprintf("Freeze skipped for filesystem at %s: %v", mountPath, err)
			eventType = corev1.EventTypeNormal
		} else if state == FreezeStateUserFrozen {
			reason = EventReasonUserFrozenDetected
			message = fmt.Sprintf("Filesystem at %s already frozen (possibly by user)", mountPath)
			eventType = corev1.EventTypeWarning
		}
		// Lets remove if this is redundant to the event that controller generates on volume snapshot
		w.sendEvent(va, eventType, reason, message)
	}
}

// processUnfreeze performs the unfreeze operation
func (w *VolumeAttachmentWatcher) processUnfreeze(ctx context.Context, va *storagev1.VolumeAttachment) {
	vaName := va.Name

	// Check if already processing
	if !w.markProcessing(vaName, true) {
		klog.V(6).Infof("Already processing unfreeze for VolumeAttachment %s", vaName)
		return
	}
	defer w.markProcessing(vaName, false)

	// Get mount path and volume UUID
	mountPath, volumeUUID, err := w.getMountInfo(va)
	if err != nil {
		klog.Errorf("Failed to get mount info for VolumeAttachment %s: %v", vaName, err)
		w.cleanupFreezeState(ctx, va)
		return
	}

	if mountPath == "" {
		klog.V(2).Infof("No mount path found for VolumeAttachment %s, cleaning up freeze state", vaName)
		w.cleanupFreezeState(ctx, va)
		return
	}

	// Perform unfreeze
	klog.V(2).Infof("Unfreezing filesystem at %s for VolumeAttachment %s", mountPath, vaName)
	err = w.freezeManager.Unfreeze(ctx, mountPath, volumeUUID)

	if err != nil {
		// Critical event as unfreeze failure wont be visible from controller
		w.sendEvent(va, corev1.EventTypeWarning, EventReasonUnfreezeFailed,
			fmt.Sprintf("Failed to unfreeze filesystem at %s: %v", mountPath, err))
		// Don't remove annotation on failure - needs manual intervention
		return
	}

	// Remove freeze-state annotation after successful unfreeze
	w.cleanupFreezeState(ctx, va)
}

// cleanupFreezeState removes the freeze-state annotation
func (w *VolumeAttachmentWatcher) cleanupFreezeState(ctx context.Context, va *storagev1.VolumeAttachment) {
	if va.Annotations == nil || va.Annotations[AnnotationFreezeState] == "" {
		return
	}

	// Create a copy and remove the annotation
	vaCopy := va.DeepCopy()
	delete(vaCopy.Annotations, AnnotationFreezeState)

	_, err := w.kubeClient.StorageV1().VolumeAttachments().Update(ctx, vaCopy, metav1.UpdateOptions{})
	if err != nil {
		klog.Errorf("Failed to remove freeze-state annotation from VolumeAttachment %s: %v", va.Name, err)
		return
	}

	klog.V(2).Infof("Removed freeze-state annotation from VolumeAttachment %s", va.Name)
}

// updateFreezeState updates the freeze-state annotation
func (w *VolumeAttachmentWatcher) updateFreezeState(ctx context.Context, va *storagev1.VolumeAttachment, state string) {
	vaCopy := va.DeepCopy()
	if vaCopy.Annotations == nil {
		vaCopy.Annotations = make(map[string]string)
	}
	vaCopy.Annotations[AnnotationFreezeState] = state

	_, err := w.kubeClient.StorageV1().VolumeAttachments().Update(ctx, vaCopy, metav1.UpdateOptions{})
	if err != nil {
		klog.Errorf("Failed to update freeze-state annotation on VolumeAttachment %s: %v", va.Name, err)
		return
	}

	klog.V(2).Infof("Updated freeze-state annotation on VolumeAttachment %s to %s", va.Name, state)
}

// getMountInfo returns the mount path and volume UUID for a VolumeAttachment
func (w *VolumeAttachmentWatcher) getMountInfo(va *storagev1.VolumeAttachment) (string, string, error) {
	volumeHandle := va.Spec.Source.PersistentVolumeName
	if volumeHandle == nil || *volumeHandle == "" {
		return "", "", fmt.Errorf("volume handle not found")
	}

	// Get PV to find volume information
	pv, err := w.kubeClient.CoreV1().PersistentVolumes().Get(context.Background(), *volumeHandle, metav1.GetOptions{})
	if err != nil {
		return "", "", fmt.Errorf("failed to get PV: %v", err)
	}

	// Check if it's a filesystem volume (not block)
	if pv.Spec.VolumeMode != nil && *pv.Spec.VolumeMode == corev1.PersistentVolumeBlock {
		return "", "", fmt.Errorf("block volume mode not supported for freeze")
	}

	// Extract volume UUID
	volumeUUID := extractVolumeUUID(*volumeHandle)
	if volumeUUID == "" {
		return "", "", fmt.Errorf("failed to extract volume UUID from %s", *volumeHandle)
	}

	// Find the staging mount path
	// For CSI volumes, the staging path is typically /var/lib/kubelet/plugins/kubernetes.io/csi/pv/<pv-name>/globalmount
	stagingPath := filepath.Join("/var/lib/kubelet/plugins/kubernetes.io/csi/pv", *volumeHandle, "globalmount")

	// Verify the mount exists
	if w.mounter != nil {
		notMnt, err := w.mounter.IsLikelyNotMountPoint(stagingPath)
		if err != nil || notMnt {
			klog.V(4).Infof("Staging path %s is not a mount point or doesn't exist", stagingPath)
			return "", volumeUUID, nil
		}
	}

	return stagingPath, volumeUUID, nil
}

// extractVolumeUUID extracts a UUID from volume handle
func extractVolumeUUID(volumeHandle string) string {
	// Azure disk volume handles typically contain the disk name
	// Extract the last part after the last slash
	parts := strings.Split(volumeHandle, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return volumeHandle
}

// isTimedOut checks if the freeze request has timed out
func (w *VolumeAttachmentWatcher) isTimedOut(timestampStr string) bool {
	timestamp, err := time.Parse(time.RFC3339, timestampStr)
	if err != nil {
		klog.Errorf("Failed to parse timestamp %s: %v", timestampStr, err)
		return false
	}

	elapsed := time.Since(timestamp)
	return elapsed > w.timeout
}

// markProcessing marks a VolumeAttachment as being processed (or not)
func (w *VolumeAttachmentWatcher) markProcessing(vaName string, processing bool) bool {
	w.processingLock.Lock()
	defer w.processingLock.Unlock()

	if processing {
		if w.processing[vaName] {
			return false // Already processing
		}
		w.processing[vaName] = true
		return true
	}

	delete(w.processing, vaName)
	return true
}

// sendEvent sends a Kubernetes event
func (w *VolumeAttachmentWatcher) sendEvent(va *storagev1.VolumeAttachment, eventType, reason, message string) {
	if w.eventRecorder == nil {
		return
	}

	// Try to get the PV for better event context
	var ref *corev1.ObjectReference
	if va.Spec.Source.PersistentVolumeName != nil && *va.Spec.Source.PersistentVolumeName != "" {
		pv, err := w.kubeClient.CoreV1().PersistentVolumes().Get(context.Background(), *va.Spec.Source.PersistentVolumeName, metav1.GetOptions{})
		if err == nil {
			ref = &corev1.ObjectReference{
				Kind:      "PersistentVolume",
				Name:      pv.Name,
				UID:       pv.UID,
				Namespace: "",
			}
		}
	}

	if ref == nil {
		// Fallback to VolumeAttachment reference
		ref = &corev1.ObjectReference{
			Kind: "VolumeAttachment",
			Name: va.Name,
			UID:  va.UID,
		}
	}

	w.eventRecorder.Event(ref, eventType, reason, message)
	klog.V(4).Infof("Event sent: %s - %s: %s", eventType, reason, message)
}

// reconcileLoop periodically checks for timed out freeze operations
func (w *VolumeAttachmentWatcher) reconcileLoop(ctx context.Context) {
	defer w.wg.Done()

	ticker := time.NewTicker(ReconcileInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.reconcile(ctx)
		}
	}
}

// reconcile checks for VolumeAttachments that need attention
func (w *VolumeAttachmentWatcher) reconcile(ctx context.Context) {
	fieldSelector := fields.OneTermEqualSelector("spec.nodeName", w.nodeName).String()

	vaList, err := w.kubeClient.StorageV1().VolumeAttachments().List(ctx, metav1.ListOptions{
		FieldSelector: fieldSelector,
	})
	if err != nil {
		klog.Errorf("Failed to list VolumeAttachments during reconciliation: %v", err)
		return
	}

	// Build map of VolumeAttachments by volume UUID for lookup
	vaByVolumeUUID := make(map[string]*storagev1.VolumeAttachment)
	for i := range vaList.Items {
		va := &vaList.Items[i]

		if va.Annotations == nil {
			continue
		}

		freezeRequired := va.Annotations[AnnotationFreezeRequired]
		freezeState := va.Annotations[AnnotationFreezeState]

		// Check for timed out freeze operations
		if freezeRequired != "" && freezeState == "" {
			if w.isTimedOut(freezeRequired) {
				klog.Warningf("Reconciler: freeze operation timed out for VolumeAttachment %s", va.Name)
				w.updateFreezeState(ctx, va, FreezeStateSkipped)
				// Critical event as unfreeze on timeout is done by a reconciler which is not visible in controller
				w.sendEvent(va, corev1.EventTypeWarning, EventReasonFreezeTimeout,
					"Freeze operation timed out during reconciliation")
			}
		}

		// Check for frozen volumes without freeze-required annotation
		if freezeRequired == "" && freezeState == FreezeStateFrozen {
			klog.V(4).Infof("Reconciler: found frozen volume without freeze-required, triggering unfreeze for VA %s", va.Name)
			w.processUnfreeze(ctx, va)
		}

		// Build lookup map for checking local state
		if va.Spec.Source.PersistentVolumeName != nil && *va.Spec.Source.PersistentVolumeName != "" {
			volumeUUID := extractVolumeUUID(*va.Spec.Source.PersistentVolumeName)
			if volumeUUID != "" {
				vaByVolumeUUID[volumeUUID] = va
			}
		}
	}

	// Check local freeze state for orphaned frozen volumes
	frozenVolumes, err := w.freezeManager.ListFrozenVolumes()
	if err != nil {
		klog.Errorf("Failed to list frozen volumes from local state: %v", err)
		return
	}

	for volumeUUID, state := range frozenVolumes {
		// Check if freeze has timed out
		if w.isTimedOut(state.Timestamp.Format(time.RFC3339)) {
			klog.Warningf("Reconciler: frozen volume %s has timed out (frozen at %v), attempting unfreeze", volumeUUID, state.Timestamp)

			// Find mount path - try to construct it from volumeUUID
			// For CSI volumes, the staging path is typically /var/lib/kubelet/plugins/kubernetes.io/csi/pv/<pv-name>/globalmount
			mountPath := filepath.Join("/var/lib/kubelet/plugins/kubernetes.io/csi/pv", volumeUUID, "globalmount")

			// Verify the mount exists before attempting unfreeze
			if w.mounter != nil {
				notMnt, err := w.mounter.IsLikelyNotMountPoint(mountPath)
				if err != nil || notMnt {
					klog.V(4).Infof("Reconciler: mount path %s not found for timed out volume %s, cleaning up state", mountPath, volumeUUID)
					w.freezeManager.CleanupState(volumeUUID)
					continue
				}
			}

			// Attempt unfreeze
			if err := w.freezeManager.Unfreeze(ctx, mountPath, volumeUUID); err != nil {
				klog.Errorf("Reconciler: failed to unfreeze timed out volume %s at %s: %v", volumeUUID, mountPath, err)
				// Send event if VolumeAttachment exists
				if va, exists := vaByVolumeUUID[volumeUUID]; exists {
					w.sendEvent(va, corev1.EventTypeWarning, EventReasonUnfreezeFailed,
						fmt.Sprintf("Failed to unfreeze timed out volume at %s: %v", mountPath, err))
				}
			} else {
				klog.V(2).Infof("Reconciler: successfully unfroze timed out volume %s at %s", volumeUUID, mountPath)
				// Send event if VolumeAttachment exists
				if va, exists := vaByVolumeUUID[volumeUUID]; exists {
					// Critical event as unfreeze on timeout is done by a reconciler which is not visible in controller
					w.sendEvent(va, corev1.EventTypeWarning, EventReasonUnfreezeSuccess,
						fmt.Sprintf("Unfroze timed out volume at %s after timeout", mountPath))
					// Update VolumeAttachment if it has freeze-state annotation
					if va.Annotations[AnnotationFreezeState] == FreezeStateFrozen {
						w.cleanupFreezeState(ctx, va)
					}
				}
			}
		}
	}
}

// ParseTimeoutFromOptions parses timeout from driver options
func ParseTimeoutFromOptions(optionStr string) (int, error) {
	if optionStr == "" {
		return azureconstants.DefaultFreezeWaitTimeoutMinutes, nil
	}

	timeout, err := strconv.Atoi(optionStr)
	if err != nil {
		return 0, fmt.Errorf("invalid timeout value: %v", err)
	}

	if timeout <= 0 {
		return azureconstants.DefaultFreezeWaitTimeoutMinutes, nil
	}

	return timeout, nil
}
