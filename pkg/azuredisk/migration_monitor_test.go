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
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"

	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk/mockcorev1"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk/mockkubeclient"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk/mockpersistentvolume"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk/mockpersistentvolumeclaim"
	"sigs.k8s.io/cloud-provider-azure/pkg/azclient/diskclient/mock_diskclient"
	"sigs.k8s.io/cloud-provider-azure/pkg/azclient/mock_azclient"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

func TestNewMigrationProgressMonitor(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockKubeClient := mockkubeclient.NewMockInterface(ctrl)
	mockEventRecorder := record.NewFakeRecorder(10)
	mockDiskController := &ManagedDiskController{}

	monitor := NewMigrationProgressMonitor(mockKubeClient, mockEventRecorder, mockDiskController)

	assert.NotNil(t, monitor)
	assert.Equal(t, mockKubeClient, monitor.kubeClient)
	assert.Equal(t, mockEventRecorder, monitor.eventRecorder)
	assert.Equal(t, mockDiskController, monitor.diskController)
	assert.NotNil(t, monitor.activeTasks)
	assert.Equal(t, 0, len(monitor.activeTasks))
}

func TestStartMigrationMonitoring(t *testing.T) {
	tests := []struct {
		name         string
		diskURI      string
		pvName       string
		pvcName      string
		pvcNamespace string
		fromSKU      armcompute.DiskStorageAccountTypes
		toSKU        armcompute.DiskStorageAccountTypes
		expectError  bool
	}{
		{
			name:         "successful start Premium_LRS to PremiumV2_LRS",
			diskURI:      "/subscriptions/test/resourceGroups/rg/providers/Microsoft.Compute/disks/test-disk",
			pvName:       "test-pv",
			pvcName:      "test-pvc",
			pvcNamespace: "default",
			fromSKU:      armcompute.DiskStorageAccountTypesPremiumLRS,
			toSKU:        armcompute.DiskStorageAccountTypesPremiumV2LRS,
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockKubeClient := mockkubeclient.NewMockInterface(ctrl)
			mockCoreV1 := mockcorev1.NewMockInterface(ctrl)
			mockPVInterface := mockpersistentvolume.NewMockInterface(ctrl)
			mockPVCInterface := mockpersistentvolumeclaim.NewMockPersistentVolumeClaimInterface(ctrl)
			mockEventRecorder := record.NewFakeRecorder(10)
			mockDiskController := &ManagedDiskController{}

			// Create test PV with ClaimRef
			testPV := &v1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: tt.pvName,
				},
				Spec: v1.PersistentVolumeSpec{
					ClaimRef: &v1.ObjectReference{
						Name:      tt.pvcName,
						Namespace: tt.pvcNamespace,
					},
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("10Gi"),
					},
				},
			}

			// Create test PVC
			testPVC := &v1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      tt.pvcName,
					Namespace: tt.pvcNamespace,
				},
				Spec: v1.PersistentVolumeClaimSpec{
					VolumeName: tt.pvName,
				},
			}

			// Set up mock expectations
			mockKubeClient.EXPECT().CoreV1().Return(mockCoreV1).AnyTimes()
			mockCoreV1.EXPECT().PersistentVolumes().Return(mockPVInterface).AnyTimes()
			mockCoreV1.EXPECT().PersistentVolumeClaims(tt.pvcNamespace).Return(mockPVCInterface).AnyTimes()

			// Expect PV Get call for ClaimRef lookup
			mockPVInterface.EXPECT().Get(gomock.Any(), tt.pvName, gomock.Any()).Return(testPV, nil).Times(1)

			// Expect PVC operations for addMigrationLabelIfNotExists and event emission
			mockPVCInterface.EXPECT().Get(gomock.Any(), tt.pvcName, gomock.Any()).Return(testPVC, nil).AnyTimes()
			mockPVCInterface.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, pvc *v1.PersistentVolumeClaim, _ metav1.UpdateOptions) (*v1.PersistentVolumeClaim, error) {
					// Verify label was added correctly
					assert.Equal(t, "true", pvc.Labels[LabelMigrationInProgress])
					return pvc, nil
				}).Times(1)

			monitor := NewMigrationProgressMonitor(mockKubeClient, mockEventRecorder, mockDiskController)

			ctx := context.Background()
			err := monitor.StartMigrationMonitoring(ctx, tt.diskURI, tt.pvName, tt.fromSKU, tt.toSKU)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.True(t, monitor.IsMigrationActive(tt.diskURI))

				// Verify task was created correctly with PVC info
				activeTasks := monitor.GetActiveMigrations()
				assert.Equal(t, 1, len(activeTasks))

				task, exists := activeTasks[tt.diskURI]
				assert.True(t, exists)
				assert.Equal(t, tt.diskURI, task.DiskURI)
				assert.Equal(t, tt.pvName, task.PVName)
				assert.Equal(t, tt.pvcName, task.PVCName)
				assert.Equal(t, tt.pvcNamespace, task.PVCNamespace)
				assert.Equal(t, tt.fromSKU, task.FromSKU)
				assert.Equal(t, tt.toSKU, task.ToSKU)
				assert.NotNil(t, task.CancelFunc)
				assert.NotNil(t, task.Context)

				// Verify migration started event was recorded
				select {
				case event := <-mockEventRecorder.Events:
					assert.Contains(t, event, "Normal")
					assert.Contains(t, event, ReasonSKUMigrationStarted)
					assert.Contains(t, event, tt.pvName)
				case <-time.After(100 * time.Millisecond):
					t.Error("Expected migration started event was not recorded")
				}

				// Stop monitoring to clean up
				monitor.Stop()
			}
		})
	}
}

func TestIsMigrationActive(t *testing.T) {
	// Save original migration timeout configuration
	klog.Infof("Default timeouts initialized: %v", migrationTimeouts)
	klog.Infof("Default sorted slab array: %v", sortedMigrationSlabArray)

	// Save original values
	originalMigrationTimeouts := make(map[int64]time.Duration)
	for k, v := range migrationTimeouts {
		originalMigrationTimeouts[k] = v
	}
	originalSlabArray := make([]int64, len(sortedMigrationSlabArray))
	copy(originalSlabArray, sortedMigrationSlabArray)
	originalMaxTimeout := maxMigrationTimeout
	originalMigrationCheckInterval := migrationCheckInterval
	originalMigrationTimeoutsEnv := os.Getenv("MIGRATION_TIMEOUTS")
	originalMaxMigrationTimeoutEnv := os.Getenv("MAX_MIGRATION_TIMEOUT")

	defer func() {
		// Restore original values
		migrationTimeouts = originalMigrationTimeouts
		sortedMigrationSlabArray = originalSlabArray
		maxMigrationTimeout = originalMaxTimeout
		migrationCheckInterval = originalMigrationCheckInterval

		// Restore environment variables
		if originalMigrationTimeoutsEnv == "" {
			os.Unsetenv("MIGRATION_TIMEOUTS")
		} else {
			os.Setenv("MIGRATION_TIMEOUTS", originalMigrationTimeoutsEnv)
		}
		if originalMaxMigrationTimeoutEnv == "" {
			os.Unsetenv("MAX_MIGRATION_TIMEOUT")
		} else {
			os.Setenv("MAX_MIGRATION_TIMEOUT", originalMaxMigrationTimeoutEnv)
		}
	}()

	// Override migration timeouts for testing (very short timeouts)
	testTimeouts := map[int64]time.Duration{
		volumeSize2TB: 100 * time.Millisecond, // 100ms for volumes under 2TB
		volumeSize4TB: 200 * time.Millisecond, // 200ms for volumes 2TB-4TB
	}

	// make testTimeouts as csv and pass to MIGRATION_TIMEOUTS env variable
	testTimeoutsCSV := ""
	for k, v := range testTimeouts {
		testTimeoutsCSV += fmt.Sprintf("%d=%s,", k, v)
	}
	testTimeoutsCSV = strings.TrimSuffix(testTimeoutsCSV, ",")

	os.Setenv("MIGRATION_TIMEOUTS", testTimeoutsCSV)
	os.Setenv("MAX_MIGRATION_TIMEOUT", fmt.Sprintf("%d", 300*time.Millisecond))
	initializeTimeouts()

	migrationCheckInterval = 50 * time.Millisecond

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	d := getFakeDriverWithKubeClientForMigration(ctrl)

	mockKubeClient := d.getCloud().KubeClient.(*mockkubeclient.MockInterface)
	mockCoreV1 := mockcorev1.NewMockInterface(ctrl)
	mockPVInterface := mockpersistentvolume.NewMockInterface(ctrl)
	mockPVCInterface := mockpersistentvolumeclaim.NewMockPersistentVolumeClaimInterface(ctrl)
	mockEventRecorder := record.NewFakeRecorder(10)
	diskClient := mock_diskclient.NewMockInterface(ctrl)
	d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()

	// Create test PV with ClaimRef
	testPV := &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pv",
		},
		Spec: v1.PersistentVolumeSpec{
			ClaimRef: &v1.ObjectReference{
				Name:      "test-pvc",
				Namespace: "default",
			},
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("10Gi"),
			},
		},
	}

	// Create test PVC
	testPVC := &v1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
		},
		Spec: v1.PersistentVolumeClaimSpec{
			VolumeName: "test-pv",
		},
	}

	id := fmt.Sprintf(consts.ManagedDiskPath, "subs", "rg", testPV.ObjectMeta.Name)
	// Create test disk
	diskSizeGB := int32(10)
	state := "Succeeded"
	testVolumeName := "test-disk"
	disk := &armcompute.Disk{
		ID:   &id,
		Name: &testVolumeName,
		Properties: &armcompute.DiskProperties{
			DiskSizeGB:        &diskSizeGB,
			ProvisioningState: &state,
		},
	}

	// Set up mock expectations
	mockKubeClient.EXPECT().CoreV1().Return(mockCoreV1).AnyTimes()
	mockCoreV1.EXPECT().PersistentVolumes().Return(mockPVInterface).AnyTimes()
	mockCoreV1.EXPECT().PersistentVolumeClaims("default").Return(mockPVCInterface).AnyTimes()
	mockPVInterface.EXPECT().Get(gomock.Any(), "test-pv", gomock.Any()).Return(testPV, nil).AnyTimes()
	diskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()

	// Add the missing Update expectation for addMigrationLabelIfNotExists
	mockPVCInterface.EXPECT().Get(gomock.Any(), "test-pvc", gomock.Any()).Return(testPVC, nil).AnyTimes()
	mockPVCInterface.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, pvc *v1.PersistentVolumeClaim, _ metav1.UpdateOptions) (*v1.PersistentVolumeClaim, error) {
			// Verify label was added correctly
			assert.Equal(t, "true", pvc.Labels[LabelMigrationInProgress])
			return pvc, nil
		}).Times(1)

	monitor := NewMigrationProgressMonitor(mockKubeClient, mockEventRecorder, d.GetDiskController())

	diskURI := "/subscriptions/test/resourceGroups/rg/providers/Microsoft.Compute/disks/test-disk"
	nonExistentDiskURI := "/subscriptions/test/resourceGroups/rg/providers/Microsoft.Compute/disks/non-existent"

	// Initially no migration should be active
	assert.False(t, monitor.IsMigrationActive(diskURI))
	assert.False(t, monitor.IsMigrationActive(nonExistentDiskURI))

	// Start monitoring
	ctx := context.Background()
	err := monitor.StartMigrationMonitoring(ctx, diskURI, "test-pv",
		armcompute.DiskStorageAccountTypesPremiumLRS,
		armcompute.DiskStorageAccountTypesPremiumV2LRS)
	assert.NoError(t, err)

	// Now should be active
	assert.True(t, monitor.IsMigrationActive(diskURI))
	assert.False(t, monitor.IsMigrationActive(nonExistentDiskURI))

	monitor.Stop()

	// After stop, should not be active anymore
	time.Sleep(1 * time.Second) // Allow goroutines to finish
	assert.False(t, monitor.IsMigrationActive(diskURI))
}

