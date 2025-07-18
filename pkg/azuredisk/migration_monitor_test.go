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
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk/mockcorev1"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk/mockkubeclient"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk/mockpersistentvolume"
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
		name        string
		diskURI     string
		pvName      string
		fromSKU     armcompute.DiskStorageAccountTypes
		toSKU       armcompute.DiskStorageAccountTypes
		expectError bool
	}{
		{
			name:        "successful start Premium_LRS to PremiumV2_LRS",
			diskURI:     "/subscriptions/test/resourceGroups/rg/providers/Microsoft.Compute/disks/test-disk",
			pvName:      "test-pv",
			fromSKU:     armcompute.DiskStorageAccountTypesPremiumLRS,
			toSKU:       armcompute.DiskStorageAccountTypesPremiumV2LRS,
			expectError: false,
		},
		{
			name:        "successful start Standard_LRS to Premium_LRS",
			diskURI:     "/subscriptions/test/resourceGroups/rg/providers/Microsoft.Compute/disks/test-disk-2",
			pvName:      "test-pv-2",
			fromSKU:     armcompute.DiskStorageAccountTypesStandardLRS,
			toSKU:       armcompute.DiskStorageAccountTypesPremiumLRS,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockKubeClient := mockkubeclient.NewMockInterface(ctrl)
			mockCoreV1 := mockcorev1.NewMockInterface(ctrl)
			mockPVInterface := mockpersistentvolume.NewMockInterface(ctrl)
			mockEventRecorder := record.NewFakeRecorder(10)
			mockDiskController := &ManagedDiskController{}

			// Create test PV for the event emission and annotation updates
			testPV := &v1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: tt.pvName,
				},
			}

			// Set up mock expectations for both event emission and annotation updates
			mockKubeClient.EXPECT().CoreV1().Return(mockCoreV1).AnyTimes()
			mockCoreV1.EXPECT().PersistentVolumes().Return(mockPVInterface).AnyTimes()

			// Expect Get calls for both event emission and addMigrationAnnotationsIfNotExists
			mockPVInterface.EXPECT().Get(gomock.Any(), tt.pvName, gomock.Any()).Return(testPV, nil).AnyTimes()

			// Expect Update call for addMigrationAnnotationsIfNotExists
			mockPVInterface.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, pv *v1.PersistentVolume, _ metav1.UpdateOptions) (*v1.PersistentVolume, error) {
					// Verify annotations were added correctly
					assert.Equal(t, "in-progress", pv.Annotations[AnnotationMigrationStatus])
					assert.Equal(t, string(tt.fromSKU), pv.Annotations[AnnotationMigrationFrom])
					assert.Equal(t, string(tt.toSKU), pv.Annotations[AnnotationMigrationTo])
					assert.NotEmpty(t, pv.Annotations[AnnotationMigrationStart])
					return pv, nil
				}).Times(1)

			monitor := NewMigrationProgressMonitor(mockKubeClient, mockEventRecorder, mockDiskController)

			ctx := context.Background()
			err := monitor.StartMigrationMonitoring(ctx, tt.diskURI, tt.pvName, tt.fromSKU, tt.toSKU)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.True(t, monitor.IsMigrationActive(tt.diskURI))

				// Verify task was created correctly
				activeTasks := monitor.GetActiveMigrations()
				assert.Equal(t, 1, len(activeTasks))

				task, exists := activeTasks[tt.diskURI]
				assert.True(t, exists)
				assert.Equal(t, tt.diskURI, task.DiskURI)
				assert.Equal(t, tt.pvName, task.PVName)
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
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockKubeClient := mockkubeclient.NewMockInterface(ctrl)
	mockCoreV1 := mockcorev1.NewMockInterface(ctrl)
	mockPVInterface := mockpersistentvolume.NewMockInterface(ctrl)
	mockEventRecorder := record.NewFakeRecorder(10)
	mockDiskController := &ManagedDiskController{}

	// Create test PV for the event emission
	testPV := &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pv",
		},
	}

	// Set up mock expectations for event emission and annotation updates
	mockKubeClient.EXPECT().CoreV1().Return(mockCoreV1).AnyTimes()
	mockCoreV1.EXPECT().PersistentVolumes().Return(mockPVInterface).AnyTimes()
	mockPVInterface.EXPECT().Get(gomock.Any(), "test-pv", gomock.Any()).Return(testPV, nil).AnyTimes()

	// Add the missing Update expectation for addMigrationAnnotationsIfNotExists
	mockPVInterface.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, pv *v1.PersistentVolume, _ metav1.UpdateOptions) (*v1.PersistentVolume, error) {
			// Verify annotations were added correctly
			assert.Equal(t, "in-progress", pv.Annotations[AnnotationMigrationStatus])
			assert.Equal(t, string(armcompute.DiskStorageAccountTypesPremiumLRS), pv.Annotations[AnnotationMigrationFrom])
			assert.Equal(t, string(armcompute.DiskStorageAccountTypesPremiumV2LRS), pv.Annotations[AnnotationMigrationTo])
			assert.NotEmpty(t, pv.Annotations[AnnotationMigrationStart])
			return pv, nil
		}).Times(1)

	monitor := NewMigrationProgressMonitor(mockKubeClient, mockEventRecorder, mockDiskController)

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
	time.Sleep(100 * time.Millisecond) // Allow goroutines to finish
	assert.False(t, monitor.IsMigrationActive(diskURI))
}

