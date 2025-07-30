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
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
)

const (
	// Migration monitoring constants
	progressReportingThreshold = 20 // Report every 20% completion

	ReasonSKUMigrationStarted   = "SKUMigrationStarted"
	ReasonSKUMigrationProgress  = "SKUMigrationProgress"
	ReasonSKUMigrationCompleted = "SKUMigrationCompleted"
	ReasonSKUMigrationTimeout   = "SKUMigrationTimeout"

	// Label for migration status on PVC
	LabelMigrationInProgress = "disk.csi.azure.com/migration-in-progress"

	// Volume size thresholds (in bytes)
	volumeSize2TB = 2 * 1024 * 1024 * 1024 * 1024 // 2TB
	volumeSize4TB = 4 * 1024 * 1024 * 1024 * 1024 // 4TB

	// Timeout durations based on volume size
	migrationTimeoutSmall  = 5 * time.Hour  // Below 2TB
	migrationTimeoutMedium = 9 * time.Hour  // 2TB to 4TB
	migrationTimeoutLarge  = 10 * time.Hour // Above 4TB
)

var (
	migrationCheckInterval = 60 * time.Second
	// Migration timeout map
	// Note: This is a global variable to allow overriding via environment variable
	// It is initialized in init() function
	// This allows for testing and flexibility in production environments
	migrationTimeouts = map[int64]time.Duration{
		volumeSize2TB: migrationTimeoutSmall,
		volumeSize4TB: migrationTimeoutMedium,
	}

	// timeout array for small, medium and large volumes
	sortedMigrationSlabArray = []int64{
		volumeSize2TB, // 2TB
		volumeSize4TB, // 4TB
	}

	// Maximum migration timeout
	maxMigrationTimeout = 24 * time.Hour // Maximum allowed timeout for any migration
)

// getMigrationTimeout returns the appropriate timeout based on volume size
func getMigrationTimeout(volumeSize int64) time.Duration {
	for _, slab := range sortedMigrationSlabArray {
		if volumeSize < slab {
			if timeout, exists := migrationTimeouts[slab]; exists {
				return timeout
			}
			break // No more slabs to check
		}
	}
	return migrationTimeoutLarge
}

func initializeTimeouts() {
	// Use env variable if exists to override the maximum migration timeout mainly for testing purposes

	// overrides migrationTimeouts map in form of comma separated values like "2TB=5h,4TB=9h"
	if migrationTimeoutsEnv := os.Getenv("MIGRATION_TIMEOUTS"); migrationTimeoutsEnv != "" {
		// migration size array
		for _, pair := range strings.Split(migrationTimeoutsEnv, ",") {
			parts := strings.Split(pair, "=")
			if len(parts) != 2 {
				klog.Warningf("Invalid migration timeout format: %s", pair)
				continue
			}

			sizeStr := parts[0]
			timeoutStr := parts[1]

			var size resource.Quantity
			var timeout time.Duration
			var err error

			if size, err = resource.ParseQuantity(sizeStr); err != nil {
				klog.Warningf("Invalid migration timeout size: %s", sizeStr)
				continue
			}

			if timeout, err = time.ParseDuration(timeoutStr); err != nil {
				klog.Warningf("Invalid migration timeout duration: %s", timeoutStr)
				continue
			}

			migrationTimeouts[size.Value()] = timeout
			sortedMigrationSlabArray = append(sortedMigrationSlabArray, size.Value())
		}
		// sort the migration size array
		sort.Slice(sortedMigrationSlabArray, func(i, j int) bool {
			return sortedMigrationSlabArray[i] < sortedMigrationSlabArray[j]
		})

		klog.Infof("Sorted migration slab array: %v", sortedMigrationSlabArray)
	}

	if maxMigrationTimeoutEnv := os.Getenv("MAX_MIGRATION_TIMEOUT"); maxMigrationTimeoutEnv != "" {
		if duration, err := time.ParseDuration(maxMigrationTimeoutEnv); err == nil {
			maxMigrationTimeout = duration
		}
	}
}