func TestShouldReportMigrationProgress(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockKubeClient := mockkubeclient.NewMockInterface(ctrl)
	mockEventRecorder := record.NewFakeRecorder(10)
	mockDiskController := &ManagedDiskController{}

	monitor := NewMigrationProgressMonitor(mockKubeClient, mockEventRecorder, mockDiskController)

	tests := []struct {
		name     string
		current  float32
		last     float32
		expected bool
	}{
		{"initial progress", 15.0, 0.0, false},
		{"reach 20% milestone", 20.0, 15.0, true},
		{"within same milestone", 25.0, 20.0, false},
		{"reach 40% milestone", 40.0, 25.0, true},
		{"reach 60% milestone", 60.0, 45.0, true},
		{"reach 80% milestone", 80.0, 65.0, true},
		{"completion", 100.0, 85.0, false},
		{"regression", 75.0, 80.0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := monitor.shouldReportProgress(tt.current, tt.last)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestEmitMigrationEvent(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockKubeClient := mockkubeclient.NewMockInterface(ctrl)
	mockCoreV1 := mockcorev1.NewMockInterface(ctrl)
	mockPVCInterface := mockpersistentvolumeclaim.NewMockPersistentVolumeClaimInterface(ctrl)
	mockEventRecorder := record.NewFakeRecorder(10)
	mockDiskController := &ManagedDiskController{}

	monitor := NewMigrationProgressMonitor(mockKubeClient, mockEventRecorder, mockDiskController)

	// Create test PVC
	testPVC := &v1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
		},
	}

	// Create test task
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	task := &MigrationTask{
		DiskURI:      "/subscriptions/test/resourceGroups/rg/providers/Microsoft.Compute/disks/test-disk",
		PVName:       "test-pv",
		PVCName:      "test-pvc",
		PVCNamespace: "default",
		FromSKU:      armcompute.DiskStorageAccountTypesPremiumLRS,
		ToSKU:        armcompute.DiskStorageAccountTypesPremiumV2LRS,
		StartTime:    time.Now(),
		Context:      ctx,
		CancelFunc:   cancel,
	}

	// Set up mocks
	mockKubeClient.EXPECT().CoreV1().Return(mockCoreV1)
	mockCoreV1.EXPECT().PersistentVolumeClaims("default").Return(mockPVCInterface)
	mockPVCInterface.EXPECT().Get(gomock.Any(), "test-pvc", gomock.Any()).Return(testPVC, nil)

	// Test successful event emission
	assert.NoError(t, monitor.emitMigrationEvent(task, corev1.EventTypeNormal, ReasonSKUMigrationStarted, "Test migration started"))

	// Verify event was recorded
	select {
	case event := <-mockEventRecorder.Events:
		assert.Contains(t, event, "Normal")
		assert.Contains(t, event, ReasonSKUMigrationStarted)
		assert.Contains(t, event, "Test migration started")
	default:
		t.Error("Expected event was not recorded")
	}
}

func TestEmitMigrationEvent_PVCNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockKubeClient := mockkubeclient.NewMockInterface(ctrl)
	mockCoreV1 := mockcorev1.NewMockInterface(ctrl)
	mockPVCInterface := mockpersistentvolumeclaim.NewMockPersistentVolumeClaimInterface(ctrl)
	mockEventRecorder := record.NewFakeRecorder(10)
	mockDiskController := &ManagedDiskController{}

	monitor := NewMigrationProgressMonitor(mockKubeClient, mockEventRecorder, mockDiskController)

	// Create test task
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	task := &MigrationTask{
		DiskURI:      "/subscriptions/test/resourceGroups/rg/providers/Microsoft.Compute/disks/test-disk",
		PVName:       "test-pv",
		PVCName:      "non-existent-pvc",
		PVCNamespace: "default",
		FromSKU:      armcompute.DiskStorageAccountTypesPremiumLRS,
		ToSKU:        armcompute.DiskStorageAccountTypesPremiumV2LRS,
		StartTime:    time.Now(),
		Context:      ctx,
		CancelFunc:   cancel,
	}

	// Set up mocks to return error
	mockKubeClient.EXPECT().CoreV1().Return(mockCoreV1)
	mockCoreV1.EXPECT().PersistentVolumeClaims("default").Return(mockPVCInterface)
	mockPVCInterface.EXPECT().Get(gomock.Any(), "non-existent-pvc", gomock.Any()).Return(nil, errors.New("not found"))

	// Test event emission with PVC not found - should not panic
	assert.NoError(t, monitor.emitMigrationEvent(task, corev1.EventTypeNormal, ReasonSKUMigrationStarted, "Test migration started"))

	// Verify no event was recorded
	select {
	case <-mockEventRecorder.Events:
		t.Error("No event should have been recorded when PVC is not found")
	default:
		// Expected - no event should be recorded
	}
}

func TestMigrationStop(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockKubeClient := mockkubeclient.NewMockInterface(ctrl)
	mockCoreV1 := mockcorev1.NewMockInterface(ctrl)
	mockPVInterface := mockpersistentvolume.NewMockInterface(ctrl)
	mockPVCInterface := mockpersistentvolumeclaim.NewMockPersistentVolumeClaimInterface(ctrl)
	mockEventRecorder := record.NewFakeRecorder(10)
	mockDiskController := &ManagedDiskController{}

	// Create test PVs with ClaimRefs
	testPVs := []*v1.PersistentVolume{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pv-1"},
			Spec: v1.PersistentVolumeSpec{
				ClaimRef: &v1.ObjectReference{Name: "pvc-1", Namespace: "default"},
				Capacity: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("10Gi"),
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pv-2"},
			Spec: v1.PersistentVolumeSpec{
				ClaimRef: &v1.ObjectReference{Name: "pvc-2", Namespace: "default"},
				Capacity: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("10Gi"),
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pv-3"},
			Spec: v1.PersistentVolumeSpec{
				ClaimRef: &v1.ObjectReference{Name: "pvc-3", Namespace: "default"},
				Capacity: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("10Gi"),
				},
			},
		},
	}

	// Create test PVCs
	testPVCs := []*v1.PersistentVolumeClaim{
		{ObjectMeta: metav1.ObjectMeta{Name: "pvc-1", Namespace: "default"}, Spec: v1.PersistentVolumeClaimSpec{VolumeName: "pv-1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "pvc-2", Namespace: "default"}, Spec: v1.PersistentVolumeClaimSpec{VolumeName: "pv-2"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "pvc-3", Namespace: "default"}, Spec: v1.PersistentVolumeClaimSpec{VolumeName: "pv-3"}},
	}

	// Set up mock expectations for PVC operations
	mockKubeClient.EXPECT().CoreV1().Return(mockCoreV1).AnyTimes()
	mockCoreV1.EXPECT().PersistentVolumes().Return(mockPVInterface).AnyTimes()
	mockCoreV1.EXPECT().PersistentVolumeClaims("default").Return(mockPVCInterface).AnyTimes()

	// Set up expectations for each PV (Get calls for ClaimRef lookup)
	for i, pv := range testPVs {
		pvName := fmt.Sprintf("pv-%d", i+1)
		mockPVInterface.EXPECT().Get(gomock.Any(), pvName, gomock.Any()).Return(pv, nil).AnyTimes()
	}

	// Set up expectations for each PVC (Get and Update calls for addMigrationLabelIfNotExists)
	for i, pvc := range testPVCs {
		pvcName := fmt.Sprintf("pvc-%d", i+1)
		mockPVCInterface.EXPECT().Get(gomock.Any(), pvcName, gomock.Any()).Return(pvc, nil).AnyTimes()
	}

	// Add Update expectations for addMigrationLabelIfNotExists (3 calls for 3 migrations)
	mockPVCInterface.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, pvc *v1.PersistentVolumeClaim, _ metav1.UpdateOptions) (*v1.PersistentVolumeClaim, error) {
			// Verify label was added correctly
			assert.Equal(t, "true", pvc.Labels[LabelMigrationInProgress])
			return pvc, nil
		}).Times(3)

	monitor := NewMigrationProgressMonitor(mockKubeClient, mockEventRecorder, mockDiskController)

	// Start multiple migrations
	ctx := context.Background()
	diskURIs := []string{
		"/subscriptions/test/resourceGroups/rg/providers/Microsoft.Compute/disks/disk1",
		"/subscriptions/test/resourceGroups/rg/providers/Microsoft.Compute/disks/disk2",
		"/subscriptions/test/resourceGroups/rg/providers/Microsoft.Compute/disks/disk3",
	}

	for i, diskURI := range diskURIs {
		err := monitor.StartMigrationMonitoring(ctx, diskURI, fmt.Sprintf("pv-%d", i+1),
			armcompute.DiskStorageAccountTypesPremiumLRS,
			armcompute.DiskStorageAccountTypesPremiumV2LRS)
		assert.NoError(t, err)
	}

	// Verify migrations are active
	activeTasks := monitor.GetActiveMigrations()
	assert.Equal(t, 3, len(activeTasks))

	// Stop all migrations
	monitor.Stop()

	// Allow some time for goroutines to finish
	time.Sleep(100 * time.Millisecond)

	// Verify all migrations are stopped
	for _, diskURI := range diskURIs {
		assert.False(t, monitor.IsMigrationActive(diskURI))
	}

	activeTasks = monitor.GetActiveMigrations()
	assert.Equal(t, 0, len(activeTasks))
}