func TestMigrationShouldReportProgress(t *testing.T) {
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
	mockPVInterface := mockpersistentvolume.NewMockInterface(ctrl)
	mockEventRecorder := record.NewFakeRecorder(10)
	mockDiskController := &ManagedDiskController{}

	monitor := NewMigrationProgressMonitor(mockKubeClient, mockEventRecorder, mockDiskController)

	// Create test PV
	testPV := &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pv",
		},
	}

	// Create test task
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	task := &MigrationTask{
		DiskURI:    "/subscriptions/test/resourceGroups/rg/providers/Microsoft.Compute/disks/test-disk",
		PVName:     "test-pv",
		FromSKU:    armcompute.DiskStorageAccountTypesPremiumLRS,
		ToSKU:      armcompute.DiskStorageAccountTypesPremiumV2LRS,
		StartTime:  time.Now(),
		Context:    ctx,
		CancelFunc: cancel,
	}

	// Set up mocks
	mockKubeClient.EXPECT().CoreV1().Return(mockCoreV1)
	mockCoreV1.EXPECT().PersistentVolumes().Return(mockPVInterface)
	mockPVInterface.EXPECT().Get(gomock.Any(), "test-pv", gomock.Any()).Return(testPV, nil)

	// Test successful event emission
	assert.NoError(t, monitor.emitMigrationEvent(task, EventTypeNormal, ReasonSKUMigrationStarted, "Test migration started"))

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