func init() {
	initializeTimeouts()
}

// MigrationTask represents an active disk migration monitoring task
type MigrationTask struct {
	DiskURI              string
	PVName               string
	PVCName              string
	PVCNamespace         string
	FromSKU              armcompute.DiskStorageAccountTypes
	ToSKU                armcompute.DiskStorageAccountTypes
	StartTime            time.Time
	LastReportedProgress float32
	Context              context.Context
	CancelFunc           context.CancelFunc
	VolumeSize           int64         // Volume size in bytes
	MigrationTimeout     time.Duration // Calculated timeout based on volume size
	Timedout             bool          // Indicates if the task has timed out
}

// MigrationProgressMonitor monitors disk migration progress
type MigrationProgressMonitor struct {
	kubeClient     kubernetes.Interface
	eventRecorder  record.EventRecorder
	diskController *ManagedDiskController
	activeTasks    map[string]*MigrationTask
	mutex          sync.RWMutex
}

// NewMigrationProgressMonitor creates a new migration progress monitor
func NewMigrationProgressMonitor(kubeClient kubernetes.Interface, eventRecorder record.EventRecorder, diskController *ManagedDiskController) *MigrationProgressMonitor {
	return &MigrationProgressMonitor{
		kubeClient:     kubeClient,
		eventRecorder:  eventRecorder,
		diskController: diskController,
		activeTasks:    make(map[string]*MigrationTask),
	}
}

// StartMigrationMonitoring starts monitoring a disk migration with progress updates
func (m *MigrationProgressMonitor) StartMigrationMonitoring(ctx context.Context, diskURI, pvName string, fromSKU, toSKU armcompute.DiskStorageAccountTypes) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Check if already monitoring this disk
	if _, exists := m.activeTasks[diskURI]; exists {
		klog.V(2).Infof("Migration monitoring already active for disk %s", diskURI)
		return nil
	}

	// Get PV to find associated PVC
	pv, err := m.kubeClient.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get PV %s: %v", pvName, err)
	}

	// Check if PV has a claim reference
	if pv.Spec.ClaimRef == nil {
		klog.V(2).Infof("PV %s has no claim reference, skipping migration monitoring", pvName)
		return nil
	}

	pvcName := pv.Spec.ClaimRef.Name
	pvcNamespace := pv.Spec.ClaimRef.Namespace

	// Get volume size from PV capacity
	var volumeSize int64
	// Use the resource.Quantity methods explicitly to avoid import not recognized by linter/ide/goimports
	var storageQuantity resource.Quantity
	var exists bool
	if storageQuantity, exists = pv.Spec.Capacity[corev1.ResourceStorage]; exists {
		volumeSize = storageQuantity.Value()
	} else {
		return fmt.Errorf("PV %s does not have storage capacity defined", pvName)
	}

	// Calculate timeout based on volume size
	migrationTimeout := getMigrationTimeout(volumeSize)

	// Create task context with calculated timeout
	taskCtx, cancelFunc := context.WithTimeout(context.Background(), migrationTimeout)

	task := &MigrationTask{
		DiskURI:              diskURI,
		PVName:               pvName,
		PVCName:              pvcName,
		PVCNamespace:         pvcNamespace,
		FromSKU:              fromSKU,
		ToSKU:                toSKU,
		StartTime:            time.Now(),
		LastReportedProgress: 0,
		Context:              taskCtx,
		CancelFunc:           cancelFunc,
		VolumeSize:           volumeSize,
		MigrationTimeout:     migrationTimeout,
	}

	m.activeTasks[diskURI] = task

	// Start async monitoring
	go m.monitorMigrationProgress(task)

	// Add label to PVC to track migration state and check if it was already present
	labelExisted, err := m.addMigrationLabelIfNotExists(ctx, pvcName, pvcNamespace, fromSKU, toSKU)
	if err != nil {
		klog.Warningf("Failed to add migration label to PVC %s/%s: %v", pvcNamespace, pvcName, err)
	}

	// Log the timeout being used
	klog.V(2).Infof("Using migration timeout of %v for volume %s", migrationTimeout, pvName)

	// Only emit start event if label was not already present (new migration)
	if !labelExisted {
		_ = m.emitMigrationEvent(task, corev1.EventTypeNormal, ReasonSKUMigrationStarted,
			fmt.Sprintf("Started monitoring SKU migration from %s to %s for volume %s (timeout: %v)", fromSKU, toSKU, pvName, migrationTimeout))
		klog.V(2).Infof("Started migration monitoring for disk %s (%s -> %s)", pvName, fromSKU, toSKU)
	} else {
		klog.V(2).Infof("Resumed migration monitoring for disk %s (%s -> %s)", pvName, fromSKU, toSKU)
	}

	return nil
}