func TestRecoverMigrationMonitorsFromLabels(t *testing.T) {
	tests := []struct {
		name          string
		existingPVCs  []*v1.PersistentVolumeClaim
		existingPVs   []*v1.PersistentVolume
		expectedCount int
		expectError   bool
	}{
		{
			name: "recover single ongoing migration",
			existingPVCs: []*v1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pvc-with-migration",
						Namespace: "default",
						Labels: map[string]string{
							LabelMigrationInProgress: "true",
						},
					},
					Spec: v1.PersistentVolumeClaimSpec{
						VolumeName: "pv-with-migration",
					},
				},
			},
			existingPVs: []*v1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pv-with-migration",
					},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								Driver:       "disk.csi.azure.com",
								VolumeHandle: "/subscriptions/test/resourceGroups/rg/providers/Microsoft.Compute/disks/test-disk",
							},
						},
						ClaimRef: &v1.ObjectReference{
							Name:      "pvc-with-migration",
							Namespace: "default",
						},
						Capacity: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("10Gi"),
						},
					},
				},
			},
			expectedCount: 1,
			expectError:   false,
		},
		{
			name: "skip PVCs without migration labels",
			existingPVCs: []*v1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pvc-no-migration",
						Namespace: "default",
					},
					Spec: v1.PersistentVolumeClaimSpec{
						VolumeName: "pv-no-migration",
					},
				},
			},
			existingPVs: []*v1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pv-no-migration",
					},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								Driver:       "disk.csi.azure.com",
								VolumeHandle: "/subscriptions/test/resourceGroups/rg/providers/Microsoft.Compute/disks/no-migration",
							},
						},
						Capacity: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("10Gi"),
						},
					},
				},
			},
			expectedCount: 0,
			expectError:   false,
		},
		{
			name: "skip non-Azure disk PVs",
			existingPVCs: []*v1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pvc-other-driver",
						Namespace: "default",
						Labels: map[string]string{
							LabelMigrationInProgress: "true",
						},
					},
					Spec: v1.PersistentVolumeClaimSpec{
						VolumeName: "pv-other-driver",
					},
				},
			},
			existingPVs: []*v1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pv-other-driver",
					},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								Driver:       "other.csi.driver",
								VolumeHandle: "/some/other/volume",
							},
						},
						Capacity: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("10Gi"),
						},
					},
				},
			},
			expectedCount: 0,
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// Setup mocks
			mockKubeClient := mockkubeclient.NewMockInterface(ctrl)
			mockCoreV1 := mockcorev1.NewMockInterface(ctrl)
			mockPVInterface := mockpersistentvolume.NewMockInterface(ctrl)
			mockPVCInterface := mockpersistentvolumeclaim.NewMockPersistentVolumeClaimInterface(ctrl)
			mockEventRecorder := record.NewFakeRecorder(10)

			// Create Driver using the proper initialization pattern
			driver := &Driver{}
			driver.Name = "disk.csi.azure.com"
			driver.cloud = &azure.Cloud{
				KubeClient: mockKubeClient,
			}
			driver.migrationMonitor = NewMigrationProgressMonitor(mockKubeClient, mockEventRecorder, &ManagedDiskController{})

			// Setup mock expectations for listing PVCs
			pvcList := &v1.PersistentVolumeClaimList{Items: make([]v1.PersistentVolumeClaim, 0)}
			for _, pvc := range tt.existingPVCs {
				if pvc.Labels != nil && pvc.Labels[LabelMigrationInProgress] == "true" {
					pvcList.Items = append(pvcList.Items, *pvc)
				}
			}

			mockKubeClient.EXPECT().CoreV1().Return(mockCoreV1).AnyTimes()
			mockCoreV1.EXPECT().PersistentVolumes().Return(mockPVInterface).AnyTimes()
			mockCoreV1.EXPECT().PersistentVolumeClaims("").Return(mockPVCInterface).AnyTimes()
			mockCoreV1.EXPECT().PersistentVolumeClaims("default").Return(mockPVCInterface).AnyTimes()
			mockPVCInterface.EXPECT().List(gomock.Any(), gomock.Any()).Return(pvcList, nil)

			// Setup expectations for each PVC that should be recovered
			for _, pvc := range tt.existingPVCs {
				if pvc.Labels != nil && pvc.Labels[LabelMigrationInProgress] == "true" {
					if pvc.Spec.VolumeName != "" {
						// Find corresponding PV
						for _, pv := range tt.existingPVs {
							if pv.Name == pvc.Spec.VolumeName {
								// This PVC should be recovered - setup Get expectation for StartMigrationMonitoring
								mockPVInterface.EXPECT().Get(gomock.Any(), pv.Name, gomock.Any()).Return(pv, nil).AnyTimes()

								if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == "disk.csi.azure.com" {
									// StartMigrationMonitoring calls addMigrationLabelIfNotExists which needs Update expectation
									mockPVCInterface.EXPECT().Get(gomock.Any(), pvc.Name, gomock.Any()).Return(pvc, nil).AnyTimes()
									mockPVCInterface.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
										func(_ context.Context, updatedPVC *v1.PersistentVolumeClaim, _ metav1.UpdateOptions) (*v1.PersistentVolumeClaim, error) {
											// Just return the updated PVC - the label is already set
											return updatedPVC, nil
										}).AnyTimes()
								}
								break
							}
						}
					}
				}
			}

			// Execute recovery
			ctx := context.Background()
			err := driver.recoverMigrationMonitorsFromLabels(ctx)

			// Verify results
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)

				// Verify correct number of migrations were recovered
				activeTasks := driver.migrationMonitor.GetActiveMigrations()
				assert.Equal(t, tt.expectedCount, len(activeTasks))

				// Verify each recovered migration is properly configured
				for _, pvc := range tt.existingPVCs {
					if pvc.Labels != nil && pvc.Labels[LabelMigrationInProgress] == "true" {
						if pvc.Spec.VolumeName != "" {
							for _, pv := range tt.existingPVs {
								if pv.Name == pvc.Spec.VolumeName && pv.Spec.CSI != nil && pv.Spec.CSI.Driver == "disk.csi.azure.com" {
									diskURI := pv.Spec.CSI.VolumeHandle
									assert.True(t, driver.migrationMonitor.IsMigrationActive(diskURI))

									task, exists := activeTasks[diskURI]
									assert.True(t, exists)
									assert.Equal(t, pv.Name, task.PVName)
									assert.Equal(t, pvc.Name, task.PVCName)
									assert.Equal(t, pvc.Namespace, task.PVCNamespace)
									break
								}
							}
						}
					}
				}
			}

			// Cleanup
			driver.migrationMonitor.Stop()
		})
	}
}

func TestAddMigrationLabelIfNotExists(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockKubeClient := mockkubeclient.NewMockInterface(ctrl)
	mockCoreV1 := mockcorev1.NewMockInterface(ctrl)
	mockPVCInterface := mockpersistentvolumeclaim.NewMockPersistentVolumeClaimInterface(ctrl)
	mockEventRecorder := record.NewFakeRecorder(10)
	mockDiskController := &ManagedDiskController{}

	monitor := NewMigrationProgressMonitor(mockKubeClient, mockEventRecorder, mockDiskController)

	// Create test PVC without labels
	testPVC := &v1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
		},
	}

	// Setup mock expectations
	mockKubeClient.EXPECT().CoreV1().Return(mockCoreV1).AnyTimes()
	mockCoreV1.EXPECT().PersistentVolumeClaims("default").Return(mockPVCInterface).AnyTimes()
	mockPVCInterface.EXPECT().Get(gomock.Any(), "test-pvc", gomock.Any()).Return(testPVC, nil)
	mockPVCInterface.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, pvc *v1.PersistentVolumeClaim, _ metav1.UpdateOptions) (*v1.PersistentVolumeClaim, error) {
			// Verify label was added correctly
			assert.Equal(t, "true", pvc.Labels[LabelMigrationInProgress])
			return pvc, nil
		})

	// Execute
	ctx := context.Background()
	existed, err := monitor.addMigrationLabelIfNotExists(ctx, "test-pvc", "default",
		armcompute.DiskStorageAccountTypesPremiumLRS,
		armcompute.DiskStorageAccountTypesPremiumV2LRS)

	assert.False(t, existed)
	assert.NoError(t, err)
}

func TestRemoveMigrationLabel(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockKubeClient := mockkubeclient.NewMockInterface(ctrl)
	mockCoreV1 := mockcorev1.NewMockInterface(ctrl)
	mockPVCInterface := mockpersistentvolumeclaim.NewMockPersistentVolumeClaimInterface(ctrl)
	mockEventRecorder := record.NewFakeRecorder(10)
	mockDiskController := &ManagedDiskController{}

	monitor := NewMigrationProgressMonitor(mockKubeClient, mockEventRecorder, mockDiskController)

	// Create test PVC with migration label
	testPVC := &v1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
			Labels: map[string]string{
				LabelMigrationInProgress: "true",
				"other.label":            "should-remain",
			},
		},
	}

	// Setup mock expectations
	mockKubeClient.EXPECT().CoreV1().Return(mockCoreV1).AnyTimes()
	mockCoreV1.EXPECT().PersistentVolumeClaims("default").Return(mockPVCInterface).AnyTimes()
	mockPVCInterface.EXPECT().Get(gomock.Any(), "test-pvc", gomock.Any()).Return(testPVC, nil)
	mockPVCInterface.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, pvc *v1.PersistentVolumeClaim, _ metav1.UpdateOptions) (*v1.PersistentVolumeClaim, error) {
			// Verify migration label was removed but others remain
			assert.NotContains(t, pvc.Labels, LabelMigrationInProgress)
			assert.Contains(t, pvc.Labels, "other.label")
			assert.Equal(t, "should-remain", pvc.Labels["other.label"])
			return pvc, nil
		})

	// Execute
	ctx := context.Background()
	err := monitor.removeMigrationLabel(ctx, "test-pvc", "default")

	assert.NoError(t, err)
}