func TestEmitMigrationEvent_PVNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockKubeClient := mockkubeclient.NewMockInterface(ctrl)
	mockCoreV1 := mockcorev1.NewMockInterface(ctrl)
	mockPVInterface := mockpersistentvolume.NewMockInterface(ctrl)
	mockEventRecorder := record.NewFakeRecorder(10)
	mockDiskController := &ManagedDiskController{}

	monitor := NewMigrationProgressMonitor(mockKubeClient, mockEventRecorder, mockDiskController)

	// Create test task
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	task := &MigrationTask{
		DiskURI:    "/subscriptions/test/resourceGroups/rg/providers/Microsoft.Compute/disks/test-disk",
		PVName:     "non-existent-pv",
		FromSKU:    armcompute.DiskStorageAccountTypesPremiumLRS,
		ToSKU:      armcompute.DiskStorageAccountTypesPremiumV2LRS,
		StartTime:  time.Now(),
		Context:    ctx,
		CancelFunc: cancel,
	}

	// Set up mocks to return error
	mockKubeClient.EXPECT().CoreV1().Return(mockCoreV1)
	mockCoreV1.EXPECT().PersistentVolumes().Return(mockPVInterface)
	mockPVInterface.EXPECT().Get(gomock.Any(), "non-existent-pv", gomock.Any()).Return(nil, errors.New("not found"))

	// Test event emission with PV not found - should not panic
	assert.NoError(t, monitor.emitMigrationEvent(task, EventTypeNormal, ReasonSKUMigrationStarted, "Test migration started"))

	// Verify no event was recorded
	select {
	case <-mockEventRecorder.Events:
		t.Error("No event should have been recorded when PV is not found")
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
	mockEventRecorder := record.NewFakeRecorder(10)
	mockDiskController := &ManagedDiskController{}

	// Create test PVs for the event emission
	testPVs := []*v1.PersistentVolume{
		{ObjectMeta: metav1.ObjectMeta{Name: "pv-1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "pv-2"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "pv-3"}},
	}

	// Set up mock expectations for event emission and annotation updates
	mockKubeClient.EXPECT().CoreV1().Return(mockCoreV1).AnyTimes()
	mockCoreV1.EXPECT().PersistentVolumes().Return(mockPVInterface).AnyTimes()

	// Set up expectations for each PV (Get calls for both event emission and addMigrationAnnotationsIfNotExists)
	for i, pv := range testPVs {
		pvName := fmt.Sprintf("pv-%d", i+1)
		mockPVInterface.EXPECT().Get(gomock.Any(), pvName, gomock.Any()).Return(pv, nil).AnyTimes()
	}

	// Add Update expectations for addMigrationAnnotationsIfNotExists (3 calls for 3 migrations)
	mockPVInterface.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, pv *v1.PersistentVolume, _ metav1.UpdateOptions) (*v1.PersistentVolume, error) {
			// Verify annotations were added correctly
			assert.Equal(t, "in-progress", pv.Annotations[AnnotationMigrationStatus])
			assert.Equal(t, string(armcompute.DiskStorageAccountTypesPremiumLRS), pv.Annotations[AnnotationMigrationFrom])
			assert.Equal(t, string(armcompute.DiskStorageAccountTypesPremiumV2LRS), pv.Annotations[AnnotationMigrationTo])
			assert.NotEmpty(t, pv.Annotations[AnnotationMigrationStart])
			return pv, nil
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

func TestRecoverMigrationMonitorsFromAnnotations(t *testing.T) {
	tests := []struct {
		name          string
		existingPVs   []*v1.PersistentVolume
		expectedCount int
		expectError   bool
	}{
		{
			name: "recover single ongoing migration",
			existingPVs: []*v1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pv-with-migration",
						Annotations: map[string]string{
							AnnotationMigrationStatus: "in-progress",
							AnnotationMigrationFrom:   string(armcompute.DiskStorageAccountTypesPremiumLRS),
							AnnotationMigrationTo:     string(armcompute.DiskStorageAccountTypesPremiumV2LRS),
							AnnotationMigrationStart:  time.Now().Format(time.RFC3339),
						},
					},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								Driver:       "disk.csi.azure.com",
								VolumeHandle: "/subscriptions/test/resourceGroups/rg/providers/Microsoft.Compute/disks/test-disk",
							},
						},
					},
				},
			},
			expectedCount: 1,
			expectError:   false,
		},
		{
			name: "recover multiple ongoing migrations",
			existingPVs: []*v1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pv-migration-1",
						Annotations: map[string]string{
							AnnotationMigrationStatus: "in-progress",
							AnnotationMigrationFrom:   string(armcompute.DiskStorageAccountTypesPremiumLRS),
							AnnotationMigrationTo:     string(armcompute.DiskStorageAccountTypesPremiumV2LRS),
						},
					},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								Driver:       "disk.csi.azure.com",
								VolumeHandle: "/subscriptions/test/resourceGroups/rg/providers/Microsoft.Compute/disks/disk-1",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pv-migration-2",
						Annotations: map[string]string{
							AnnotationMigrationStatus: "in-progress",
							AnnotationMigrationFrom:   string(armcompute.DiskStorageAccountTypesStandardLRS),
							AnnotationMigrationTo:     string(armcompute.DiskStorageAccountTypesPremiumLRS),
						},
					},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								Driver:       "disk.csi.azure.com",
								VolumeHandle: "/subscriptions/test/resourceGroups/rg/providers/Microsoft.Compute/disks/disk-2",
							},
						},
					},
				},
			},
			expectedCount: 2,
			expectError:   false,
		},
		{
			name: "skip PVs without migration annotations",
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
					},
				},
			},
			expectedCount: 0,
			expectError:   false,
		},
		{
			name: "skip non-Azure disk PVs",
			existingPVs: []*v1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pv-other-driver",
						Annotations: map[string]string{
							AnnotationMigrationStatus: "in-progress",
							AnnotationMigrationFrom:   string(armcompute.DiskStorageAccountTypesPremiumLRS),
							AnnotationMigrationTo:     string(armcompute.DiskStorageAccountTypesPremiumV2LRS),
						},
					},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								Driver:       "other.csi.driver",
								VolumeHandle: "/some/other/volume",
							},
						},
					},
				},
			},
			expectedCount: 0,
			expectError:   false,
		},
		{
			name: "skip completed migrations",
			existingPVs: []*v1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pv-completed-migration",
						Annotations: map[string]string{
							AnnotationMigrationStatus: "completed",
							AnnotationMigrationFrom:   string(armcompute.DiskStorageAccountTypesPremiumLRS),
							AnnotationMigrationTo:     string(armcompute.DiskStorageAccountTypesPremiumV2LRS),
						},
					},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								Driver:       "disk.csi.azure.com",
								VolumeHandle: "/subscriptions/test/resourceGroups/rg/providers/Microsoft.Compute/disks/completed-disk",
							},
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
			mockEventRecorder := record.NewFakeRecorder(10)

			// Create Driver using the proper initialization pattern
			driver := &Driver{}
			driver.Name = "disk.csi.azure.com" // Set the driver name directly
			driver.cloud = &azure.Cloud{
				KubeClient: mockKubeClient,
			}
			driver.migrationMonitor = NewMigrationProgressMonitor(mockKubeClient, mockEventRecorder, &ManagedDiskController{})

			// Setup mock expectations for listing PVs
			pvList := &v1.PersistentVolumeList{Items: make([]v1.PersistentVolume, len(tt.existingPVs))}
			for i, pv := range tt.existingPVs {
				pvList.Items[i] = *pv
			}

			mockKubeClient.EXPECT().CoreV1().Return(mockCoreV1).AnyTimes()
			mockCoreV1.EXPECT().PersistentVolumes().Return(mockPVInterface).AnyTimes()
			mockPVInterface.EXPECT().List(gomock.Any(), gomock.Any()).Return(pvList, nil)

			// Setup expectations for each PV that should be recovered
			for _, pv := range tt.existingPVs {
				if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == "disk.csi.azure.com" {
					if status, exists := pv.Annotations[AnnotationMigrationStatus]; exists && status == "in-progress" {
						if pv.Annotations[AnnotationMigrationFrom] != "" && pv.Annotations[AnnotationMigrationTo] != "" {
							// This PV should be recovered - setup Get expectation for StartMigrationMonitoring
							mockPVInterface.EXPECT().Get(gomock.Any(), pv.Name, gomock.Any()).Return(pv, nil).AnyTimes()

							// StartMigrationMonitoring calls addMigrationAnnotationsIfNotExists which needs Update expectation
							mockPVInterface.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
								func(_ context.Context, updatedPV *v1.PersistentVolume, _ metav1.UpdateOptions) (*v1.PersistentVolume, error) {
									// Just return the updated PV - the annotations are already set
									return updatedPV, nil
								}).AnyTimes()
						}
					}
				}
			}

			// Execute recovery
			ctx := context.Background()
			err := driver.recoverMigrationMonitorsFromAnnotations(ctx)

			// Verify results
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)

				// Verify correct number of migrations were recovered
				activeTasks := driver.migrationMonitor.GetActiveMigrations()
				assert.Equal(t, tt.expectedCount, len(activeTasks))

				// Verify each recovered migration is properly configured
				for _, pv := range tt.existingPVs {
					if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == "disk.csi.azure.com" {
						if status, exists := pv.Annotations[AnnotationMigrationStatus]; exists && status == "in-progress" {
							if pv.Annotations[AnnotationMigrationFrom] != "" && pv.Annotations[AnnotationMigrationTo] != "" {
								diskURI := pv.Spec.CSI.VolumeHandle
								assert.True(t, driver.migrationMonitor.IsMigrationActive(diskURI))

								task, exists := activeTasks[diskURI]
								assert.True(t, exists)
								assert.Equal(t, pv.Name, task.PVName)
								assert.Equal(t, armcompute.DiskStorageAccountTypes(pv.Annotations[AnnotationMigrationFrom]), task.FromSKU)
								assert.Equal(t, armcompute.DiskStorageAccountTypes(pv.Annotations[AnnotationMigrationTo]), task.ToSKU)
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

func TestAddMigrationAnnotationsIfNotExists(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockKubeClient := mockkubeclient.NewMockInterface(ctrl)
	mockCoreV1 := mockcorev1.NewMockInterface(ctrl)
	mockPVInterface := mockpersistentvolume.NewMockInterface(ctrl)
	mockEventRecorder := record.NewFakeRecorder(10)
	mockDiskController := &ManagedDiskController{}

	monitor := NewMigrationProgressMonitor(mockKubeClient, mockEventRecorder, mockDiskController)

	// Create test PV without annotations
	testPV := &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pv",
		},
	}

	// Setup mock expectations - need multiple calls to CoreV1() and PersistentVolumes()
	mockKubeClient.EXPECT().CoreV1().Return(mockCoreV1).AnyTimes()
	mockCoreV1.EXPECT().PersistentVolumes().Return(mockPVInterface).AnyTimes()
	mockPVInterface.EXPECT().Get(gomock.Any(), "test-pv", gomock.Any()).Return(testPV, nil)
	mockPVInterface.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, pv *v1.PersistentVolume, _ metav1.UpdateOptions) (*v1.PersistentVolume, error) {
			// Verify annotations were added correctly
			assert.Equal(t, "in-progress", pv.Annotations[AnnotationMigrationStatus])
			assert.Equal(t, string(armcompute.DiskStorageAccountTypesPremiumLRS), pv.Annotations[AnnotationMigrationFrom])
			assert.Equal(t, string(armcompute.DiskStorageAccountTypesPremiumV2LRS), pv.Annotations[AnnotationMigrationTo])
			assert.NotEmpty(t, pv.Annotations[AnnotationMigrationStart])
			return pv, nil
		})

	// Execute
	ctx := context.Background()
	exists, err := monitor.addMigrationAnnotationsIfNotExists(ctx, "test-pv",
		armcompute.DiskStorageAccountTypesPremiumLRS,
		armcompute.DiskStorageAccountTypesPremiumV2LRS)

	assert.Equal(t, exists, false)
	assert.NoError(t, err)
}