// monitorMigrationProgress monitors the progress of a disk migration
func (m *MigrationProgressMonitor) monitorMigrationProgress(task *MigrationTask) {
	defer func() {
		m.mutex.Lock()
		delete(m.activeTasks, task.DiskURI)
		m.mutex.Unlock()
		task.CancelFunc()
	}()

	ticker := time.NewTicker(migrationCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-task.Context.Done():
			if task.Context.Err() == context.Canceled {
				klog.Infof("Migration monitoring for disk %s cancelled", task.DiskURI)
				return
			}

			if !task.Timedout && task.Context.Err() == context.DeadlineExceeded {
				_ = m.emitMigrationEvent(task, corev1.EventTypeWarning, ReasonSKUMigrationTimeout,
					fmt.Sprintf("Migration taking too long (running %v hours) for volume %s", task.MigrationTimeout.Hours(), task.PVName))
				klog.Warningf("Migration taking too long (running %v hours) for disk %s", task.MigrationTimeout.Hours(), task.DiskURI)
				task.Timedout = true
			} else if time.Since(task.StartTime) >= maxMigrationTimeout {
				err := m.emitMigrationEvent(task, corev1.EventTypeWarning, ReasonSKUMigrationTimeout,
					fmt.Sprintf("Stopping monitoring the migration for the disk %s after waiting for %vh", task.DiskURI, maxMigrationTimeout.Hours()))
				if err != nil {
					klog.Errorf("Failed to emit timeout event for disk %s: %v", task.DiskURI, err)
				}
				klog.Warningf("Migration monitoring for disk %s cancelling after waiting for %vh", task.DiskURI, maxMigrationTimeout.Hours())
				return
			}
			task.CancelFunc()
			// Extend 1 hour deadline to avoid immediate exit
			// This allows the task to continue running and report progress
			// even if it has exceeded the initial timeout
			remaining := maxMigrationTimeout - time.Since(task.StartTime)
			if remaining <= 0 {
				// Migration has exceeded max timeout, stop monitoring
				return
			}
			task.Context, task.CancelFunc = context.WithTimeout(context.Background(), remaining)
			continue
		case <-ticker.C:
			completed, err := m.checkMigrationProgress(task)
			if err != nil {
				klog.V(4).Infof("Progress check error for disk %s (will retry): %v", task.DiskURI, err)
				continue
			}

			if completed {
				if m.emitMigrationEvent(task, corev1.EventTypeNormal, ReasonSKUMigrationCompleted,
					fmt.Sprintf("Successfully completed SKU migration from %s to %s for volume %s (duration: %v)",
						task.FromSKU, task.ToSKU, task.PVName, time.Since(task.StartTime))) != nil {
					klog.Errorf("Failed to emit completion event for disk %s: %v", task.DiskURI, err)
					return
				}
				klog.V(2).Infof("Migration completed for disk %s in %v", task.DiskURI, time.Since(task.StartTime))

				// Remove label when migration completes
				if err := m.removeMigrationLabel(task.Context, task.PVCName, task.PVCNamespace); err != nil {
					klog.Warningf("Failed to remove migration label from PVC %s/%s: %v", task.PVCNamespace, task.PVCName, err)
				}
				return
			}
		}
	}
}