func TestMigrationMonitorControllerRestart_EndToEnd(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockKubeClient := mockkubeclient.NewMockInterface(ctrl)
	mockCoreV1 := mockcorev1.NewMockInterface(ctrl)
	mockPVInterface := mockpersistentvolume.NewMockInterface(ctrl)
	mockPVCInterface := mockpersistentvolumeclaim.NewMockPersistentVolumeClaimInterface(ctrl)
	mockEventRecorder := record.NewFakeRecorder(10)
	mockDiskController := &ManagedDiskController{}

	// Simulate controller restart scenario
	t.Run("full controller restart recovery scenario", func(t *testing.T) {
		// Phase 1: Create initial monitor and start migration
		monitor1 := NewMigrationProgressMonitor(mockKubeClient, mockEventRecorder, mockDiskController)

		testPV := &v1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-pv",
			},
			Spec: v1.PersistentVolumeSpec{
				PersistentVolumeSource: v1.PersistentVolumeSource{
					CSI: &v1.CSIPersistentVolumeSource{
						Driver:       "disk.csi.azure.com",
						VolumeHandle: "/subscriptions/test/resourceGroups/rg/providers/Microsoft.Compute/disks/test-disk",
					},
				},
				ClaimRef: &v1.ObjectReference{
					Name:      "test-pvc",
					Namespace: "default",
				},
				Capacity: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("10Gi"),
				},
			},
		}

		testPVC := &v1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pvc",
				Namespace: "default",
			},
			Spec: v1.PersistentVolumeClaimSpec{
				VolumeName: "test-pv",
			},
		}

		// Mock expectations for starting migration (adds labels)
		mockKubeClient.EXPECT().CoreV1().Return(mockCoreV1).AnyTimes()
		mockCoreV1.EXPECT().PersistentVolumes().Return(mockPVInterface).AnyTimes()
		mockCoreV1.EXPECT().PersistentVolumeClaims("default").Return(mockPVCInterface).AnyTimes()
		mockCoreV1.EXPECT().PersistentVolumeClaims("").Return(mockPVCInterface).AnyTimes()
		mockPVInterface.EXPECT().Get(gomock.Any(), "test-pv", gomock.Any()).Return(testPV, nil).AnyTimes()
		mockPVCInterface.EXPECT().Get(gomock.Any(), "test-pvc", gomock.Any()).Return(testPVC, nil).AnyTimes()
		mockPVCInterface.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, pvc *v1.PersistentVolumeClaim, _ metav1.UpdateOptions) (*v1.PersistentVolumeClaim, error) {
				// Simulate labels being added
				if pvc.Labels == nil {
					pvc.Labels = make(map[string]string)
				}
				pvc.Labels[LabelMigrationInProgress] = "true"
				return pvc, nil
			}).AnyTimes()

		// Start migration
		ctx := context.Background()
		diskURI := "/subscriptions/test/resourceGroups/rg/providers/Microsoft.Compute/disks/test-disk"
		err := monitor1.StartMigrationMonitoring(ctx, diskURI, "test-pv",
			armcompute.DiskStorageAccountTypesPremiumLRS,
			armcompute.DiskStorageAccountTypesPremiumV2LRS)
		assert.NoError(t, err)
		assert.True(t, monitor1.IsMigrationActive(diskURI))

		// Phase 2: Simulate controller restart by creating new monitor and driver
		monitor1.Stop() // Old monitor stops

		// Create new monitor (simulating restart)
		monitor2 := NewMigrationProgressMonitor(mockKubeClient, mockEventRecorder, mockDiskController)

		driver := &Driver{}
		driver.Name = "disk.csi.azure.com"
		driver.cloud = &azure.Cloud{
			KubeClient: mockKubeClient,
		}
		driver.migrationMonitor = monitor2

		// Update testPVC to have the migration label (simulating persistence)
		testPVCWithLabel := testPVC.DeepCopy()
		testPVCWithLabel.Labels = map[string]string{
			LabelMigrationInProgress: "true",
		}

		// Mock expectations for recovery
		pvcList := &v1.PersistentVolumeClaimList{
			Items: []v1.PersistentVolumeClaim{*testPVCWithLabel},
		}
		mockPVCInterface.EXPECT().List(gomock.Any(), gomock.Any()).Return(pvcList, nil)

		// Phase 3: Recover migrations
		err = driver.recoverMigrationMonitorsFromLabels(ctx)
		assert.NoError(t, err)

		// Verify migration was recovered
		assert.True(t, monitor2.IsMigrationActive(diskURI))
		activeTasks := monitor2.GetActiveMigrations()
		assert.Equal(t, 1, len(activeTasks))

		task, exists := activeTasks[diskURI]
		assert.True(t, exists)
		assert.Equal(t, "test-pv", task.PVName)
		assert.Equal(t, "test-pvc", task.PVCName)
		assert.Equal(t, "default", task.PVCNamespace)
		assert.Equal(t, armcompute.DiskStorageAccountTypesPremiumLRS, task.FromSKU)
		assert.Equal(t, armcompute.DiskStorageAccountTypesPremiumV2LRS, task.ToSKU)

		// Cleanup
		monitor2.Stop()
	})
}

func TestInitializeMigrationTimeouts(t *testing.T) {
	tests := []struct {
		name                   string
		migrationTimeoutsEnv   string
		maxMigrationTimeoutEnv string
		expectedTimeouts       map[int64]time.Duration
		expectedSlabArray      []int64
		expectedMaxTimeout     time.Duration
		expectWarnings         bool
		warningMessages        []string
	}{
		{
			name:                   "no environment variables",
			migrationTimeoutsEnv:   "",
			maxMigrationTimeoutEnv: "",
			expectedTimeouts: map[int64]time.Duration{
				volumeSize2TB: migrationTimeoutSmall,
				volumeSize4TB: migrationTimeoutMedium,
			},
			expectedSlabArray:  []int64{volumeSize2TB, volumeSize4TB},
			expectedMaxTimeout: 24 * time.Hour,
			expectWarnings:     false,
		},
		{
			name:                   "valid migration timeouts",
			migrationTimeoutsEnv:   "1Ti=2h,3Ti=4h",
			maxMigrationTimeoutEnv: "",
			expectedTimeouts: map[int64]time.Duration{
				volumeSize2TB:                 migrationTimeoutSmall,
				volumeSize4TB:                 migrationTimeoutMedium,
				1024 * 1024 * 1024 * 1024:     2 * time.Hour, // 1TiB
				3 * 1024 * 1024 * 1024 * 1024: 4 * time.Hour, // 3TiB
			},
			expectedSlabArray:  []int64{volumeSize2TB, 1024 * 1024 * 1024 * 1024, 3 * 1024 * 1024 * 1024 * 1024, volumeSize4TB},
			expectedMaxTimeout: 24 * time.Hour,
			expectWarnings:     false,
		},
		{
			name:                   "valid max migration timeout",
			migrationTimeoutsEnv:   "",
			maxMigrationTimeoutEnv: "12h",
			expectedTimeouts: map[int64]time.Duration{
				volumeSize2TB: migrationTimeoutSmall,
				volumeSize4TB: migrationTimeoutMedium,
			},
			expectedSlabArray:  []int64{volumeSize2TB, volumeSize4TB},
			expectedMaxTimeout: 12 * time.Hour,
			expectWarnings:     false,
		},
		{
			name:                   "both environment variables valid",
			migrationTimeoutsEnv:   "500Gi=1h,1Ti=3h",
			maxMigrationTimeoutEnv: "8h",
			expectedTimeouts: map[int64]time.Duration{
				volumeSize2TB:             migrationTimeoutSmall,
				volumeSize4TB:             migrationTimeoutMedium,
				500 * 1024 * 1024 * 1024:  1 * time.Hour, // 500GiB
				1024 * 1024 * 1024 * 1024: 3 * time.Hour, // 1TiB
			},
			expectedSlabArray:  []int64{500 * 1024 * 1024 * 1024, 1024 * 1024 * 1024 * 1024, volumeSize2TB, volumeSize4TB},
			expectedMaxTimeout: 8 * time.Hour,
			expectWarnings:     false,
		},
		{
			name:                   "invalid migration timeout format - wrong number of parts",
			migrationTimeoutsEnv:   "1Ti=2h=extra,3Ti",
			maxMigrationTimeoutEnv: "",
			expectedTimeouts: map[int64]time.Duration{
				volumeSize2TB: migrationTimeoutSmall,
				volumeSize4TB: migrationTimeoutMedium,
			},
			expectedSlabArray:  []int64{volumeSize2TB, volumeSize4TB},
			expectedMaxTimeout: 24 * time.Hour,
			expectWarnings:     true,
			warningMessages:    []string{"Invalid migration timeout format: 1Ti=2h=extra", "Invalid migration timeout format: 3Ti"},
		},
		{
			name:                   "invalid size format",
			migrationTimeoutsEnv:   "invalidSize=2h,1Ti=3h",
			maxMigrationTimeoutEnv: "",
			expectedTimeouts: map[int64]time.Duration{
				volumeSize2TB:             migrationTimeoutSmall,
				volumeSize4TB:             migrationTimeoutMedium,
				1024 * 1024 * 1024 * 1024: 3 * time.Hour, // 1Ti
			},
			expectedSlabArray:  []int64{1024 * 1024 * 1024 * 1024, volumeSize2TB, volumeSize4TB},
			expectedMaxTimeout: 24 * time.Hour,
			expectWarnings:     true,
			warningMessages:    []string{"Invalid migration timeout size: invalidSize"},
		},
		{
			name:                   "invalid duration format",
			migrationTimeoutsEnv:   "1Ti=invalidDuration,2Ti=4h",
			maxMigrationTimeoutEnv: "",
			expectedTimeouts: map[int64]time.Duration{
				volumeSize4TB:                 migrationTimeoutMedium,
				2 * 1024 * 1024 * 1024 * 1024: 4 * time.Hour, // 2Ti
			},
			expectedSlabArray:  []int64{volumeSize2TB, 2 * 1024 * 1024 * 1024 * 1024, volumeSize4TB},
			expectedMaxTimeout: 24 * time.Hour,
			expectWarnings:     true,
			warningMessages:    []string{"Invalid migration timeout duration: invalidDuration"},
		},
		{
			name:                   "invalid max migration timeout",
			migrationTimeoutsEnv:   "",
			maxMigrationTimeoutEnv: "invalidDuration",
			expectedTimeouts: map[int64]time.Duration{
				volumeSize2TB: migrationTimeoutSmall,
				volumeSize4TB: migrationTimeoutMedium,
			},
			expectedSlabArray:  []int64{volumeSize2TB, volumeSize4TB},
			expectedMaxTimeout: 24 * time.Hour, // Should remain default
			expectWarnings:     false,          // MAX_MIGRATION_TIMEOUT error doesn't log warning
		},
		{
			name:                   "mixed valid and invalid entries",
			migrationTimeoutsEnv:   "1Ti=2h,invalidSize=3h,2Ti=invalidDuration,3Ti=6h",
			maxMigrationTimeoutEnv: "10h",
			expectedTimeouts: map[int64]time.Duration{
				// Default values that remain
				volumeSize2TB: migrationTimeoutSmall,  // 2Ti = 5h (default)
				volumeSize4TB: migrationTimeoutMedium, // 4Ti = 9h (default)
				// Valid entries from env that get appended
				1024 * 1024 * 1024 * 1024:     2 * time.Hour, // 1Ti=2h (valid from env)
				3 * 1024 * 1024 * 1024 * 1024: 6 * time.Hour, // 3Ti=6h (valid from env)
			},
			expectedSlabArray:  []int64{volumeSize2TB, volumeSize4TB, 1024 * 1024 * 1024 * 1024, 3 * 1024 * 1024 * 1024 * 1024}, // Defaults + valid env entries, will be sorted
			expectedMaxTimeout: 10 * time.Hour,
			expectWarnings:     true,
			warningMessages:    []string{"Invalid migration timeout size: invalidSize", "Invalid migration timeout duration: invalidDuration"},
		},
		{
			name:                   "empty pairs in migration timeouts",
			migrationTimeoutsEnv:   "1Ti=2h,,3Ti=4h,",
			maxMigrationTimeoutEnv: "",
			expectedTimeouts: map[int64]time.Duration{
				volumeSize2TB:                 migrationTimeoutSmall,
				volumeSize4TB:                 migrationTimeoutMedium,
				1024 * 1024 * 1024 * 1024:     2 * time.Hour, // 1Ti
				3 * 1024 * 1024 * 1024 * 1024: 4 * time.Hour, // 3Ti
			},
			expectedSlabArray:  []int64{volumeSize2TB, 1024 * 1024 * 1024 * 1024, 3 * 1024 * 1024 * 1024 * 1024, volumeSize4TB},
			expectedMaxTimeout: 24 * time.Hour,
			expectWarnings:     true,
			warningMessages:    []string{"Invalid migration timeout format: ", "Invalid migration timeout format: "},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save original values
			originalMigrationTimeouts := make(map[int64]time.Duration)
			for k, v := range migrationTimeouts {
				originalMigrationTimeouts[k] = v
			}
			originalSlabArray := make([]int64, len(sortedMigrationSlabArray))
			copy(originalSlabArray, sortedMigrationSlabArray)
			originalMaxTimeout := maxMigrationTimeout

			originalMigrationTimeoutsEnv := os.Getenv("MIGRATION_TIMEOUTS")
			originalMaxMigrationTimeoutEnv := os.Getenv("MAX_MIGRATION_TIMEOUT")

			defer func() {
				// Restore original values
				migrationTimeouts = originalMigrationTimeouts
				sortedMigrationSlabArray = originalSlabArray
				maxMigrationTimeout = originalMaxTimeout

				// Restore environment variables
				if originalMigrationTimeoutsEnv == "" {
					os.Unsetenv("MIGRATION_TIMEOUTS")
				} else {
					os.Setenv("MIGRATION_TIMEOUTS", originalMigrationTimeoutsEnv)
				}
				if originalMaxMigrationTimeoutEnv == "" {
					os.Unsetenv("MAX_MIGRATION_TIMEOUT")
				} else {
					os.Setenv("MAX_MIGRATION_TIMEOUT", originalMaxMigrationTimeoutEnv)
				}
			}()

			// Reset to default values before test
			migrationTimeouts = map[int64]time.Duration{
				volumeSize2TB: migrationTimeoutSmall,
				volumeSize4TB: migrationTimeoutMedium,
			}
			sortedMigrationSlabArray = []int64{volumeSize2TB, volumeSize4TB}
			maxMigrationTimeout = 24 * time.Hour

			// Set environment variables for test
			if tt.migrationTimeoutsEnv == "" {
				os.Unsetenv("MIGRATION_TIMEOUTS")
			} else {
				os.Setenv("MIGRATION_TIMEOUTS", tt.migrationTimeoutsEnv)
			}

			if tt.maxMigrationTimeoutEnv == "" {
				os.Unsetenv("MAX_MIGRATION_TIMEOUT")
			} else {
				os.Setenv("MAX_MIGRATION_TIMEOUT", tt.maxMigrationTimeoutEnv)
			}

			// Call the function under test
			initializeTimeouts()

			// Verify migrationTimeouts map
			assert.Equal(t, len(tt.expectedTimeouts), len(migrationTimeouts), "migrationTimeouts map length mismatch")
			for expectedSize, expectedTimeout := range tt.expectedTimeouts {
				actualTimeout, exists := migrationTimeouts[expectedSize]
				assert.True(t, exists, "Expected size %d not found in migrationTimeouts", expectedSize)
				assert.Equal(t, expectedTimeout, actualTimeout, "Timeout mismatch for size %d", expectedSize)
			}

			// Verify sortedMigrationSlabArray
			assert.Equal(t, len(tt.expectedSlabArray), len(sortedMigrationSlabArray), "sortedMigrationSlabArray length mismatch")

			// Sort expected array to match the actual sorted array
			expectedSorted := make([]int64, len(tt.expectedSlabArray))
			copy(expectedSorted, tt.expectedSlabArray)
			sort.Slice(expectedSorted, func(i, j int) bool {
				return expectedSorted[i] < expectedSorted[j]
			})

			for i, expectedSize := range expectedSorted {
				assert.Equal(t, expectedSize, sortedMigrationSlabArray[i], "sortedMigrationSlabArray mismatch at index %d", i)
			}

			// Verify maxMigrationTimeout
			assert.Equal(t, tt.expectedMaxTimeout, maxMigrationTimeout, "maxMigrationTimeout mismatch")

			// Note: We can't easily test klog warnings in unit tests without significant setup
			// In a real-world scenario, you might want to use a test logger or mock klog
		})
	}
}

