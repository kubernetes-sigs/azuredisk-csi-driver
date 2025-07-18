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
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
)

const (
	// Migration monitoring constants
	migrationCheckInterval     = 30 * time.Second
	migrationTimeout           = 24 * time.Hour
	progressReportingThreshold = 20 // Report every 20% completion

	// Event types and reasons
	EventTypeNormal  = "Normal"
	EventTypeWarning = "Warning"

	ReasonSKUMigrationStarted   = "SKUMigrationStarted"
	ReasonSKUMigrationProgress  = "SKUMigrationProgress"
	ReasonSKUMigrationCompleted = "SKUMigrationCompleted"
	ReasonSKUMigrationFailed    = "SKUMigrationFailed"
	ReasonSKUMigrationTimeout   = "SKUMigrationTimeout"

	// Annotations for migration status
	AnnotationMigrationStatus = "disk.csi.azure.com/migration-status"
	AnnotationMigrationFrom   = "disk.csi.azure.com/migration-from-sku"
	AnnotationMigrationTo     = "disk.csi.azure.com/migration-to-sku"
	AnnotationMigrationStart  = "disk.csi.azure.com/migration-start-time"
)

// MigrationTask represents an active disk migration monitoring task
type MigrationTask struct {
	DiskURI              string
	PVName               string
	FromSKU              armcompute.DiskStorageAccountTypes
	ToSKU                armcompute.DiskStorageAccountTypes
	StartTime            time.Time
	LastReportedProgress float32
	Context              context.Context
	CancelFunc           context.CancelFunc
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

	// Create task context with timeout
	taskCtx, cancelFunc := context.WithTimeout(context.Background(), migrationTimeout)

	task := &MigrationTask{
		DiskURI:              diskURI,
		PVName:               pvName,
		FromSKU:              fromSKU,
		ToSKU:                toSKU,
		StartTime:            time.Now(),
		LastReportedProgress: 0,
		Context:              taskCtx,
		CancelFunc:           cancelFunc,
	}

	m.activeTasks[diskURI] = task

	// Start async monitoring
	go m.monitorMigrationProgress(task)

	// Add annotations to PV to track migration state and check if they were already present
	annotationsExisted, err := m.addMigrationAnnotationsIfNotExists(ctx, pvName, fromSKU, toSKU)
	if err != nil {
		klog.Warningf("Failed to add migration annotations to PV %s: %v", pvName, err)
	}

	// Only emit start event if annotations were not already present (new migration)
	if !annotationsExisted {
		_ = m.emitMigrationEvent(task, EventTypeNormal, ReasonSKUMigrationStarted,
			fmt.Sprintf("Started SKU migration from %s to %s for volume %s", fromSKU, toSKU, pvName))
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
			if task.Context.Err() == context.DeadlineExceeded {
				_ = m.emitMigrationEvent(task, EventTypeWarning, ReasonSKUMigrationTimeout,
					fmt.Sprintf("Migration timeout after %v for volume %s", migrationTimeout, task.PVName))
				klog.Warningf("Migration timeout for disk %s after %v", task.DiskURI, migrationTimeout)
			}
			return

		case <-ticker.C:
			completed, err := m.checkMigrationProgress(task)
			if err != nil {
				klog.V(4).Infof("Progress check error for disk %s (will retry): %v", task.DiskURI, err)
				continue
			}

			if completed {
				if m.emitMigrationEvent(task, EventTypeNormal, ReasonSKUMigrationCompleted,
					fmt.Sprintf("Successfully completed SKU migration from %s to %s for volume %s (duration: %v)",
						task.FromSKU, task.ToSKU, task.PVName, time.Since(task.StartTime))) != nil {
					klog.Errorf("Failed to emit completion event for disk %s: %v", task.DiskURI, err)
					return
				}
				klog.V(2).Infof("Migration completed for disk %s in %v", task.DiskURI, time.Since(task.StartTime))

				// Remove annotations when migration completes
				if err := m.removeMigrationAnnotations(task.Context, task.PVName); err != nil {
					klog.Warningf("Failed to remove migration annotations from PV %s: %v", task.PVName, err)
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
		_ = m.emitMigrationEvent(task, EventTypeNormal, ReasonSKUMigrationProgress,
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

// emitMigrationEvent emits a Kubernetes event for the PersistentVolume
func (m *MigrationProgressMonitor) emitMigrationEvent(task *MigrationTask, eventType, reason, message string) error {
	if m.eventRecorder == nil || m.kubeClient == nil {
		klog.Warningf("Event recorder or kube client not available, skipping event: %s", message)
		return fmt.Errorf("event recorder or kube client not available")
	}

	// Get PersistentVolume object
	pv, err := m.kubeClient.CoreV1().PersistentVolumes().Get(context.TODO(), task.PVName, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("Failed to get PersistentVolume %s for event emission: %v", task.PVName, err)
		// if error is not found, lets exit the migration go routine
		if apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}

	// Emit event on the PersistentVolume
	m.eventRecorder.Event(pv, eventType, reason, message)
	klog.V(4).Infof("Emitted event for PV %s: %s - %s", task.PVName, reason, message)
	return nil
}

// addMigrationAnnotationsIfNotExists adds migration-related annotations to the PersistentVolume if they don't already exist
// Returns true if annotations already existed, false if they were newly added
func (m *MigrationProgressMonitor) addMigrationAnnotationsIfNotExists(ctx context.Context, pvName string, fromSKU, toSKU armcompute.DiskStorageAccountTypes) (bool, error) {
	pv, err := m.kubeClient.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
	if err != nil {
		return false, err
	}

	// Check if migration annotations already exist
	if pv.Annotations != nil {
		if status, exists := pv.Annotations[AnnotationMigrationStatus]; exists && status == "in-progress" {
			// Verify that the SKU annotations match what we expect
			existingFromSKU := pv.Annotations[AnnotationMigrationFrom]
			existingToSKU := pv.Annotations[AnnotationMigrationTo]

			if existingFromSKU == string(fromSKU) && existingToSKU == string(toSKU) {
				klog.V(2).Infof("Migration annotations already exist for PV %s (%s -> %s)", pvName, fromSKU, toSKU)
				return true, nil
			}
		}
	}

	// Annotations don't exist or don't match, add them
	if pv.Annotations == nil {
		pv.Annotations = make(map[string]string)
	}

	pv.Annotations[AnnotationMigrationStatus] = "in-progress"
	pv.Annotations[AnnotationMigrationFrom] = string(fromSKU)
	pv.Annotations[AnnotationMigrationTo] = string(toSKU)
	pv.Annotations[AnnotationMigrationStart] = time.Now().Format(time.RFC3339)

	_, err = m.kubeClient.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{})
	return false, err
}

// removeMigrationAnnotations removes migration-related annotations from the PersistentVolume
func (m *MigrationProgressMonitor) removeMigrationAnnotations(ctx context.Context, pvName string) error {
	pv, err := m.kubeClient.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	if pv.Annotations != nil {
		delete(pv.Annotations, AnnotationMigrationStatus)
		delete(pv.Annotations, AnnotationMigrationFrom)
		delete(pv.Annotations, AnnotationMigrationTo)
		delete(pv.Annotations, AnnotationMigrationStart)
	}

	_, err = m.kubeClient.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{})
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

// Recovery function using annotations
func (d *Driver) recoverMigrationMonitorsFromAnnotations(ctx context.Context) error {
	pvList, err := d.cloud.KubeClient.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	recoveredCount := 0
	for _, pv := range pvList.Items {
		if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == d.Name {
			if status, exists := pv.Annotations[AnnotationMigrationStatus]; exists && status == "in-progress" {
				fromSKU := pv.Annotations[AnnotationMigrationFrom]
				toSKU := pv.Annotations[AnnotationMigrationTo]
				diskURI := pv.Spec.CSI.VolumeHandle

				if fromSKU != "" && toSKU != "" {
					klog.V(2).Infof("Recovering migration monitor for PV %s (%s -> %s)", pv.Name, fromSKU, toSKU)

					if err := d.migrationMonitor.StartMigrationMonitoring(ctx, diskURI, pv.Name,
						armcompute.DiskStorageAccountTypes(fromSKU),
						armcompute.DiskStorageAccountTypes(toSKU)); err != nil {
						klog.Errorf("Failed to recover migration for PV %s: %v", pv.Name, err)
					} else {
						recoveredCount++
					}
				}
			}
		}
	}

	klog.V(2).Infof("Recovered %d migration monitors from annotations", recoveredCount)
	return nil
}