func TestRemoveMigrationAnnotations(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockKubeClient := mockkubeclient.NewMockInterface(ctrl)
	mockCoreV1 := mockcorev1.NewMockInterface(ctrl)
	mockPVInterface := mockpersistentvolume.NewMockInterface(ctrl)
	mockEventRecorder := record.NewFakeRecorder(10)
	mockDiskController := &ManagedDiskController{}

	monitor := NewMigrationProgressMonitor(mockKubeClient, mockEventRecorder, mockDiskController)

	// Create test PV with migration annotations
	testPV := &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pv",
			Annotations: map[string]string{
				AnnotationMigrationStatus: "in-progress",
				AnnotationMigrationFrom:   string(armcompute.DiskStorageAccountTypesPremiumLRS),
				AnnotationMigrationTo:     string(armcompute.DiskStorageAccountTypesPremiumV2LRS),
				AnnotationMigrationStart:  time.Now().Format(time.RFC3339),
				"other.annotation":        "should-remain",
			},
		},
	}

	// Setup mock expectations - need multiple calls to CoreV1() and PersistentVolumes()
	mockKubeClient.EXPECT().CoreV1().Return(mockCoreV1).AnyTimes()
	mockCoreV1.EXPECT().PersistentVolumes().Return(mockPVInterface).AnyTimes()
	mockPVInterface.EXPECT().Get(gomock.Any(), "test-pv", gomock.Any()).Return(testPV, nil)
	mockPVInterface.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, pv *v1.PersistentVolume, _ metav1.UpdateOptions) (*v1.PersistentVolume, error) {
			// Verify migration annotations were removed but others remain
			assert.NotContains(t, pv.Annotations, AnnotationMigrationStatus)
			assert.NotContains(t, pv.Annotations, AnnotationMigrationFrom)
			assert.NotContains(t, pv.Annotations, AnnotationMigrationTo)
			assert.NotContains(t, pv.Annotations, AnnotationMigrationStart)
			assert.Contains(t, pv.Annotations, "other.annotation")
			assert.Equal(t, "should-remain", pv.Annotations["other.annotation"])
			return pv, nil
		})

	// Execute
	ctx := context.Background()
	err := monitor.removeMigrationAnnotations(ctx, "test-pv")

	assert.NoError(t, err)
}