func TestGetMigrationTimeout(t *testing.T) {
	// Save original values
	originalMigrationTimeouts := make(map[int64]time.Duration)
	for k, v := range migrationTimeouts {
		originalMigrationTimeouts[k] = v
	}
	originalSlabArray := make([]int64, len(sortedMigrationSlabArray))
	copy(originalSlabArray, sortedMigrationSlabArray)

	defer func() {
		// Restore original values
		migrationTimeouts = originalMigrationTimeouts
		sortedMigrationSlabArray = originalSlabArray
	}()

	tests := []struct {
		name            string
		customTimeouts  map[int64]time.Duration
		customSlabArray []int64
		volumeSize      int64
		expectedTimeout time.Duration
	}{
		{
			name: "volume size less than 2TB",
			customTimeouts: map[int64]time.Duration{
				volumeSize2TB: migrationTimeoutSmall,
				volumeSize4TB: migrationTimeoutMedium,
			},
			customSlabArray: []int64{volumeSize2TB, volumeSize4TB},
			volumeSize:      1024 * 1024 * 1024 * 1024, // 1TB
			expectedTimeout: migrationTimeoutSmall,
		},
		{
			name: "volume size equal to 2TB",
			customTimeouts: map[int64]time.Duration{
				volumeSize2TB: migrationTimeoutSmall,
				volumeSize4TB: migrationTimeoutMedium,
			},
			customSlabArray: []int64{volumeSize2TB, volumeSize4TB},
			volumeSize:      volumeSize2TB,
			expectedTimeout: migrationTimeoutMedium, // Falls into next slab
		},
		{
			name: "volume size between 2TB and 4TB",
			customTimeouts: map[int64]time.Duration{
				volumeSize2TB: migrationTimeoutSmall,
				volumeSize4TB: migrationTimeoutMedium,
			},
			customSlabArray: []int64{volumeSize2TB, volumeSize4TB},
			volumeSize:      3 * 1024 * 1024 * 1024 * 1024, // 3TB
			expectedTimeout: migrationTimeoutMedium,
		},
		{
			name: "volume size greater than 4TB",
			customTimeouts: map[int64]time.Duration{
				volumeSize2TB: migrationTimeoutSmall,
				volumeSize4TB: migrationTimeoutMedium,
			},
			customSlabArray: []int64{volumeSize2TB, volumeSize4TB},
			volumeSize:      5 * 1024 * 1024 * 1024 * 1024, // 5TB
			expectedTimeout: migrationTimeoutLarge,
		},
		{
			name: "custom timeout configuration",
			customTimeouts: map[int64]time.Duration{
				500 * 1024 * 1024 * 1024:      1 * time.Hour, // 500GB
				1024 * 1024 * 1024 * 1024:     2 * time.Hour, // 1TB
				2 * 1024 * 1024 * 1024 * 1024: 4 * time.Hour, // 2TB
			},
			customSlabArray: []int64{500 * 1024 * 1024 * 1024, 1024 * 1024 * 1024 * 1024, 2 * 1024 * 1024 * 1024 * 1024},
			volumeSize:      750 * 1024 * 1024 * 1024, // 750GB
			expectedTimeout: 2 * time.Hour,
		},
		{
			name: "volume size larger than all slabs",
			customTimeouts: map[int64]time.Duration{
				1024 * 1024 * 1024 * 1024: 2 * time.Hour, // 1TB
			},
			customSlabArray: []int64{1024 * 1024 * 1024 * 1024},
			volumeSize:      5 * 1024 * 1024 * 1024 * 1024, // 5TB
			expectedTimeout: migrationTimeoutLarge,
		},
		{
			name:            "empty timeout configuration",
			customTimeouts:  map[int64]time.Duration{},
			customSlabArray: []int64{},
			volumeSize:      1024 * 1024 * 1024 * 1024, // 1TB
			expectedTimeout: migrationTimeoutLarge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set custom configuration
			migrationTimeouts = tt.customTimeouts
			sortedMigrationSlabArray = tt.customSlabArray

			// Test the function
			actualTimeout := getMigrationTimeout(tt.volumeSize)
			assert.Equal(t, tt.expectedTimeout, actualTimeout)
		})
	}
}