// checkMigrationProgress checks the current progress of a disk migration
func (m *MigrationProgressMonitor) checkMigrationProgress(task *MigrationTask) (bool, error) {
	// Get current disk state
	disk, err := m.diskController.GetDiskByURI(task.Context, task.DiskURI)
	if err != nil {
		return false, fmt.Errorf("failed to get disk %s: %v", task.DiskURI, err)
	}

	// Check completion percentage if available
	var completionPercent float32
	if disk.Properties != nil && disk.Properties.CompletionPercent != nil {
		completionPercent = *disk.Properties.CompletionPercent
	}

	// Report progress if significant milestone reached
	if m.shouldReportProgress(completionPercent, task.LastReportedProgress) {
		_ = m.emitMigrationEvent(task, corev1.EventTypeNormal, ReasonSKUMigrationProgress,
			fmt.Sprintf("Migration progress: %.1f%% complete for volume %s (elapsed: %v)",
				completionPercent, task.PVName, time.Since(task.StartTime)))
		task.LastReportedProgress = completionPercent
		klog.V(2).Infof("Migration progress for disk %s: %.1f%% complete", task.DiskURI, completionPercent)
	}

	return completionPercent >= 100, nil
}

// shouldReportProgress determines if progress should be reported based on milestones
func (m *MigrationProgressMonitor) shouldReportProgress(current, last float32) bool {
	// Report at every 20% milestone
	currentMilestone := int(current/progressReportingThreshold) * progressReportingThreshold
	lastMilestone := int(last/progressReportingThreshold) * progressReportingThreshold

	return current < 100 && (currentMilestone > lastMilestone && currentMilestone > 0)
}

// emitMigrationEvent emits a Kubernetes event for the PersistentVolumeClaim
func (m *MigrationProgressMonitor) emitMigrationEvent(task *MigrationTask, eventType, reason, message string) error {
	if m.eventRecorder == nil || m.kubeClient == nil {
		klog.Warningf("Event recorder or kube client not available, skipping event: %s", message)
		return fmt.Errorf("event recorder or kube client not available")
	}

	// Get PersistentVolumeClaim object
	pvc, err := m.kubeClient.CoreV1().PersistentVolumeClaims(task.PVCNamespace).Get(task.Context, task.PVCName, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("Failed to get PersistentVolumeClaim %s/%s for event emission: %v", task.PVCNamespace, task.PVCName, err)
		// if error is not found, lets exit the migration go routine
		if apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}

	// Emit event on the PersistentVolumeClaim
	m.eventRecorder.Event(pvc, eventType, reason, message)
	klog.V(4).Infof("Emitted event for PVC %s/%s: %s - %s", task.PVCNamespace, task.PVCName, reason, message)
	return nil
}