func TestMigrationMonitorControllerRestart_EndToEnd(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockKubeClient := mockkubeclient.NewMockInterface(ctrl)
	mockCoreV1 := mockcorev1.NewMockInterface(ctrl)
	mockPVInterface := mockpersistentvolume.NewMockInterface(ctrl)
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
			},
		}

		// Mock expectations for starting migration (adds annotations)
		mockKubeClient.EXPECT().CoreV1().Return(mockCoreV1).AnyTimes()
		mockCoreV1.EXPECT().PersistentVolumes().Return(mockPVInterface).AnyTimes()
		mockPVInterface.EXPECT().Get(gomock.Any(), "test-pv", gomock.Any()).Return(testPV, nil).AnyTimes()
		mockPVInterface.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, pv *v1.PersistentVolume, _ metav1.UpdateOptions) (*v1.PersistentVolume, error) {
				// Simulate annotations being added
				if pv.Annotations == nil {
					pv.Annotations = make(map[string]string)
				}
				pv.Annotations[AnnotationMigrationStatus] = "in-progress"
				pv.Annotations[AnnotationMigrationFrom] = string(armcompute.DiskStorageAccountTypesPremiumLRS)
				pv.Annotations[AnnotationMigrationTo] = string(armcompute.DiskStorageAccountTypesPremiumV2LRS)
				pv.Annotations[AnnotationMigrationStart] = time.Now().Format(time.RFC3339)
				return pv, nil
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

		// Update testPV to have the migration annotations (simulating persistence)
		testPVWithAnnotations := testPV.DeepCopy()
		testPVWithAnnotations.Annotations = map[string]string{
			AnnotationMigrationStatus: "in-progress",
			AnnotationMigrationFrom:   string(armcompute.DiskStorageAccountTypesPremiumLRS),
			AnnotationMigrationTo:     string(armcompute.DiskStorageAccountTypesPremiumV2LRS),
			AnnotationMigrationStart:  time.Now().Format(time.RFC3339),
		}

		// Mock expectations for recovery
		pvList := &v1.PersistentVolumeList{
			Items: []v1.PersistentVolume{*testPVWithAnnotations},
		}
		mockPVInterface.EXPECT().List(gomock.Any(), gomock.Any()).Return(pvList, nil)

		// Phase 3: Recover migrations
		err = driver.recoverMigrationMonitorsFromAnnotations(ctx)
		assert.NoError(t, err)

		// Verify migration was recovered
		assert.True(t, monitor2.IsMigrationActive(diskURI))
		activeTasks := monitor2.GetActiveMigrations()
		assert.Equal(t, 1, len(activeTasks))

		task, exists := activeTasks[diskURI]
		assert.True(t, exists)
		assert.Equal(t, "test-pv", task.PVName)
		assert.Equal(t, armcompute.DiskStorageAccountTypesPremiumLRS, task.FromSKU)
		assert.Equal(t, armcompute.DiskStorageAccountTypesPremiumV2LRS, task.ToSKU)

		// Cleanup
		monitor2.Stop()
	})
}