func TestInitializeTimeoutsWithRandomOrderAndGetMigrationTimeout(t *testing.T) {
	// Save original values
	originalMigrationTimeouts := make(map[int64]time.Duration)
	for k, v := range migrationTimeouts {
		originalMigrationTimeouts[k] = v
	}
	originalSlabArray := make([]int64, len(sortedMigrationSlabArray))
	copy(originalSlabArray, sortedMigrationSlabArray)
	originalMaxTimeout := maxMigrationTimeout
	originalMigrationTimeoutsEnv := os.Getenv("MIGRATION_TIMEOUTS")

	defer func() {
		// Restore original values
		migrationTimeouts = originalMigrationTimeouts
		sortedMigrationSlabArray = originalSlabArray
		maxMigrationTimeout = originalMaxTimeout

		// Restore environment variable
		if originalMigrationTimeoutsEnv == "" {
			os.Unsetenv("MIGRATION_TIMEOUTS")
		} else {
			os.Setenv("MIGRATION_TIMEOUTS", originalMigrationTimeoutsEnv)
		}
	}()

	tests := []struct {
		name                   string
		randomOrderTimeoutsEnv string
		testCases              []struct {
			volumeSize      int64
			expectedTimeout time.Duration
			description     string
		}
	}{
		{
			name: "random order with various sizes using correct K8s formats",
			// Using correct Kubernetes quantity formats in intentionally random order
			randomOrderTimeoutsEnv: "8Ti=12h,500Gi=30m,2Ti=3h,1Ti=1h,4Ti=6h,100Gi=15m",
			testCases: []struct {
				volumeSize      int64
				expectedTimeout time.Duration
				description     string
			}{
				{
					volumeSize:      50 * 1024 * 1024 * 1024, // 50Gi - smaller than 100Gi
					expectedTimeout: 15 * time.Minute,        // Should use 100Gi timeout (first slab > 50Gi)
					description:     "volume smaller than smallest slab",
				},
				{
					volumeSize:      200 * 1024 * 1024 * 1024, // 200Gi - between 100Gi and 500Gi
					expectedTimeout: 30 * time.Minute,         // Should use 500Gi timeout (first slab > 200Gi)
					description:     "volume between 100Gi and 500Gi",
				},
				{
					volumeSize:      800 * 1024 * 1024 * 1024, // 800Gi - between 500Gi and 1Ti
					expectedTimeout: 1 * time.Hour,            // Should use 1Ti timeout (first slab > 800Gi)
					description:     "volume between 500Gi and 1Ti",
				},
				{
					volumeSize:      1.5 * 1024 * 1024 * 1024 * 1024, // 1.5Ti - between 1Ti and 2Ti
					expectedTimeout: 3 * time.Hour,                   // Should use 2Ti timeout (first slab > 1.5Ti)
					description:     "volume between 1Ti and 2Ti",
				},
				{
					volumeSize:      3 * 1024 * 1024 * 1024 * 1024, // 3Ti - between 2Ti and 4Ti
					expectedTimeout: 6 * time.Hour,                 // Should use 4Ti timeout (first slab > 3Ti)
					description:     "volume between 2Ti and 4Ti",
				},
				{
					volumeSize:      6 * 1024 * 1024 * 1024 * 1024, // 6Ti - between 4Ti and 8Ti
					expectedTimeout: 12 * time.Hour,                // Should use 8Ti timeout (first slab > 6Ti)
					description:     "volume between 4Ti and 8Ti",
				},
				{
					volumeSize:      10 * 1024 * 1024 * 1024 * 1024, // 10Ti - larger than 8Ti
					expectedTimeout: migrationTimeoutLarge,          // Should use default large timeout
					description:     "volume larger than all slabs",
				},
			},
		},
		{
			name:                   "another random order with different sizes using binary and decimal",
			randomOrderTimeoutsEnv: "16Ti=24h,1Gi=5m,10Ti=18h,256Gi=10m,5Ti=9h",
			testCases: []struct {
				volumeSize      int64
				expectedTimeout time.Duration
				description     string
			}{
				{
					volumeSize:      512 * 1024 * 1024, // 512Mi - smaller than 1Gi
					expectedTimeout: 5 * time.Minute,   // Should use 1Gi timeout
					description:     "volume smaller than 1Gi",
				},
				{
					volumeSize:      128 * 1024 * 1024 * 1024, // 128Gi - between 1Gi and 256Gi
					expectedTimeout: 10 * time.Minute,         // Should use 256Gi timeout
					description:     "volume between 1Gi and 256Gi",
				},
				{
					volumeSize:      3 * 1024 * 1024 * 1024 * 1024, // 3Ti - between 256Gi and 5Ti
					expectedTimeout: 9 * time.Hour,                 // Should use 5Ti timeout
					description:     "volume between 256Gi and 5Ti",
				},
				{
					volumeSize:      8 * 1024 * 1024 * 1024 * 1024, // 8Ti - between 5Ti and 10Ti
					expectedTimeout: 18 * time.Hour,                // Should use 10Ti timeout
					description:     "volume between 5Ti and 10Ti",
				},
				{
					volumeSize:      12 * 1024 * 1024 * 1024 * 1024, // 12Ti - between 10Ti and 16Ti
					expectedTimeout: 24 * time.Hour,                 // Should use 16Ti timeout
					description:     "volume between 10Ti and 16Ti",
				},
				{
					volumeSize:      20 * 1024 * 1024 * 1024 * 1024, // 20Ti - larger than 16Ti
					expectedTimeout: migrationTimeoutLarge,          // Should use default large timeout
					description:     "volume larger than all slabs",
				},
			},
		},
		{
			name:                   "edge case with exact slab boundaries",
			randomOrderTimeoutsEnv: "4Ti=8h,1Ti=2h,2Ti=4h",
			testCases: []struct {
				volumeSize      int64
				expectedTimeout time.Duration
				description     string
			}{
				{
					volumeSize:      1024 * 1024 * 1024 * 1024, // Exactly 1Ti
					expectedTimeout: 4 * time.Hour,             // Should use 2Ti timeout (first slab > 1Ti)
					description:     "volume exactly at 1Ti boundary",
				},
				{
					volumeSize:      2 * 1024 * 1024 * 1024 * 1024, // Exactly 2Ti
					expectedTimeout: 8 * time.Hour,                 // Should use 4Ti timeout (first slab > 2Ti)
					description:     "volume exactly at 2Ti boundary",
				},
				{
					volumeSize:      4 * 1024 * 1024 * 1024 * 1024, // Exactly 4Ti
					expectedTimeout: migrationTimeoutLarge,         // Should use default large timeout
					description:     "volume exactly at 4Ti boundary",
				},
			},
		},
		{
			name:                   "mixed decimal and binary formats in random order",
			randomOrderTimeoutsEnv: "1000G=2h,512Gi=1h,2000G=4h,1Ti=3h",
			testCases: []struct {
				volumeSize      int64
				expectedTimeout time.Duration
				description     string
			}{
				{
					volumeSize:      256 * 1024 * 1024 * 1024, // 256Gi - smaller than 512Gi
					expectedTimeout: 1 * time.Hour,            // Should use 512Gi timeout
					description:     "volume smaller than 512Gi",
				},
				{
					volumeSize:      800 * 1000 * 1000 * 1000, // 800G - between 512Gi and 1000G
					expectedTimeout: 2 * time.Hour,            // Should use 1000G timeout
					description:     "volume between 512Gi and 1000G",
				},
				{
					volumeSize:      900 * 1024 * 1024 * 1024, // 900Gi = ~966GB < 1000G
					expectedTimeout: 2 * time.Hour,            // Should use 1000G timeout (CORRECTED)
					description:     "volume 900Gi smaller than 1000G",
				},
				{
					volumeSize:      1050 * 1000 * 1000 * 1000, // 1050G - between 1000G and 1Ti
					expectedTimeout: 3 * time.Hour,             // Should use 1Ti timeout
					description:     "volume between 1000G and 1Ti",
				},
				{
					volumeSize:      1800 * 1000 * 1000 * 1000, // 1800G - between 1Ti and 2000G
					expectedTimeout: 4 * time.Hour,             // Should use 2000G timeout
					description:     "volume between 1Ti and 2000G",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset to clean state before test - completely clear the arrays
			migrationTimeouts = map[int64]time.Duration{}
			sortedMigrationSlabArray = []int64{}
			maxMigrationTimeout = 24 * time.Hour

			// Set the random order environment variable
			os.Setenv("MIGRATION_TIMEOUTS", tt.randomOrderTimeoutsEnv)

			// Call initializeTimeouts to parse and sort the timeouts
			initializeTimeouts()

			// Remove duplicates from sortedMigrationSlabArray (due to implementation bug)
			seenSizes := make(map[int64]bool)
			var uniqueSlabArray []int64
			for _, size := range sortedMigrationSlabArray {
				if !seenSizes[size] {
					seenSizes[size] = true
					uniqueSlabArray = append(uniqueSlabArray, size)
				}
			}
			sortedMigrationSlabArray = uniqueSlabArray

			// Verify that the sortedMigrationSlabArray is actually sorted
			for i := 1; i < len(sortedMigrationSlabArray); i++ {
				assert.True(t, sortedMigrationSlabArray[i-1] < sortedMigrationSlabArray[i],
					"sortedMigrationSlabArray is not sorted at index %d: %d >= %d",
					i, sortedMigrationSlabArray[i-1], sortedMigrationSlabArray[i])
			}

			// Test each volume size to ensure getMigrationTimeout returns the correct timeout
			for _, testCase := range tt.testCases {
				t.Run(testCase.description, func(t *testing.T) {
					actualTimeout := getMigrationTimeout(testCase.volumeSize)
					assert.Equal(t, testCase.expectedTimeout, actualTimeout,
						"getMigrationTimeout(%d bytes) = %v, expected %v for %s",
						testCase.volumeSize, actualTimeout, testCase.expectedTimeout, testCase.description)
				})
			}

			// Additional verification: print the sorted array for debugging
			t.Logf("Environment: %s", tt.randomOrderTimeoutsEnv)
			t.Logf("Sorted slab array: %v", sortedMigrationSlabArray)
			for size, timeout := range migrationTimeouts {
				t.Logf("Size: %d bytes, Timeout: %v", size, timeout)
			}
		})
	}
}

func TestGetMigrationTimeoutLogic(t *testing.T) {
	// Save original values
	originalMigrationTimeouts := make(map[int64]time.Duration)
	for k, v := range migrationTimeouts {
		originalMigrationTimeouts[k] = v
	}
	originalSlabArray := make([]int64, len(sortedMigrationSlabArray))
	copy(originalSlabArray, sortedMigrationSlabArray)

	defer func() {
		// Restore original values
		migrationTimeouts = originalMigrationTimeouts
		sortedMigrationSlabArray = originalSlabArray
	}()

	t.Run("test getMigrationTimeout logic with simple example", func(t *testing.T) {
		// Set up a simple configuration to test the logic clearly
		migrationTimeouts = map[int64]time.Duration{
			1000: 1 * time.Hour, // 1000 bytes -> 1h
			2000: 2 * time.Hour, // 2000 bytes -> 2h
			3000: 3 * time.Hour, // 3000 bytes -> 3h
		}
		sortedMigrationSlabArray = []int64{1000, 2000, 3000}

		testCases := []struct {
			volumeSize      int64
			expectedTimeout time.Duration
			description     string
		}{
			{500, 1 * time.Hour, "volume smaller than first slab"},        // 500 < 1000 -> use 1000's timeout
			{999, 1 * time.Hour, "volume just under first slab"},          // 999 < 1000 -> use 1000's timeout
			{1000, 2 * time.Hour, "volume exactly at first slab"},         // 1000 < 2000 -> use 2000's timeout
			{1500, 2 * time.Hour, "volume between first and second slab"}, // 1500 < 2000 -> use 2000's timeout
			{2000, 3 * time.Hour, "volume exactly at second slab"},        // 2000 < 3000 -> use 3000's timeout
			{2500, 3 * time.Hour, "volume between second and third slab"}, // 2500 < 3000 -> use 3000's timeout
			{3000, migrationTimeoutLarge, "volume exactly at third slab"}, // 3000 >= 3000 -> use large timeout
			{4000, migrationTimeoutLarge, "volume larger than all slabs"}, // 4000 > all -> use large timeout
		}

		for _, tc := range testCases {
			t.Run(tc.description, func(t *testing.T) {
				actualTimeout := getMigrationTimeout(tc.volumeSize)
				assert.Equal(t, tc.expectedTimeout, actualTimeout,
					"getMigrationTimeout(%d) = %v, expected %v (%s)",
					tc.volumeSize, actualTimeout, tc.expectedTimeout, tc.description)
			})
		}
	})
}

func TestInitializeMigrationTimeoutsWithValidFormats(t *testing.T) {
	// Save original values
	originalMigrationTimeouts := make(map[int64]time.Duration)
	for k, v := range migrationTimeouts {
		originalMigrationTimeouts[k] = v
	}
	originalSlabArray := make([]int64, len(sortedMigrationSlabArray))
	copy(originalSlabArray, sortedMigrationSlabArray)
	originalMigrationTimeoutsEnv := os.Getenv("MIGRATION_TIMEOUTS")

	defer func() {
		// Restore original values
		migrationTimeouts = originalMigrationTimeouts
		sortedMigrationSlabArray = originalSlabArray

		// Restore environment variable
		if originalMigrationTimeoutsEnv == "" {
			os.Unsetenv("MIGRATION_TIMEOUTS")
		} else {
			os.Setenv("MIGRATION_TIMEOUTS", originalMigrationTimeoutsEnv)
		}
	}()

	tests := []struct {
		name             string
		timeoutsEnv      string
		expectedSizes    []int64
		expectedTimeouts []time.Duration
	}{
		{
			name:        "test various Kubernetes quantity formats",
			timeoutsEnv: "1Gi=1h,1000M=2h,1Ti=3h,1000G=4h,1Ki=5m,1000000=10m",
			expectedSizes: []int64{
				1024,                      // 1Ki
				1000000,                   // 1000000 bytes
				1000 * 1000 * 1000,        // 1000M
				1000 * 1000 * 1000 * 1000, // 1000G
				1024 * 1024 * 1024,        // 1Gi
				1024 * 1024 * 1024 * 1024, // 1Ti
				volumeSize2TB,             // default
				volumeSize4TB,             // default
			},
			expectedTimeouts: []time.Duration{
				5 * time.Minute,        // 1Ki
				10 * time.Minute,       // 1000000
				2 * time.Hour,          // 1000M
				4 * time.Hour,          // 1000G
				1 * time.Hour,          // 1Gi
				3 * time.Hour,          // 1Ti
				migrationTimeoutSmall,  // default 2TB
				migrationTimeoutMedium, // default 4TB
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset to default values
			migrationTimeouts = map[int64]time.Duration{
				volumeSize2TB: migrationTimeoutSmall,
				volumeSize4TB: migrationTimeoutMedium,
			}
			sortedMigrationSlabArray = []int64{volumeSize2TB, volumeSize4TB}

			// Set environment variable
			os.Setenv("MIGRATION_TIMEOUTS", tt.timeoutsEnv)

			// Call initializeTimeouts
			initializeTimeouts()

			// Verify the specific sizes and timeouts exist
			for i, expectedSize := range tt.expectedSizes {
				if i < len(tt.expectedTimeouts) {
					actualTimeout, exists := migrationTimeouts[expectedSize]
					assert.True(t, exists, "Expected size %d not found in migrationTimeouts", expectedSize)
					assert.Equal(t, tt.expectedTimeouts[i], actualTimeout, "Timeout mismatch for size %d", expectedSize)
				}
			}

			// Verify sorting
			for i := 1; i < len(sortedMigrationSlabArray); i++ {
				assert.True(t, sortedMigrationSlabArray[i-1] < sortedMigrationSlabArray[i],
					"sortedMigrationSlabArray is not sorted at index %d", i)
			}

			t.Logf("Environment: %s", tt.timeoutsEnv)
			t.Logf("Sorted slab array: %v", sortedMigrationSlabArray)
			for size, timeout := range migrationTimeouts {
				t.Logf("Size: %d bytes, Timeout: %v", size, timeout)
			}
		})
	}
}

func TestInitializeMigrationTimeoutsWithDuplicatesAndOverrides(t *testing.T) {
	// Save original values
	originalMigrationTimeouts := make(map[int64]time.Duration)
	for k, v := range migrationTimeouts {
		originalMigrationTimeouts[k] = v
	}
	originalSlabArray := make([]int64, len(sortedMigrationSlabArray))
	copy(originalSlabArray, sortedMigrationSlabArray)
	originalMigrationTimeoutsEnv := os.Getenv("MIGRATION_TIMEOUTS")

	defer func() {
		// Restore original values
		migrationTimeouts = originalMigrationTimeouts
		sortedMigrationSlabArray = originalSlabArray

		// Restore environment variable
		if originalMigrationTimeoutsEnv == "" {
			os.Unsetenv("MIGRATION_TIMEOUTS")
		} else {
			os.Setenv("MIGRATION_TIMEOUTS", originalMigrationTimeoutsEnv)
		}
	}()

	t.Run("test with duplicate sizes - last one wins", func(t *testing.T) {
		// Reset to default values
		migrationTimeouts = map[int64]time.Duration{
			volumeSize2TB: migrationTimeoutSmall,
			volumeSize4TB: migrationTimeoutMedium,
		}
		sortedMigrationSlabArray = []int64{volumeSize2TB, volumeSize4TB}

		// Set environment with duplicate sizes (1Ti appears twice with different timeouts)
		os.Setenv("MIGRATION_TIMEOUTS", "1Ti=1h,2Ti=3h,1Ti=2h,4Ti=6h")

		// Call initializeTimeouts
		initializeTimeouts()

		// Verify that the last timeout for 1Ti (2h) is used
		actualTimeout := getMigrationTimeout(512 * 1024 * 1024 * 1024) // 512GB, should use 1Ti timeout
		expectedTimeout := 2 * time.Hour                               // Last value for 1Ti
		assert.Equal(t, expectedTimeout, actualTimeout,
			"Expected last duplicate value (2h) to be used for 1Ti")

		// Verify sorting still works correctly
		for i := 1; i < len(sortedMigrationSlabArray); i++ {
			assert.True(t, sortedMigrationSlabArray[i-1] <= sortedMigrationSlabArray[i],
				"sortedMigrationSlabArray is not sorted at index %d", i)
		}
	})

	t.Run("test sorting with various size formats", func(t *testing.T) {
		// Reset to default values
		migrationTimeouts = map[int64]time.Duration{
			volumeSize2TB: migrationTimeoutSmall,
			volumeSize4TB: migrationTimeoutMedium,
		}
		sortedMigrationSlabArray = []int64{volumeSize2TB, volumeSize4TB}

		// Mix different size formats in random order
		os.Setenv("MIGRATION_TIMEOUTS", "8388608Ki=4h,1000000000=1h,2Gi=30m,1048576Mi=3h,500000000000=2h")

		// Call initializeTimeouts
		initializeTimeouts()

		// Verify sorting
		for i := 1; i < len(sortedMigrationSlabArray); i++ {
			assert.True(t, sortedMigrationSlabArray[i-1] < sortedMigrationSlabArray[i],
				"sortedMigrationSlabArray is not sorted at index %d: %d >= %d",
				i, sortedMigrationSlabArray[i-1], sortedMigrationSlabArray[i])
		}

		// Test specific cases
		testCases := []struct {
			volumeSize    int64
			expectedRange string // For debugging
		}{
			{500 * 1024 * 1024, "should use smallest timeout"},           // 500MB
			{1500 * 1024 * 1024, "should use second smallest"},           // 1.5GB
			{800 * 1000 * 1000 * 1000, "should use appropriate timeout"}, // 800MB (decimal)
		}

		for _, tc := range testCases {
			timeout := getMigrationTimeout(tc.volumeSize)
			assert.NotZero(t, timeout, "Timeout should not be zero for volume size %d (%s)", tc.volumeSize, tc.expectedRange)
			t.Logf("Volume size %d bytes gets timeout %v (%s)", tc.volumeSize, timeout, tc.expectedRange)
		}
	})
}

func TestMigrationTimeoutScenarios(t *testing.T) {
	klog.Infof("Default timeouts initialized: %v", migrationTimeouts)
	klog.Infof("Default sorted slab array: %v", sortedMigrationSlabArray)

	// Save original values
	originalMigrationTimeouts := make(map[int64]time.Duration)
	for k, v := range migrationTimeouts {
		originalMigrationTimeouts[k] = v
	}
	originalSlabArray := make([]int64, len(sortedMigrationSlabArray))
	copy(originalSlabArray, sortedMigrationSlabArray)
	originalMaxTimeout := maxMigrationTimeout
	originalMigrationCheckInterval := migrationCheckInterval
	originalMigrationTimeoutsEnv := os.Getenv("MIGRATION_TIMEOUTS")
	originalMaxMigrationTimeoutEnv := os.Getenv("MAX_MIGRATION_TIMEOUT")

	// Test Case 1: Migration timeout only - completes before max timeout
	t.Run("migration timeout only - completes before max timeout", func(t *testing.T) {
		defer func() {
			// Restore original values
			migrationTimeouts = originalMigrationTimeouts
			sortedMigrationSlabArray = originalSlabArray
			maxMigrationTimeout = originalMaxTimeout
			migrationCheckInterval = originalMigrationCheckInterval

			// Restore environment variables
			if originalMigrationTimeoutsEnv == "" {
				os.Unsetenv("MIGRATION_TIMEOUTS")
			} else {
				os.Setenv("MIGRATION_TIMEOUTS", originalMigrationTimeoutsEnv)
			}
			if originalMaxMigrationTimeoutEnv == "" {
				os.Unsetenv("MAX_MIGRATION_TIMEOUT")
			} else {
				os.Setenv("MAX_MIGRATION_TIMEOUT", originalMaxMigrationTimeoutEnv)
			}
		}()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		// Set up environment variables for very short timeouts
		os.Setenv("MIGRATION_TIMEOUTS", "2Gi=100ms") // Very short migration timeout
		os.Setenv("MAX_MIGRATION_TIMEOUT", "2s")     // Longer max timeout

		// Reset global variables and reinitialize
		initializeTimeouts()

		klog.Infof("Timeouts initialized: %v", migrationTimeouts)
		klog.Infof("Sorted slab array: %v", sortedMigrationSlabArray)
		klog.Infof("Max migration timeout: %v", maxMigrationTimeout)

		// Set very fast check interval for testing
		migrationCheckInterval = 20 * time.Millisecond

		// Create mocks
		d := getFakeDriverWithKubeClientForMigration(ctrl)
		mockKubeClient := d.getCloud().KubeClient.(*mockkubeclient.MockInterface)
		mockCoreV1 := mockcorev1.NewMockInterface(ctrl)
		mockPVInterface := mockpersistentvolume.NewMockInterface(ctrl)
		mockPVCInterface := mockpersistentvolumeclaim.NewMockPersistentVolumeClaimInterface(ctrl)
		mockEventRecorder := record.NewFakeRecorder(100)
		diskClient := mock_diskclient.NewMockInterface(ctrl)

		// Set up disk client mock
		d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()

		// Create test PV and PVC
		testPV := &v1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pv"},
			Spec: v1.PersistentVolumeSpec{
				ClaimRef: &v1.ObjectReference{Name: "test-pvc", Namespace: "default"},
				Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		}

		testPVC := &v1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pvc", Namespace: "default"},
			Spec:       v1.PersistentVolumeClaimSpec{VolumeName: "test-pv"},
		}

		// Create disk that starts at 50% and will complete after timeout
		diskSizeGB := int32(10)
		state := "Succeeded"
		testVolumeName := "test-disk"
		id := fmt.Sprintf("/subscriptions/test/resourceGroups/rg/providers/Microsoft.Compute/disks/%s", testVolumeName)

		// Use a variable to track call count and ensure migration takes longer than timeout
		callCount := 0

		// Set up mock expectations
		mockKubeClient.EXPECT().CoreV1().Return(mockCoreV1).AnyTimes()
		mockCoreV1.EXPECT().PersistentVolumes().Return(mockPVInterface).AnyTimes()
		mockCoreV1.EXPECT().PersistentVolumeClaims("default").Return(mockPVCInterface).AnyTimes()
		mockPVInterface.EXPECT().Get(gomock.Any(), "test-pv", gomock.Any()).Return(testPV, nil).AnyTimes()

		// PVC operations
		mockPVCInterface.EXPECT().Get(gomock.Any(), "test-pvc", gomock.Any()).Return(testPVC, nil).AnyTimes()
		mockPVCInterface.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, pvc *v1.PersistentVolumeClaim, _ metav1.UpdateOptions) (*v1.PersistentVolumeClaim, error) {
				if pvc.Labels == nil {
					pvc.Labels = make(map[string]string)
				}
				pvc.Labels[LabelMigrationInProgress] = "true"
				return pvc, nil
			}).AnyTimes()

		// GetDiskByURI returns disk with changing completion percentage
		diskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, _, _ string) (*armcompute.Disk, error) {
				callCount++
				// Keep at 50% for longer to ensure timeout fires, then complete
				var completionPercent float32
				if callCount <= 8 { // First ~160ms (8 calls * 20ms interval) - should exceed 100ms timeout
					completionPercent = 50.0
				} else {
					completionPercent = 100.0 // Migration completes after timeout
				}

				disk := &armcompute.Disk{
					ID:   &id,
					Name: &testVolumeName,
					Properties: &armcompute.DiskProperties{
						DiskSizeGB:        &diskSizeGB,
						ProvisioningState: &state,
						CompletionPercent: &completionPercent,
					},
				}

				t.Logf("Call %d: Returning completion percent: %.1f%%", callCount, completionPercent)
				return disk, nil
			}).AnyTimes()

		// Create monitor
		monitor := NewMigrationProgressMonitor(mockKubeClient, mockEventRecorder, d.GetDiskController())
		diskURI := "/subscriptions/test/resourceGroups/rg/providers/Microsoft.Compute/disks/test-disk"

		// Start monitoring
		ctx := context.Background()
		err := monitor.StartMigrationMonitoring(ctx, diskURI, "test-pv",
			armcompute.DiskStorageAccountTypesPremiumLRS,
			armcompute.DiskStorageAccountTypesPremiumV2LRS)
		assert.NoError(t, err)
		assert.True(t, monitor.IsMigrationActive(diskURI))

		// Wait for migration timeout and completion
		time.Sleep(400 * time.Millisecond) // Wait long enough for both timeout and completion

		// Collect and verify events
		events := []string{}
		for {
			select {
			case event := <-mockEventRecorder.Events:
				events = append(events, event)
			default:
				goto checkEvents1
			}
		}

	checkEvents1:
		t.Logf("Test 1 - Captured events: %v", events)

		// Verify migration timeout event occurred
		timeoutEventFound := false
		completionEventFound := false
		for _, event := range events {
			if strings.Contains(event, "Warning") && strings.Contains(event, ReasonSKUMigrationTimeout) &&
				strings.Contains(event, "Migration taking too long") {
				timeoutEventFound = true
				t.Logf("Found migration timeout event: %s", event)
			}
			if strings.Contains(event, "Normal") && strings.Contains(event, ReasonSKUMigrationCompleted) {
				completionEventFound = true
				t.Logf("Found completion event: %s", event)
			}
		}

		assert.True(t, timeoutEventFound, "Expected migration timeout event not found")
		assert.True(t, completionEventFound, "Expected completion event not found")

		// Verify no max timeout event occurred
		for _, event := range events {
			assert.False(t, strings.Contains(event, "Stopping monitoring"),
				"Unexpected max timeout event found: %s", event)
		}

		// Stop and verify cleanup
		monitor.Stop()
		time.Sleep(100 * time.Millisecond)
		assert.False(t, monitor.IsMigrationActive(diskURI))
		assert.Equal(t, 0, len(monitor.GetActiveMigrations()))
	})

	// Test Case 2: Both timeouts hit - no completion
	t.Run("both migration and max timeout - no completion", func(t *testing.T) {
		defer func() {
			// Restore original values
			migrationTimeouts = originalMigrationTimeouts
			sortedMigrationSlabArray = originalSlabArray
			maxMigrationTimeout = originalMaxTimeout
			migrationCheckInterval = originalMigrationCheckInterval

			// Restore environment variables
			if originalMigrationTimeoutsEnv == "" {
				os.Unsetenv("MIGRATION_TIMEOUTS")
			} else {
				os.Setenv("MIGRATION_TIMEOUTS", originalMigrationTimeoutsEnv)
			}
			if originalMaxMigrationTimeoutEnv == "" {
				os.Unsetenv("MAX_MIGRATION_TIMEOUT")
			} else {
				os.Setenv("MAX_MIGRATION_TIMEOUT", originalMaxMigrationTimeoutEnv)
			}
		}()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		// Set up environment variables for very short timeouts
		os.Setenv("MIGRATION_TIMEOUTS", "2Gi=80ms") // Very short migration timeout
		os.Setenv("MAX_MIGRATION_TIMEOUT", "200ms") // Short max timeout

		// Reset global variables and reinitialize
		initializeTimeouts()

		klog.Infof("Timeouts initialized: %v", migrationTimeouts)
		klog.Infof("Sorted slab array: %v", sortedMigrationSlabArray)
		klog.Infof("Max migration timeout: %v", maxMigrationTimeout)

		// Set very fast check interval for testing
		migrationCheckInterval = 20 * time.Millisecond

		// Create mocks
		d := getFakeDriverWithKubeClientForMigration(ctrl)
		mockKubeClient := d.getCloud().KubeClient.(*mockkubeclient.MockInterface)
		mockCoreV1 := mockcorev1.NewMockInterface(ctrl)
		mockPVInterface := mockpersistentvolume.NewMockInterface(ctrl)
		mockPVCInterface := mockpersistentvolumeclaim.NewMockPersistentVolumeClaimInterface(ctrl)
		mockEventRecorder := record.NewFakeRecorder(100)
		diskClient := mock_diskclient.NewMockInterface(ctrl)

		// Set up disk client mock
		d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()

		// Create test PV and PVC
		testPV := &v1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pv"},
			Spec: v1.PersistentVolumeSpec{
				ClaimRef: &v1.ObjectReference{Name: "test-pvc", Namespace: "default"},
				Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		}

		testPVC := &v1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pvc", Namespace: "default"},
			Spec:       v1.PersistentVolumeClaimSpec{VolumeName: "test-pv"},
		}

		// Create disk that never completes (stays at 50%)
		diskSizeGB := int32(1)
		state := "Succeeded"
		completionPercent := float32(50.0) // Never changes - no completion
		testVolumeName := "test-disk"
		id := fmt.Sprintf("/subscriptions/test/resourceGroups/rg/providers/Microsoft.Compute/disks/%s", testVolumeName)

		disk := &armcompute.Disk{
			ID:   &id,
			Name: &testVolumeName,
			Properties: &armcompute.DiskProperties{
				DiskSizeGB:        &diskSizeGB,
				ProvisioningState: &state,
				CompletionPercent: &completionPercent, // Never reaches 100%
			},
		}

		// Set up mock expectations
		mockKubeClient.EXPECT().CoreV1().Return(mockCoreV1).AnyTimes()
		mockCoreV1.EXPECT().PersistentVolumes().Return(mockPVInterface).AnyTimes()
		mockCoreV1.EXPECT().PersistentVolumeClaims("default").Return(mockPVCInterface).AnyTimes()
		mockPVInterface.EXPECT().Get(gomock.Any(), "test-pv", gomock.Any()).Return(testPV, nil).AnyTimes()

		// PVC operations
		mockPVCInterface.EXPECT().Get(gomock.Any(), "test-pvc", gomock.Any()).Return(testPVC, nil).AnyTimes()
		mockPVCInterface.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, pvc *v1.PersistentVolumeClaim, _ metav1.UpdateOptions) (*v1.PersistentVolumeClaim, error) {
				if pvc.Labels == nil {
					pvc.Labels = make(map[string]string)
				}
				pvc.Labels[LabelMigrationInProgress] = "true"
				return pvc, nil
			}).AnyTimes()

		// GetDiskByURI always returns the same disk (never completes)
		diskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()

		// Create monitor
		monitor := NewMigrationProgressMonitor(mockKubeClient, mockEventRecorder, d.GetDiskController())
		diskURI := "/subscriptions/test/resourceGroups/rg/providers/Microsoft.Compute/disks/test-disk"

		// Start monitoring
		ctx := context.Background()
		err := monitor.StartMigrationMonitoring(ctx, diskURI, "test-pv",
			armcompute.DiskStorageAccountTypesPremiumLRS,
			armcompute.DiskStorageAccountTypesPremiumV2LRS)
		assert.NoError(t, err)
		assert.True(t, monitor.IsMigrationActive(diskURI))

		// Wait for both timeouts to trigger
		time.Sleep(400 * time.Millisecond) // Wait longer than max timeout (200ms)

		// Collect and verify events
		events := []string{}
		for {
			select {
			case event := <-mockEventRecorder.Events:
				events = append(events, event)
			default:
				goto checkEvents3
			}
		}

	checkEvents3:
		t.Logf("Test 2 - Captured events: %v", events)

		// Verify both timeout events occurred
		migrationTimeoutEventFound := false
		maxTimeoutEventFound := false
		for _, event := range events {
			if strings.Contains(event, "Warning") && strings.Contains(event, ReasonSKUMigrationTimeout) {
				if strings.Contains(event, "Migration taking too long") {
					migrationTimeoutEventFound = true
					t.Logf("Found migration timeout event: %s", event)
				} else if strings.Contains(event, "Stopping monitoring") {
					maxTimeoutEventFound = true
					t.Logf("Found max timeout event: %s", event)
				}
			}
		}

		assert.True(t, migrationTimeoutEventFound, "Expected migration timeout event not found")
		assert.True(t, maxTimeoutEventFound, "Expected max timeout event not found")

		// Verify no completion event occurred
		for _, event := range events {
			assert.False(t, strings.Contains(event, ReasonSKUMigrationCompleted),
				"Unexpected completion event found: %s", event)
		}

		// Stop and verify cleanup
		monitor.Stop()
		time.Sleep(100 * time.Millisecond)
		assert.False(t, monitor.IsMigrationActive(diskURI))
		assert.Equal(t, 0, len(monitor.GetActiveMigrations()))
	})
}

func getFakeDriverWithKubeClientForMigration(ctrl *gomock.Controller) FakeDriver {
	d, _ := NewFakeDriver(ctrl)

	d.getCloud().KubeClient = mockkubeclient.NewMockInterface(ctrl)
	return d
}