// addMigrationLabelIfNotExists adds migration label to the PersistentVolumeClaim if it doesn't already exist
// Returns true if label already existed, false if it was newly added
func (m *MigrationProgressMonitor) addMigrationLabelIfNotExists(ctx context.Context, pvcName, pvcNamespace string, fromSKU, toSKU armcompute.DiskStorageAccountTypes) (bool, error) {
	pvc, err := m.kubeClient.CoreV1().PersistentVolumeClaims(pvcNamespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil {
		return false, err
	}

	// Check if migration label already exists
	if pvc.Labels != nil {
		if value, exists := pvc.Labels[LabelMigrationInProgress]; exists && value == "true" {
			klog.V(2).Infof("Migration label already exists for PVC %s/%s (%s -> %s)", pvcNamespace, pvcName, fromSKU, toSKU)
			return true, nil
		}
	}

	// Label doesn't exist, add it
	if pvc.Labels == nil {
		pvc.Labels = make(map[string]string)
	}

	pvc.Labels[LabelMigrationInProgress] = "true"

	_, err = m.kubeClient.CoreV1().PersistentVolumeClaims(pvcNamespace).Update(ctx, pvc, metav1.UpdateOptions{})
	return false, err
}

// removeMigrationLabel removes migration label from the PersistentVolumeClaim
func (m *MigrationProgressMonitor) removeMigrationLabel(ctx context.Context, pvcName, pvcNamespace string) error {
	pvc, err := m.kubeClient.CoreV1().PersistentVolumeClaims(pvcNamespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	delete(pvc.Labels, LabelMigrationInProgress)

	_, err = m.kubeClient.CoreV1().PersistentVolumeClaims(pvcNamespace).Update(ctx, pvc, metav1.UpdateOptions{})
	return err
}

// GetActiveMigrations returns currently active migration tasks
func (m *MigrationProgressMonitor) GetActiveMigrations() map[string]*MigrationTask {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	result := make(map[string]*MigrationTask)
	for k, v := range m.activeTasks {
		taskCopy := *v
		result[k] = &taskCopy
	}
	return result
}

// IsMigrationActive checks if migration is currently being monitored for a disk
func (m *MigrationProgressMonitor) IsMigrationActive(diskURI string) bool {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	_, exists := m.activeTasks[diskURI]
	return exists
}

// Stop stops all active migration monitoring tasks
func (m *MigrationProgressMonitor) Stop() {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	for _, task := range m.activeTasks {
		if task.CancelFunc != nil {
			task.CancelFunc()
		}
	}

	klog.V(2).Infof("Migration progress monitor stopped, cancelled %d active tasks", len(m.activeTasks))
}

// Recovery function using labels
func (d *Driver) recoverMigrationMonitorsFromLabels(ctx context.Context) error {
	// List PVCs with migration label
	labelSelector := fmt.Sprintf("%s=true", LabelMigrationInProgress)
	pvcList, err := d.cloud.KubeClient.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return err
	}

	recoveredCount := 0
	for _, pvc := range pvcList.Items {
		// Get the associated PV
		if pvc.Spec.VolumeName == "" {
			klog.V(2).Infof("PVC %s/%s has no volume name, skipping recovery", pvc.Namespace, pvc.Name)
			continue
		}

		pv, err := d.cloud.KubeClient.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
		if err != nil {
			klog.Errorf("Failed to get PV %s for PVC %s/%s: %v", pvc.Spec.VolumeName, pvc.Namespace, pvc.Name, err)
			continue
		}

		// Check if it's an Azure disk CSI volume
		if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == d.Name {
			diskURI := pv.Spec.CSI.VolumeHandle

			// For recovery, we need to determine fromSKU and toSKU from the disk properties
			// This is a limitation - we'll use a placeholder approach here
			// In a real implementation, you might want to store this info in additional labels or get it from the disk
			klog.V(2).Infof("Recovering migration monitor for PVC %s/%s (PV: %s)", pvc.Namespace, pvc.Name, pv.Name)

			// For now, we'll use placeholders - in practice, you'd need to get this from disk properties or additional labels
			fromSKU := armcompute.DiskStorageAccountTypesPremiumLRS // This would need to be determined
			toSKU := armcompute.DiskStorageAccountTypesPremiumV2LRS // This would need to be determined

			if err := d.migrationMonitor.StartMigrationMonitoring(ctx, diskURI, pv.Name, fromSKU, toSKU); err != nil {
				klog.Errorf("Failed to recover migration for PVC %s/%s: %v", pvc.Namespace, pvc.Name, err)
			} else {
				recoveredCount++
			}
		}
	}

	klog.V(2).Infof("Recovered %d migration monitors from labels", recoveredCount)
	return nil
}
