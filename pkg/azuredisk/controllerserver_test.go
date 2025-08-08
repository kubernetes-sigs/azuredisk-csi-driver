/*
Copyright 2019 The Kubernetes Authors.

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
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk/mockcorev1"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk/mockkubeclient"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk/mockpersistentvolume"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk/mockpersistentvolumeclaim"
	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	"sigs.k8s.io/cloud-provider-azure/pkg/azclient/diskclient/mock_diskclient"
	"sigs.k8s.io/cloud-provider-azure/pkg/azclient/mock_azclient"
	"sigs.k8s.io/cloud-provider-azure/pkg/azclient/snapshotclient/mock_snapshotclient"
	mockvmclient "sigs.k8s.io/cloud-provider-azure/pkg/azclient/virtualmachineclient/mock_virtualmachineclient"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

var (
	testVolumeName = "unit-test-volume"
	testVolumeID   = fmt.Sprintf(consts.ManagedDiskPath, "subs", "rg", testVolumeName)
)

func checkTestError(t *testing.T, expectedErrCode codes.Code, err error) {
	s, ok := status.FromError(err)
	if !ok {
		t.Errorf("could not get error status from err: %v", s)
	}
	if s.Code() != expectedErrCode {
		t.Errorf("expected error code: %v, actual: %v, err: %v", expectedErrCode, s.Code(), err)
	}
}

func TestCreateVolume(t *testing.T) {
	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: " invalid ",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				d.setControllerCapabilities([]*csi.ControllerServiceCapability{})

				req := &csi.CreateVolumeRequest{}
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "CREATE_DELETE_VOLUME")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: " volume name missing",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				req := &csi.CreateVolumeRequest{}
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "CreateVolume Name must be provided")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "volume capabilities missing",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				req := &csi.CreateVolumeRequest{
					Name: "unit-test",
				}
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "CreateVolume Volume capabilities must be provided")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "require volume size exceed",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				stdCapacityRange = &csi.CapacityRange{
					RequiredBytes: volumehelper.GiBToBytes(15),
					LimitBytes:    volumehelper.GiBToBytes(10),
				}
				req := &csi.CreateVolumeRequest{
					Name:               "unit-test",
					CapacityRange:      stdCapacityRange,
					VolumeCapabilities: createVolumeCapabilities(csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
				}
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "After round-up, volume size exceeds the limit specified")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "logical sector size parse error",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				mp := make(map[string]string)
				mp[consts.LogicalSectorSizeField] = "aaa"
				req := &csi.CreateVolumeRequest{
					Name:               "unit-test",
					VolumeCapabilities: createVolumeCapabilities(csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
					Parameters:         mp,
				}
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "Failed parsing disk parameters: parse aaa failed with error: strconv.Atoi: parsing \"aaa\": invalid syntax")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "maxshare parse error ",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				mp := make(map[string]string)
				mp[consts.MaxSharesField] = "aaa"
				req := &csi.CreateVolumeRequest{
					Name:               "unit-test",
					VolumeCapabilities: createVolumeCapabilities(csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
					Parameters:         mp,
				}
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "Failed parsing disk parameters: parse aaa failed with error: strconv.Atoi: parsing \"aaa\": invalid syntax")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "maxshare invalid value ",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				mp := make(map[string]string)
				mp[consts.MaxSharesField] = "0"
				req := &csi.CreateVolumeRequest{
					Name:               "unit-test",
					VolumeCapabilities: stdVolumeCapabilities,
					Parameters:         mp,
				}
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "Failed parsing disk parameters: parse 0 returned with invalid value: 0")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "invalid perf profile",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				mp := make(map[string]string)
				mp[consts.PerfProfileField] = "blah"
				req := &csi.CreateVolumeRequest{
					Name:               "unit-test",
					VolumeCapabilities: stdVolumeCapabilities,
					Parameters:         mp,
				}
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "Failed parsing disk parameters: perf profile blah is not supported, supported tuning modes are none and basic")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "Volume capability not supported ",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				mp := make(map[string]string)
				mp[consts.MaxSharesField] = "1"
				mp[consts.SkuNameField] = "ut"
				mp[consts.LocationField] = "ut"
				mp[consts.StorageAccountTypeField] = "ut"
				mp[consts.ResourceGroupField] = "ut"
				mp[consts.DiskIOPSReadWriteField] = "1"
				mp[consts.DiskMBPSReadWriteField] = "1"
				mp[consts.DiskNameField] = "ut"
				mp[consts.DesIDField] = "ut"
				mp[consts.WriteAcceleratorEnabled] = "ut"
				req := &csi.CreateVolumeRequest{
					Name:               "unit-test",
					VolumeCapabilities: createVolumeCapabilities(csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER),
					Parameters:         mp,
				}
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "mountVolume is not supported for access mode: MULTI_NODE_MULTI_WRITER")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "normalize storageaccounttype error ",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				mp := make(map[string]string)
				mp[consts.StorageAccountTypeField] = "NOT_EXISTING"
				req := &csi.CreateVolumeRequest{
					Name:               "unit-test",
					VolumeCapabilities: createVolumeCapabilities(csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
					Parameters:         mp,
				}
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "azureDisk - NOT_EXISTING is not supported sku/storageaccounttype. Supported values are [Premium_LRS PremiumV2_LRS Premium_ZRS Standard_LRS StandardSSD_LRS StandardSSD_ZRS UltraSSD_LRS]")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "normalize cache mode error ",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				mp := make(map[string]string)
				mp[consts.CachingModeField] = "WriteOnly"
				req := &csi.CreateVolumeRequest{
					Name:               "unit-test",
					VolumeCapabilities: createVolumeCapabilities(csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
					Parameters:         mp,
				}
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "azureDisk - WriteOnly is not supported cachingmode. Supported values are [None ReadOnly ReadWrite]")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "custom tags error ",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				mp := make(map[string]string)
				mp["tags"] = "unit-test"
				req := &csi.CreateVolumeRequest{
					Name:               "unit-test",
					VolumeCapabilities: createVolumeCapabilities(csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
					Parameters:         mp,
				}
				disk := &armcompute.Disk{
					Properties: &armcompute.DiskProperties{},
				}
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()
				diskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "Failed parsing disk parameters: tags 'unit-test' are invalid, the format should like: 'key1=value1,key2=value2'")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "create managed disk error ",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				mp := make(map[string]string)
				mp["tags"] = "unit=test"
				volumeSnapshotSource := &csi.VolumeContentSource_SnapshotSource{
					SnapshotId: "unit-test",
				}
				volumeContentSourceSnapshotSource := &csi.VolumeContentSource_Snapshot{
					Snapshot: volumeSnapshotSource,
				}
				volumecontensource := csi.VolumeContentSource{
					Type: volumeContentSourceSnapshotSource,
				}
				req := &csi.CreateVolumeRequest{
					Name:                "unit-test",
					VolumeCapabilities:  createVolumeCapabilities(csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
					Parameters:          mp,
					VolumeContentSource: &volumecontensource,
				}
				disk := &armcompute.Disk{
					Properties: &armcompute.DiskProperties{},
				}
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()
				diskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				diskClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("test")).AnyTimes()
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Errorf(codes.Internal, "test")
				if err.Error() != expectedErr.Error() {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "create managed disk not found error ",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				mp := make(map[string]string)
				volumeContentSourceSnapshotSource := &csi.VolumeContentSource_Snapshot{}
				volumecontensource := csi.VolumeContentSource{
					Type: volumeContentSourceSnapshotSource,
				}
				req := &csi.CreateVolumeRequest{
					Name:                "unit-test",
					VolumeCapabilities:  createVolumeCapabilities(csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
					Parameters:          mp,
					VolumeContentSource: &volumecontensource,
				}
				disk := &armcompute.Disk{
					Properties: &armcompute.DiskProperties{},
				}
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()
				diskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				diskClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf(consts.NotFound)).AnyTimes()
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Error(codes.NotFound, "invalid URI: ")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "valid request ZRS",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				mp := make(map[string]string)
				mp[consts.SkuNameField] = "StandardSSD_ZRS"
				stdCapacityRangetest := &csi.CapacityRange{
					RequiredBytes: volumehelper.GiBToBytes(10),
					LimitBytes:    volumehelper.GiBToBytes(15),
				}
				req := &csi.CreateVolumeRequest{
					Name:               testVolumeName,
					VolumeCapabilities: stdVolumeCapabilities,
					CapacityRange:      stdCapacityRangetest,
					Parameters:         mp,
				}
				size := int32(volumehelper.BytesToGiB(req.CapacityRange.RequiredBytes))
				id := fmt.Sprintf(consts.ManagedDiskPath, "subs", "rg", testVolumeName)
				state := "Succeeded"
				disk := &armcompute.Disk{
					ID:   &id,
					Name: &testVolumeName,
					Properties: &armcompute.DiskProperties{
						DiskSizeGB:        &size,
						ProvisioningState: &state,
					},
				}
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()
				diskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				diskClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := error(nil)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "valid request",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				stdCapacityRangetest := &csi.CapacityRange{
					RequiredBytes: volumehelper.GiBToBytes(10),
					LimitBytes:    volumehelper.GiBToBytes(15),
				}
				req := &csi.CreateVolumeRequest{
					Name:               testVolumeName,
					VolumeCapabilities: stdVolumeCapabilities,
					CapacityRange:      stdCapacityRangetest,
				}
				size := int32(volumehelper.BytesToGiB(req.CapacityRange.RequiredBytes))
				id := fmt.Sprintf(consts.ManagedDiskPath, "subs", "rg", testVolumeName)
				state := "Succeeded"
				disk := &armcompute.Disk{
					ID:   &id,
					Name: &testVolumeName,
					Properties: &armcompute.DiskProperties{
						DiskSizeGB:        &size,
						ProvisioningState: &state,
					},
				}
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()
				diskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				diskClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := error(nil)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "invalid parameter",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				stdCapacityRangetest := &csi.CapacityRange{
					RequiredBytes: volumehelper.GiBToBytes(10),
					LimitBytes:    volumehelper.GiBToBytes(15),
				}
				req := &csi.CreateVolumeRequest{
					Name:               testVolumeName,
					VolumeCapabilities: stdVolumeCapabilities,
					CapacityRange:      stdCapacityRangetest,
					Parameters:         map[string]string{"invalidparameter": "value"},
				}
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "Failed parsing disk parameters: invalid parameter invalidparameter in storage class")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "[Failure] advanced perfProfile fails if no device settings provided",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				d.setPerfOptimizationEnabled(true)
				stdCapacityRangetest := &csi.CapacityRange{
					RequiredBytes: volumehelper.GiBToBytes(10),
					LimitBytes:    volumehelper.GiBToBytes(15),
				}
				req := &csi.CreateVolumeRequest{
					Name:               testVolumeName,
					VolumeCapabilities: stdVolumeCapabilities,
					CapacityRange:      stdCapacityRangetest,
					Parameters:         map[string]string{"perfProfile": "advanced"},
				}
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "AreDeviceSettingsValid: No deviceSettings passed")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "valid PerformancePlus request, disk resizes to min required size",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				stdCapacityRangetest := &csi.CapacityRange{
					RequiredBytes: volumehelper.GiBToBytes(10),
					LimitBytes:    volumehelper.GiBToBytes(514),
				}
				req := &csi.CreateVolumeRequest{
					Name:               testVolumeName,
					VolumeCapabilities: stdVolumeCapabilities,
					CapacityRange:      stdCapacityRangetest,
					Parameters:         map[string]string{consts.PerformancePlusField: "true"},
				}
				size := int32(volumehelper.BytesToGiB(req.CapacityRange.RequiredBytes))
				id := fmt.Sprintf(consts.ManagedDiskPath, "subs", "rg", testVolumeName)
				state := "Succeeded"
				disk := &armcompute.Disk{
					ID:   &id,
					Name: &testVolumeName,
					Properties: &armcompute.DiskProperties{
						DiskSizeGB:        &size,
						ProvisioningState: &state,
					},
				}
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()
				diskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				diskClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				res, err := d.CreateVolume(context.Background(), req)
				assert.Equal(t, res.Volume.CapacityBytes, volumehelper.GiBToBytes(consts.PerformancePlusMinimumDiskSizeGiB))
				expectedErr := error(nil)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "valid disk created with custom parameters",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				stdCapacityRangetest := &csi.CapacityRange{
					RequiredBytes: volumehelper.GiBToBytes(10),
					LimitBytes:    volumehelper.GiBToBytes(514),
				}
				req := &csi.CreateVolumeRequest{
					Name:               testVolumeName,
					VolumeCapabilities: stdVolumeCapabilities,
					CapacityRange:      stdCapacityRangetest,
					Parameters: map[string]string{
						consts.SkuNameField:           string(armcompute.DiskStorageAccountTypesPremiumV2LRS),
						consts.DiskIOPSReadWriteField: "3000",
						consts.DiskMBPSReadWriteField: "125",
					},
					MutableParameters: map[string]string{
						consts.DiskIOPSReadWriteField: "5000",
						consts.DiskMBPSReadWriteField: "300",
					},
				}
				size := int32(volumehelper.BytesToGiB(req.CapacityRange.RequiredBytes))
				id := fmt.Sprintf(consts.ManagedDiskPath, "subs", "rg", testVolumeName)
				state := "Succeeded"
				disk := &armcompute.Disk{
					ID:   &id,
					Name: &testVolumeName,
					Properties: &armcompute.DiskProperties{
						DiskSizeGB:        &size,
						ProvisioningState: &state,
					},
				}
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()
				diskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				diskClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				res, err := d.CreateVolume(context.Background(), req)
				expectedVolumeContext := map[string]string{
					consts.CachingModeField:       "None",
					consts.SkuNameField:           string(armcompute.DiskStorageAccountTypesPremiumV2LRS),
					consts.DiskIOPSReadWriteField: "5000",
					consts.DiskMBPSReadWriteField: "300",
					consts.RequestedSizeGib:       "10",
				}
				if !reflect.DeepEqual(expectedVolumeContext, res.Volume.GetVolumeContext()) {
					t.Errorf("actualVolumeContext: (%v), expectedVolumeContext: (%v)", res.Volume.GetVolumeContext(), expectedVolumeContext)
				}
				expectedErr := error(nil)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func TestDeleteVolume(t *testing.T) {
	cntl := gomock.NewController(t)
	defer cntl.Finish()
	d, err := NewFakeDriver(cntl)
	if err != nil {
		t.Fatalf("Error getting driver: %v", err)
	}

	tests := []struct {
		desc            string
		req             *csi.DeleteVolumeRequest
		expectedResp    *csi.DeleteVolumeResponse
		expectedErrCode codes.Code
	}{
		{
			desc: "success standard",
			req: &csi.DeleteVolumeRequest{
				VolumeId: testVolumeID,
			},
			expectedResp: &csi.DeleteVolumeResponse{},
		},
		{
			desc: "fail with no volume id",
			req: &csi.DeleteVolumeRequest{
				VolumeId: "",
			},
			expectedResp:    nil,
			expectedErrCode: codes.InvalidArgument,
		},
		{
			desc: "fail with the invalid diskURI",
			req: &csi.DeleteVolumeRequest{
				VolumeId: "123",
			},
			expectedResp: &csi.DeleteVolumeResponse{},
		},
	}

	for _, test := range tests {
		ctx, cancel := context.WithCancel(context.TODO())
		defer cancel()
		id := test.req.VolumeId
		disk := &armcompute.Disk{
			ID: &id,
		}
		diskClient := mock_diskclient.NewMockInterface(cntl)
		d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()
		diskClient.EXPECT().Get(gomock.Eq(ctx), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
		diskClient.EXPECT().Delete(gomock.Eq(ctx), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

		result, err := d.DeleteVolume(ctx, test.req)
		if err != nil {
			checkTestError(t, test.expectedErrCode, err)
		}
		if !reflect.DeepEqual(result, test.expectedResp) {
			t.Errorf("input request: %v, DeleteVolume result: %v, expected: %v", test.req, result, test.expectedResp)
		}
	}
}

func TestControllerGetVolume(t *testing.T) {
	cntl := gomock.NewController(t)
	defer cntl.Finish()
	d, err := NewFakeDriver(cntl)
	if err != nil {
		t.Fatalf("Error getting driver: %v", err)
	}
	req := csi.ControllerGetVolumeRequest{}
	resp, err := d.ControllerGetVolume(context.Background(), &req)
	assert.Nil(t, resp)
	if !reflect.DeepEqual(err, status.Error(codes.Unimplemented, "")) {
		t.Errorf("Unexpected error: %v", err)
	}
}

func TestControllerModifyVolume(t *testing.T) {
	tests := []struct {
		desc                                    string
		req                                     *csi.ControllerModifyVolumeRequest
		oldSKU                                  *armcompute.DiskStorageAccountTypes
		expectedResp                            *csi.ControllerModifyVolumeResponse
		expectedErrCode                         codes.Code
		expectedErrmsg                          string
		expectMigrationStarted                  bool
		setupPVCMocks                           bool
		simulateRestart                         bool
		pvcExists                               bool
		pvHasMigrationLabels                    bool
		multipleMigrationsToRecover             bool
		simulateMigrationCompletion             bool
		simulateMigrationCompletionAfterRestart bool
	}{
		{
			desc: "success standard",
			req: &csi.ControllerModifyVolumeRequest{
				VolumeId: testVolumeID,
				MutableParameters: map[string]string{
					consts.DiskIOPSReadWriteField: "100",
					consts.DiskMBPSReadWriteField: "100",
				},
			},
			oldSKU:                 to.Ptr(armcompute.DiskStorageAccountTypesUltraSSDLRS),
			expectedResp:           &csi.ControllerModifyVolumeResponse{},
			expectMigrationStarted: false,
		},
		{
			desc: "fail with no volume id",
			req: &csi.ControllerModifyVolumeRequest{
				VolumeId: "",
			},
			expectedResp:           nil,
			expectedErrCode:        codes.InvalidArgument,
			expectMigrationStarted: false,
		},
		{
			desc: "fail with the invalid diskURI",
			req: &csi.ControllerModifyVolumeRequest{
				VolumeId: "123",
			},
			expectedResp:           nil,
			expectedErrCode:        codes.NotFound,
			expectMigrationStarted: false,
		},
		{
			desc: "fail with wrong disk name",
			req: &csi.ControllerModifyVolumeRequest{
				VolumeId: "/subscriptions/123",
			},
			expectedResp:           nil,
			expectedErrCode:        codes.NotFound,
			expectMigrationStarted: false,
		},
		{
			desc: "fail with wrong sku name",
			req: &csi.ControllerModifyVolumeRequest{
				VolumeId: testVolumeID,
				MutableParameters: map[string]string{
					consts.SkuNameField: "ut",
				},
			},
			expectedResp:           nil,
			expectedErrCode:        codes.InvalidArgument,
			expectMigrationStarted: false,
		},
		{
			desc: "fail with error parse parameter",
			req: &csi.ControllerModifyVolumeRequest{
				VolumeId: testVolumeID,
				MutableParameters: map[string]string{
					consts.DiskIOPSReadWriteField: "ut",
				},
			},
			expectedResp:           nil,
			expectedErrCode:        codes.InvalidArgument,
			expectMigrationStarted: false,
		},
		{
			desc: "fail with unsupported sku",
			req: &csi.ControllerModifyVolumeRequest{
				VolumeId: testVolumeID,
				MutableParameters: map[string]string{
					consts.SkuNameField:           "Premium_LRS",
					consts.DiskIOPSReadWriteField: "100",
				},
			},
			expectedResp:           nil,
			expectedErrCode:        codes.Internal,
			expectMigrationStarted: false,
		},
		{
			desc: "success SKU migration from Premium_LRS to PremiumV2_LRS",
			req: &csi.ControllerModifyVolumeRequest{
				VolumeId: testVolumeID,
				MutableParameters: map[string]string{
					consts.SkuNameField: string(armcompute.DiskStorageAccountTypesPremiumV2LRS),
				},
			},
			oldSKU:                 to.Ptr(armcompute.DiskStorageAccountTypesPremiumLRS),
			expectedResp:           &csi.ControllerModifyVolumeResponse{},
			expectMigrationStarted: true,
			pvcExists:              true,
			setupPVCMocks:          true,
		},
		{
			desc: "Migration monitor not triggered for Standard_LRS to Premium_LRS",
			req: &csi.ControllerModifyVolumeRequest{
				VolumeId: testVolumeID,
				MutableParameters: map[string]string{
					consts.SkuNameField: string(armcompute.DiskStorageAccountTypesPremiumLRS),
				},
			},
			oldSKU:                 to.Ptr(armcompute.DiskStorageAccountTypesStandardLRS),
			expectedResp:           &csi.ControllerModifyVolumeResponse{},
			expectMigrationStarted: false,
			setupPVCMocks:          true,
		},
		{
			desc: "no migration for same SKU",
			req: &csi.ControllerModifyVolumeRequest{
				VolumeId: testVolumeID,
				MutableParameters: map[string]string{
					consts.SkuNameField: string(armcompute.DiskStorageAccountTypesPremiumLRS),
				},
			},
			oldSKU:                 to.Ptr(armcompute.DiskStorageAccountTypesPremiumLRS),
			expectedResp:           &csi.ControllerModifyVolumeResponse{},
			expectMigrationStarted: false,
		},
		{
			desc: "controller restart - recover ongoing migration from annotations",
			req: &csi.ControllerModifyVolumeRequest{
				VolumeId: testVolumeID,
				MutableParameters: map[string]string{
					consts.SkuNameField: string(armcompute.DiskStorageAccountTypesPremiumV2LRS),
				},
			},
			oldSKU:                 to.Ptr(armcompute.DiskStorageAccountTypesPremiumLRS),
			expectedResp:           &csi.ControllerModifyVolumeResponse{},
			expectMigrationStarted: true,
			setupPVCMocks:          true,
			pvcExists:              true,
			simulateRestart:        true,
			pvHasMigrationLabels:   true,
		},
		{
			desc: "controller restart - no migration to recover",
			req: &csi.ControllerModifyVolumeRequest{
				VolumeId: testVolumeID,
				MutableParameters: map[string]string{
					consts.DiskIOPSReadWriteField: "3000",
				},
			},
			oldSKU:                 to.Ptr(armcompute.DiskStorageAccountTypesUltraSSDLRS),
			expectedResp:           &csi.ControllerModifyVolumeResponse{},
			expectMigrationStarted: false,
			setupPVCMocks:          true,
			pvcExists:              true,
			simulateRestart:        true,
			pvHasMigrationLabels:   false,
		},
		{
			desc: "controller restart - recover multiple ongoing migrations and cleanup on completion",
			req: &csi.ControllerModifyVolumeRequest{
				VolumeId: testVolumeID,
				MutableParameters: map[string]string{
					consts.SkuNameField: string(armcompute.DiskStorageAccountTypesPremiumV2LRS),
				},
			},
			oldSKU:                                  to.Ptr(armcompute.DiskStorageAccountTypesPremiumLRS),
			expectedResp:                            &csi.ControllerModifyVolumeResponse{},
			expectMigrationStarted:                  true,
			setupPVCMocks:                           true,
			pvcExists:                               true,
			simulateRestart:                         true,
			pvHasMigrationLabels:                    true,
			multipleMigrationsToRecover:             true,
			simulateMigrationCompletion:             true,
			simulateMigrationCompletionAfterRestart: true,
		},
	}

	for _, test := range tests {
		klog.Infof("Running test: %s", test.desc)

		//Cancel all existing mocking call expectations
		cntl := gomock.NewController(t)
		defer cntl.Finish()
		d := getFakeDriverWithKubeClient(cntl)

		// Initialize migration monitor with the fake driver's kube client
		mockEventRecorder := record.NewFakeRecorder(100)
		d.SetMigrationMonitor(NewMigrationProgressMonitor(d.getCloud().KubeClient, mockEventRecorder, d.GetDiskController()))

		ctx, cancel := context.WithCancel(context.TODO())
		defer cancel()
		id := test.req.VolumeId
		disk := &armcompute.Disk{
			ID: &id,
			SKU: &armcompute.DiskSKU{
				Name: test.oldSKU,
			},
			Properties: &armcompute.DiskProperties{},
		}

		// Simulate disk migration completion if needed
		if test.simulateMigrationCompletion {
			disk.Properties.CompletionPercent = to.Ptr(float32(100))
		}

		// Setup disk client mocks
		diskClient := mock_diskclient.NewMockInterface(cntl)
		d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()
		diskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
		diskClient.EXPECT().Patch(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()

		// Setup PVC mocks for migration monitoring if needed
		var testPVCs []*v1.PersistentVolumeClaim
		if test.setupPVCMocks {
			// Create primary test PVC
			testPVC := &v1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc-0",
					Namespace: "default",
				},
				Spec: v1.PersistentVolumeClaimSpec{
					AccessModes: []v1.PersistentVolumeAccessMode{
						v1.PersistentVolumeAccessMode("ReadWriteOnce"),
					},
					Resources: v1.VolumeResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceName("storage"): *resource.NewQuantity(10*1024*1024*1024, resource.BinarySI), // 10GiB
						},
					},
					VolumeName: "unit-test-volume",
				},
			}

			testPVCs = append(testPVCs, testPVC)

			// Create additional PVCs for multiple migration recovery test
			if test.multipleMigrationsToRecover {
				for i := 1; i <= 2; i++ {
					additionalPVC := &v1.PersistentVolumeClaim{
						ObjectMeta: metav1.ObjectMeta{
							Name:      fmt.Sprintf("test-pvc-%d", i),
							Namespace: "default",
						},
						Spec: v1.PersistentVolumeClaimSpec{
							AccessModes: []v1.PersistentVolumeAccessMode{
								v1.PersistentVolumeAccessMode("ReadWriteOnce"),
							},
							Resources: v1.VolumeResourceRequirements{
								Requests: v1.ResourceList{
									v1.ResourceName("storage"): *resource.NewQuantity(10*1024*1024*1024, resource.BinarySI), // 10GiB
								},
							},
							VolumeName: fmt.Sprintf("test-pv-%d", i),
						},
					}
					testPVCs = append(testPVCs, additionalPVC)
				}
			}

			// Setup mock expectations for PVC/PV operations
			pvcInterface := mockpersistentvolumeclaim.NewMockPersistentVolumeClaimInterface(cntl)
			mockCoreV1 := d.getCloud().KubeClient.CoreV1()

			// Mock List call for recovery scenarios
			pvcList := &v1.PersistentVolumeClaimList{Items: []v1.PersistentVolumeClaim{}}
			for _, pvc := range testPVCs {
				pvcList.Items = append(pvcList.Items, *pvc)
			}
			mockCoreV1.(*mockcorev1.MockInterface).EXPECT().PersistentVolumeClaims(gomock.Any()).Return(pvcInterface).AnyTimes()
			pvcInterface.EXPECT().List(gomock.Any(), gomock.Any()).Return(pvcList, nil).AnyTimes()

			// Mock Get and Update calls
			for _, pvc := range testPVCs {
				if test.pvcExists {
					pvcInterface.EXPECT().Get(gomock.Any(), pvc.Name, gomock.Any()).
						Return(pvc, nil).AnyTimes()

					pvcInterface.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).
						Return(pvc, nil).AnyTimes()

					volumeHandle := fmt.Sprintf(consts.ManagedDiskPath, "subs", "rg", pvc.Spec.VolumeName)

					d.getCloud().KubeClient.CoreV1().PersistentVolumes().(*mockpersistentvolume.MockInterface).EXPECT().Get(gomock.Any(), pvc.Spec.VolumeName, gomock.Any()).DoAndReturn(func(_ context.Context, name string, _ metav1.GetOptions) (*v1.PersistentVolume, error) {
						return &v1.PersistentVolume{
							ObjectMeta: metav1.ObjectMeta{
								Name: name,
							},
							Spec: v1.PersistentVolumeSpec{
								PersistentVolumeReclaimPolicy: v1.PersistentVolumeReclaimDelete,
								AccessModes: []v1.PersistentVolumeAccessMode{
									v1.ReadWriteOnce,
								},
								Capacity: v1.ResourceList{
									v1.ResourceName("storage"): *resource.NewQuantity(10*1024*1024*1024, resource.BinarySI), // 10GiB
								},
								ClaimRef: &v1.ObjectReference{
									Namespace: pvc.Namespace,
									Name:      pvc.Name,
								},
								PersistentVolumeSource: v1.PersistentVolumeSource{
									CSI: &v1.CSIPersistentVolumeSource{
										Driver:       "disk.csi.azure.com",
										VolumeHandle: volumeHandle,
									},
								},
							},
						}, nil
					}).AnyTimes()
				} else {
					pvcInterface.EXPECT().Get(gomock.Any(), pvc.Name, gomock.Any()).
						Return(nil, errors.New("not found")).AnyTimes()
				}
			}
		}

		result, err := d.ControllerModifyVolume(ctx, test.req)
		if err != nil {
			checkTestError(t, test.expectedErrCode, err)
		}
		if !reflect.DeepEqual(result, test.expectedResp) {
			t.Errorf("input request: %v, ControllerModifyVolume result: %v, expected: %v", test.req, result, test.expectedResp)
		}

		// Verify migration monitoring state
		if test.expectMigrationStarted {
			assert.True(t, d.GetMigrationMonitor().IsMigrationActive(test.req.VolumeId),
				"Migration should be active for test: %s", test.desc)

			activeMigrations := d.GetMigrationMonitor().GetActiveMigrations()
			assert.Equal(t, 1, len(activeMigrations),
				"Should have one active migration for test: %s", test.desc)

			migration, exists := activeMigrations[test.req.VolumeId]
			assert.True(t, exists, "Migration should exist for volume ID: %s", test.req.VolumeId)
			assert.Equal(t, test.req.VolumeId, migration.DiskURI)
			assert.Equal(t, testVolumeName, migration.PVName)

			// Verify SKU change details if this is a migration test
			if newSKU, exists := test.req.MutableParameters[consts.SkuNameField]; exists && test.oldSKU != nil {
				assert.Equal(t, armcompute.DiskStorageAccountTypes(newSKU), migration.ToSKU)
				assert.Equal(t, *test.oldSKU, migration.FromSKU)
			}

			// Verify migration started event was emitted for successful cases
			if test.expectedErrCode == codes.OK && test.setupPVCMocks {
				select {
				case event := <-mockEventRecorder.Events:
					assert.Contains(t, event, "Normal", "Event should be Normal type")
					assert.Contains(t, event, ReasonSKUMigrationStarted, "Event should contain migration started reason")
					assert.Contains(t, event, testVolumeName, "Event should contain PV name")
					if test.oldSKU != nil {
						assert.Contains(t, event, string(*test.oldSKU), "Event should contain source SKU")
					}
					if newSKU, exists := test.req.MutableParameters[consts.SkuNameField]; exists {
						assert.Contains(t, event, newSKU, "Event should contain target SKU")
					}
				default:
					t.Errorf("Expected migration started event was not recorded for test: %s", test.desc)
				}
			}
		} else {
			assert.False(t, d.GetMigrationMonitor().IsMigrationActive(test.req.VolumeId),
				"Migration should NOT be active for test: %s", test.desc)

			activeMigrations := d.GetMigrationMonitor().GetActiveMigrations()
			assert.Equal(t, 0, len(activeMigrations),
				"Should have no active migrations for test: %s", test.desc)

			// Verify no events were emitted for non-migration cases
			select {
			case event := <-mockEventRecorder.Events:
				// Only error if this is not a migration case or if it's an error case
				if !test.expectMigrationStarted && test.expectedErrCode == codes.OK {
					t.Errorf("Unexpected event recorded for test %s: %s", test.desc, event)
				}
			default:
				// Expected - no events should be recorded for non-migration cases
			}
		}

		// Simulate controller restart scenario
		if test.simulateRestart {

			if test.pvHasMigrationLabels {
				for _, testPVC := range testPVCs {
					if testPVC.Labels == nil {
						testPVC.Labels = make(map[string]string)
					}
					testPVC.Labels["disk.csi.azure.com/migration-in-progress"] = "true"
				}
			}

			if test.simulateMigrationCompletionAfterRestart {
				disk = &armcompute.Disk{
					ID: &id,
					SKU: &armcompute.DiskSKU{
						Name: test.oldSKU,
					},
					Properties: &armcompute.DiskProperties{},
				}
				disk.Properties.CompletionPercent = to.Ptr(float32(100))
			}

			cntlForRestart := gomock.NewController(t)
			defer cntlForRestart.Finish()
			drestart := getFakeDriverWithKubeClient(cntlForRestart)

			// Setup mock expectations for PV operations
			pvcInterface := mockpersistentvolumeclaim.NewMockPersistentVolumeClaimInterface(cntl)
			mockCoreV1 := drestart.getCloud().KubeClient.CoreV1()

			// Mock List call for recovery scenarios
			pvcList := &v1.PersistentVolumeClaimList{Items: []v1.PersistentVolumeClaim{}}
			for _, pvc := range testPVCs {
				pvcList.Items = append(pvcList.Items, *pvc)
			}
			mockCoreV1.(*mockcorev1.MockInterface).EXPECT().PersistentVolumeClaims(gomock.Any()).Return(pvcInterface).AnyTimes()
			pvcInterface.EXPECT().List(gomock.Any(), gomock.Any()).Return(pvcList, nil).AnyTimes()
			// Mock Get and Update calls
			for _, pvc := range testPVCs {
				if test.pvcExists {
					pvcInterface.EXPECT().Get(gomock.Any(), pvc.Name, gomock.Any()).
						Return(pvc, nil).AnyTimes()

					pvcInterface.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).
						Return(pvc, nil).AnyTimes()

					volumeHandle := fmt.Sprintf(consts.ManagedDiskPath, "subs", "rg", pvc.Spec.VolumeName)

					drestart.getCloud().KubeClient.CoreV1().PersistentVolumes().(*mockpersistentvolume.MockInterface).EXPECT().Get(gomock.Any(), pvc.Spec.VolumeName, gomock.Any()).DoAndReturn(func(_ context.Context, name string, _ metav1.GetOptions) (*v1.PersistentVolume, error) {
						return &v1.PersistentVolume{
							ObjectMeta: metav1.ObjectMeta{
								Name: name,
							},
							Spec: v1.PersistentVolumeSpec{
								PersistentVolumeReclaimPolicy: v1.PersistentVolumeReclaimDelete,
								AccessModes: []v1.PersistentVolumeAccessMode{
									v1.ReadWriteOnce,
								},
								Capacity: v1.ResourceList{
									v1.ResourceName("storage"): *resource.NewQuantity(10*1024*1024*1024, resource.BinarySI), // 10GiB
								},
								ClaimRef: &v1.ObjectReference{
									Namespace: pvc.Namespace,
									Name:      pvc.Name,
								},
								PersistentVolumeSource: v1.PersistentVolumeSource{
									CSI: &v1.CSIPersistentVolumeSource{
										Driver:       "disk.csi.azure.com",
										VolumeHandle: volumeHandle,
									},
								},
							},
						}, nil
					}).AnyTimes()
				} else {
					pvcInterface.EXPECT().Get(gomock.Any(), pvc.Name, gomock.Any()).
						Return(nil, errors.New("not found")).AnyTimes()
				}
			}

			// Setup disk client mocks
			diskclientForRestart := mock_diskclient.NewMockInterface(cntlForRestart)
			drestart.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskclientForRestart, nil).AnyTimes()
			diskclientForRestart.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
			diskclientForRestart.EXPECT().Patch(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()

			// Simulate controller restart by resetting migration monitor
			drestart.SetMigrationMonitor(nil)

			// Create new migration monitor (simulating controller restart)
			drestart.SetMigrationMonitor(NewMigrationProgressMonitor(drestart.getCloud().KubeClient, mockEventRecorder, drestart.GetDiskController()))

			// Simulate recovery process that would happen on controller startup
			if test.pvHasMigrationLabels {
				// Call recovery function that would be called during controller initialization
				err := drestart.RecoverMigrationMonitor(ctx)
				assert.NoError(t, err, "Recovery should succeed for test: %s", test.desc)

				activeMigrations := drestart.GetMigrationMonitor().GetActiveMigrations()
				assert.GreaterOrEqual(t, len(activeMigrations), 1, "At least one migration should be recovered")
				assert.True(t, drestart.GetMigrationMonitor().IsMigrationActive(testVolumeID), "Migration should be recovered for test: %s", test.desc)

				if test.multipleMigrationsToRecover {
					activeMigrations := drestart.GetMigrationMonitor().GetActiveMigrations()
					assert.Equal(t, 3, len(activeMigrations), "Should recover 3 migrations for test: %s", test.desc)
				}
			}

			result, err := drestart.ControllerModifyVolume(ctx, test.req)
			if err != nil {
				checkTestError(t, test.expectedErrCode, err)
			}
			if !reflect.DeepEqual(result, test.expectedResp) {
				t.Errorf("input request: %v, ControllerModifyVolume result: %v, expected: %v", test.req, result, test.expectedResp)
			}

			if drestart.GetMigrationMonitor() != nil {
				drestart.GetMigrationMonitor().Stop()
			}
		}

		// Clean up migration monitor
		if d.GetMigrationMonitor() != nil {
			d.GetMigrationMonitor().Stop()
		}
	}
}

func TestControllerPublishVolume(t *testing.T) {
	volumeCap := &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
		AccessMode: &csi.VolumeCapability_AccessMode{Mode: 2}}
	volumeCapWrong := &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
		AccessMode: &csi.VolumeCapability_AccessMode{Mode: 10}}
	cntl := gomock.NewController(t)
	defer cntl.Finish()
	d, err := NewFakeDriver(cntl)
	nodeName := "unit-test-node"
	//d.setCloud(&azure.Cloud{})
	if err != nil {
		t.Fatalf("Error getting driver: %v", err)
	}
	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "Volume ID missing",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				req := &csi.ControllerPublishVolumeRequest{}
				expectedErr := status.Error(codes.InvalidArgument, "Volume ID not provided")
				_, err := d.ControllerPublishVolume(context.Background(), req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "Volume capability missing",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				req := &csi.ControllerPublishVolumeRequest{
					VolumeId: "vol_1",
				}
				expectedErr := status.Error(codes.InvalidArgument, "Volume capability not provided")
				_, err := d.ControllerPublishVolume(context.Background(), req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "Volume capability not supported",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				req := &csi.ControllerPublishVolumeRequest{
					VolumeId:         "vol_1",
					VolumeCapability: volumeCapWrong,
				}
				expectedErr := status.Error(codes.InvalidArgument, "invalid access mode: [mount:{} access_mode:{mode:10}]")
				_, err := d.ControllerPublishVolume(context.Background(), req)
				if !reflect.DeepEqual(err, expectedErr) && !strings.Contains(err.Error(), "invalid access mode") {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "diskName error",
			testFunc: func(t *testing.T) {
				req := &csi.ControllerPublishVolumeRequest{
					VolumeId:         "vol_1",
					VolumeCapability: volumeCap,
				}
				expectedErr := status.Error(codes.NotFound, "Volume not found, failed with error: invalid URI: vol_1")
				_, err := d.ControllerPublishVolume(context.Background(), req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "NodeID missing",
			testFunc: func(t *testing.T) {
				req := &csi.ControllerPublishVolumeRequest{
					VolumeId:         testVolumeID,
					VolumeCapability: volumeCap,
				}
				id := req.VolumeId
				disk := &armcompute.Disk{
					ID: &id,
				}
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()
				diskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()

				expectedErr := status.Error(codes.InvalidArgument, "Node ID not provided")
				_, err := d.ControllerPublishVolume(context.Background(), req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "failed provisioning state",
			testFunc: func(t *testing.T) {
				req := &csi.ControllerPublishVolumeRequest{
					VolumeId:         testVolumeID,
					VolumeCapability: volumeCap,
					NodeId:           nodeName,
				}
				id := req.VolumeId
				disk := &armcompute.Disk{
					ID: &id,
				}
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()
				diskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				instanceID := fmt.Sprintf("/subscriptions/subscription/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/%s", nodeName)
				vm := armcompute.VirtualMachine{
					Name:     &nodeName,
					ID:       &instanceID,
					Location: &d.getCloud().Location,
				}
				vmstatus := []*armcompute.InstanceViewStatus{
					{
						Code: ptr.To("PowerState/Running"),
					},
					{
						Code: ptr.To("ProvisioningState/succeeded"),
					},
				}
				vm.Properties = &armcompute.VirtualMachineProperties{
					ProvisioningState: ptr.To("Failed"),
					HardwareProfile: &armcompute.HardwareProfile{
						VMSize: ptr.To(armcompute.VirtualMachineSizeTypesStandardA0),
					},
					InstanceView: &armcompute.VirtualMachineInstanceView{
						Statuses: vmstatus,
					},
					StorageProfile: &armcompute.StorageProfile{
						DataDisks: []*armcompute.DataDisk{},
					},
				}
				dataDisks := make([]*armcompute.DataDisk, 1)
				dataDisks[0] = &armcompute.DataDisk{Lun: ptr.To(int32(0)), Name: &testVolumeName}
				vm.Properties.StorageProfile.DataDisks = dataDisks
				mockVMClient := d.getCloud().ComputeClientFactory.GetVirtualMachineClient().(*mockvmclient.MockInterface)
				mockVMClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(&vm, nil).AnyTimes()
				mockVMClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("Retriable: false, RetryAfter: 0s, HTTPStatusCode: 0, RawError: error")).AnyTimes()
				expectedErr := status.Errorf(codes.Internal, "update instance \"unit-test-node\" failed with Retriable: false, RetryAfter: 0s, HTTPStatusCode: 0, RawError: error")
				_, err := d.ControllerPublishVolume(context.Background(), req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "Volume already attached success",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, err := NewFakeDriver(cntl)
				if err != nil {
					t.Fatalf("Error getting driver: %v", err)
				}
				req := &csi.ControllerPublishVolumeRequest{
					VolumeId:         testVolumeID,
					VolumeCapability: volumeCap,
					NodeId:           nodeName,
				}
				id := req.VolumeId
				disk := &armcompute.Disk{
					ID: &id,
				}
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()
				diskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				instanceID := fmt.Sprintf("/subscriptions/subscription/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/%s", nodeName)
				vm := armcompute.VirtualMachine{
					Name:     &nodeName,
					ID:       &instanceID,
					Location: &d.getCloud().Location,
				}
				vmstatus := []*armcompute.InstanceViewStatus{
					{
						Code: ptr.To("PowerState/Running"),
					},
					{
						Code: ptr.To("ProvisioningState/succeeded"),
					},
				}
				vm.Properties = &armcompute.VirtualMachineProperties{
					ProvisioningState: ptr.To("Succeeded"),
					HardwareProfile: &armcompute.HardwareProfile{
						VMSize: ptr.To(armcompute.VirtualMachineSizeTypesStandardA0),
					},
					InstanceView: &armcompute.VirtualMachineInstanceView{
						Statuses: vmstatus,
					},
					StorageProfile: &armcompute.StorageProfile{
						DataDisks: []*armcompute.DataDisk{},
					},
				}
				dataDisks := make([]*armcompute.DataDisk, 1)
				dataDisks[0] = &armcompute.DataDisk{Lun: ptr.To(int32(0)), Name: &testVolumeName}
				vm.Properties.StorageProfile.DataDisks = dataDisks
				mockVMClient := d.getCloud().ComputeClientFactory.GetVirtualMachineClient().(*mockvmclient.MockInterface)
				mockVMClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(&vm, nil).AnyTimes()
				_, err = d.ControllerPublishVolume(context.Background(), req)
				if !reflect.DeepEqual(err, nil) {
					t.Errorf("actualErr: (%v), expectedErr: (<nil>)", err)
				}
			},
		},
		{
			name: "CachingMode Error",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, err := NewFakeDriver(cntl)
				if err != nil {
					t.Fatalf("Error getting driver: %v", err)
				}
				volumeContext := make(map[string]string)
				volumeContext[consts.CachingModeField] = "badmode"
				req := &csi.ControllerPublishVolumeRequest{
					VolumeId:         testVolumeID,
					VolumeCapability: volumeCap,
					NodeId:           nodeName,
					VolumeContext:    volumeContext,
				}
				id := req.VolumeId
				disk := &armcompute.Disk{
					ID: &id,
				}
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()
				diskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				instanceID := fmt.Sprintf("/subscriptions/subscription/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/%s", nodeName)
				vm := armcompute.VirtualMachine{
					Name:     &nodeName,
					ID:       &instanceID,
					Location: &d.getCloud().Location,
				}
				vmstatus := []*armcompute.InstanceViewStatus{
					{
						Code: ptr.To("PowerState/Running"),
					},
					{
						Code: ptr.To("ProvisioningState/succeeded"),
					},
				}
				vm.Properties = &armcompute.VirtualMachineProperties{
					ProvisioningState: ptr.To("Succeeded"),
					HardwareProfile: &armcompute.HardwareProfile{
						VMSize: ptr.To(armcompute.VirtualMachineSizeTypesStandardA0),
					},
					InstanceView: &armcompute.VirtualMachineInstanceView{
						Statuses: vmstatus,
					},
					StorageProfile: &armcompute.StorageProfile{
						DataDisks: []*armcompute.DataDisk{},
					},
				}
				mockVMClient := d.getCloud().ComputeClientFactory.GetVirtualMachineClient().(*mockvmclient.MockInterface)
				mockVMClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(&vm, nil).AnyTimes()
				expectedErr := status.Errorf(codes.Internal, "azureDisk - badmode is not supported cachingmode. Supported values are [None ReadOnly ReadWrite]")
				_, err = d.ControllerPublishVolume(context.Background(), req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (<nil>)", err)
				}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func TestControllerUnpublishVolume(t *testing.T) {
	cntl := gomock.NewController(t)
	defer cntl.Finish()
	d, err := NewFakeDriver(cntl)
	if err != nil {
		t.Fatalf("Error getting driver: %v", err)
	}
	d.setCloud(&azure.Cloud{})
	if err != nil {
		t.Fatalf("Error getting driver: %v", err)
	}
	tests := []struct {
		desc        string
		req         *csi.ControllerUnpublishVolumeRequest
		expectedErr error
	}{
		{
			desc:        "Volume ID missing",
			req:         &csi.ControllerUnpublishVolumeRequest{},
			expectedErr: status.Error(codes.InvalidArgument, "Volume ID not provided"),
		},
		{
			desc: "Node ID missing",
			req: &csi.ControllerUnpublishVolumeRequest{
				VolumeId: "vol_1",
			},
			expectedErr: status.Error(codes.InvalidArgument, "Node ID not provided"),
		},
		{
			desc: "DiskName error",
			req: &csi.ControllerUnpublishVolumeRequest{
				VolumeId: "vol_1",
				NodeId:   "unit-test-node",
			},
			expectedErr: status.Errorf(codes.Internal, "invalid URI: vol_1"),
		},
	}
	for _, test := range tests {
		_, err := d.ControllerUnpublishVolume(context.Background(), test.req)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
		}
	}
}

func TestControllerGetCapabilities(t *testing.T) {
	cntl := gomock.NewController(t)
	defer cntl.Finish()
	d, _ := NewFakeDriver(cntl)
	capType := &csi.ControllerServiceCapability_Rpc{
		Rpc: &csi.ControllerServiceCapability_RPC{
			Type: csi.ControllerServiceCapability_RPC_UNKNOWN,
		},
	}
	capList := []*csi.ControllerServiceCapability{{
		Type: capType,
	}}
	d.setControllerCapabilities(capList)
	// Test valid request
	req := csi.ControllerGetCapabilitiesRequest{}
	resp, err := d.ControllerGetCapabilities(context.Background(), &req)
	assert.NotNil(t, resp)
	assert.Equal(t, resp.Capabilities[0].GetType(), capType)
	assert.NoError(t, err)
}

func TestControllerExpandVolume(t *testing.T) {
	stdVolSize := int64(5 * 1024 * 1024 * 1024)
	stdCapRange := &csi.CapacityRange{RequiredBytes: stdVolSize}

	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "Volume ID missing",
			testFunc: func(t *testing.T) {
				req := &csi.ControllerExpandVolumeRequest{}

				ctx := context.Background()
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)

				expectedErr := status.Error(codes.InvalidArgument, "Volume ID missing in the request")
				_, err := d.ControllerExpandVolume(ctx, req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("Unexpected error: %v", err)
				}
			},
		},
		{
			name: "Volume capabilities missing",
			testFunc: func(t *testing.T) {
				req := &csi.ControllerExpandVolumeRequest{
					VolumeId: "vol_1",
				}

				ctx := context.Background()
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				var csc []*csi.ControllerServiceCapability
				d.setControllerCapabilities(csc)
				expectedErr := status.Error(codes.InvalidArgument, "invalid expand volume request: volume_id:\"vol_1\"")
				_, err := d.ControllerExpandVolume(ctx, req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("Unexpected error: %v", err)
				}
			},
		},
		{
			name: "Volume Capacity range missing",
			testFunc: func(t *testing.T) {
				req := &csi.ControllerExpandVolumeRequest{
					VolumeId: "vol_1",
				}

				ctx := context.Background()
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)

				expectedErr := status.Error(codes.InvalidArgument, "volume capacity range missing in request")
				_, err := d.ControllerExpandVolume(ctx, req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("Unexpected error: %v", err)
				}
			},
		},
		{
			name: "disk type is not managedDisk",
			testFunc: func(t *testing.T) {
				req := &csi.ControllerExpandVolumeRequest{
					VolumeId:      "httptest",
					CapacityRange: stdCapRange,
				}
				ctx := context.Background()
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)

				expectedErr := status.Error(codes.Internal, "GetDiskByURI(httptest) failed with error(invalid URI: httptest)")
				_, err := d.ControllerExpandVolume(ctx, req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("Unexpected error: %v", err)
				}
			},
		},
		{
			name: "Disk URI not valid",
			testFunc: func(t *testing.T) {
				req := &csi.ControllerExpandVolumeRequest{
					VolumeId:      "vol_1",
					CapacityRange: stdCapRange,
				}

				ctx := context.Background()
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)

				expectedErr := status.Errorf(codes.Internal, "GetDiskByURI(vol_1) failed with error(invalid URI: vol_1)")
				_, err := d.ControllerExpandVolume(ctx, req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "DiskSize missing",
			testFunc: func(t *testing.T) {
				req := &csi.ControllerExpandVolumeRequest{
					VolumeId:      testVolumeID,
					CapacityRange: stdCapRange,
				}
				id := req.VolumeId
				diskProperties := armcompute.DiskProperties{}
				disk := &armcompute.Disk{
					ID:         &id,
					Properties: &diskProperties,
				}
				ctx := context.Background()
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()
				diskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				expectedErr := status.Errorf(codes.Internal, "could not get size of the disk(/subscriptions/subs/resourceGroups/rg/providers/Microsoft.Compute/disks/unit-test-volume)")
				_, err := d.ControllerExpandVolume(ctx, req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func TestCreateSnapshot(t *testing.T) {

	t.Logf("Wait for snapshot ready is set")
	RunTestCreateSnapshot(t, func(t *gomock.Controller) (FakeDriver, error) {
		return NewFakeDriver(t)
	})
	t.Logf("Wait for snapshot ready is cleared")
	RunTestCreateSnapshot(t, func(t *gomock.Controller) (FakeDriver, error) {
		driver, err := NewFakeDriver(t)
		if err != nil {
			return nil, err
		}
		driver.SetWaitForSnapshotReady(false)
		return driver, nil
	})
}

func RunTestCreateSnapshot(t *testing.T, fakeDriverFn func(t *gomock.Controller) (FakeDriver, error)) {
	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "Source volume ID missing",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := fakeDriverFn(cntl)
				req := &csi.CreateSnapshotRequest{}
				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "CreateSnapshot Source Volume ID must be provided")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "Snapshot name missing",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := fakeDriverFn(cntl)
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: "vol_1"}
				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "snapshot name must be provided")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "Invalid parameter option",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := fakeDriverFn(cntl)
				parameter := make(map[string]string)
				parameter["unit-test"] = "test"
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: "vol_1",
					Name:           "snapname",
					Parameters:     parameter,
				}

				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := status.Errorf(codes.Internal, "AzureDisk - invalid option unit-test in VolumeSnapshotClass")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "Invalid volume ID",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := fakeDriverFn(cntl)
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: "vol_1",
					Name:           "snapname",
				}
				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := status.Errorf(codes.InvalidArgument, "could not get resource group from diskURI(vol_1) with error(invalid URI: vol_1)")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "Invalid tag ",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := fakeDriverFn(cntl)
				d.setCloud(&azure.Cloud{})
				parameter := make(map[string]string)
				parameter["tags"] = "unit-test"
				parameter[consts.IncrementalField] = "false"
				parameter[consts.ResourceGroupField] = "test"
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: testVolumeID,
					Name:           "snapname",
					Parameters:     parameter,
				}

				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := status.Errorf(codes.InvalidArgument, "tags 'unit-test' are invalid, the format should like: 'key1=value1,key2=value2'")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "invalid data access auth mode ",
			testFunc: func(t *testing.T) {
				parameter := make(map[string]string)
				parameter["dataaccessauthmode"] = "Invalid"
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: testVolumeID,
					Name:           "snapname",
					Parameters:     parameter,
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := fakeDriverFn(cntl)
				d.setCloud(&azure.Cloud{})

				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := status.Errorf(codes.InvalidArgument, "dataAccessAuthMode(Invalid) is not supported")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "cross region non-incremental error ",
			testFunc: func(t *testing.T) {
				parameter := make(map[string]string)
				parameter["location"] = "eastus"
				parameter["incremental"] = "false"
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: testVolumeID,
					Name:           "snapname",
					Parameters:     parameter,
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := fakeDriverFn(cntl)

				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := status.Errorf(codes.InvalidArgument, "could not create snapshot cross region with incremental is false")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "get snapshot client error ",
			testFunc: func(t *testing.T) {
				parameter := make(map[string]string)
				parameter["SubscriptionID"] = "1"
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: testVolumeID,
					Name:           "snapname",
					Parameters:     parameter,
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := fakeDriverFn(cntl)
				d.setCloud(&azure.Cloud{})
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetSnapshotClientForSub(gomock.Any()).Return(nil, fmt.Errorf("test")).AnyTimes()

				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := status.Errorf(codes.Internal, "could not get snapshot client for subscription(1) with error(test)")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "create snapshot error ",
			testFunc: func(t *testing.T) {
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: testVolumeID,
					Name:           "snapname",
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := fakeDriverFn(cntl)
				d.setCloud(&azure.Cloud{})
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mock_snapshotclient.NewMockInterface(ctrl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetSnapshotClientForSub(gomock.Any()).Return(mockSnapshotClient, nil).AnyTimes()
				mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("test")).AnyTimes()
				mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("test")).AnyTimes()

				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := status.Errorf(codes.Internal, "create snapshot error: test")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "create snapshot already exist ",
			testFunc: func(t *testing.T) {
				parameter := make(map[string]string)
				parameter["tags"] = "unit=test"
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: testVolumeID,
					Name:           "snapname",
					Parameters:     parameter,
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := fakeDriverFn(cntl)
				d.setCloud(&azure.Cloud{})
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mock_snapshotclient.NewMockInterface(ctrl)
				mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetSnapshotClientForSub(gomock.Any()).Return(mockSnapshotClient, nil).AnyTimes()
				mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("existing disk")).AnyTimes()
				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := status.Errorf(codes.AlreadyExists, "request snapshot(snapname) under rg(rg) already exists, but the SourceVolumeId is different, error details: existing disk")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "create snapshot already exist - waits for snapshot ready",
			testFunc: func(t *testing.T) {
				parameter := make(map[string]string)
				parameter["tags"] = "unit=test"
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: testVolumeID,
					Name:           "snapname",
					Parameters:     parameter,
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := fakeDriverFn(cntl)
				d.setCloud(&azure.Cloud{})
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mock_snapshotclient.NewMockInterface(ctrl)
				timeCreated := ptr.To(time.Now())

				snapshotNotProvisioned := &armcompute.Snapshot{
					Properties: &armcompute.SnapshotProperties{
						CreationData:      &armcompute.CreationData{SourceResourceID: &req.SourceVolumeId},
						TimeCreated:       timeCreated,
						DiskSizeGB:        ptr.To[int32](5),
						ProvisioningState: ptr.To("Updating"),
						CompletionPercent: ptr.To[float32](0),
					},
					ID:   ptr.To("subscriptions/subscription/resourceGroups/rg/providers/Microsoft.Compute/snapshots/snapname"),
					Name: ptr.To("snapname"),
				}
				snapshotProvisioned := &armcompute.Snapshot{
					Properties: &armcompute.SnapshotProperties{
						CreationData:      &armcompute.CreationData{SourceResourceID: &req.SourceVolumeId},
						TimeCreated:       timeCreated,
						DiskSizeGB:        ptr.To[int32](5),
						CompletionPercent: ptr.To[float32](0),
						ProvisioningState: ptr.To("succeeded"),
					},
					ID:   ptr.To("subscriptions/subscription/resourceGroups/rg/providers/Microsoft.Compute/snapshots/snapname"),
					Name: ptr.To("snapname"),
				}
				snapshotComplete := &armcompute.Snapshot{
					Properties: &armcompute.SnapshotProperties{
						CreationData:      &armcompute.CreationData{SourceResourceID: &req.SourceVolumeId},
						CompletionPercent: ptr.To[float32](100),
						ProvisioningState: ptr.To("succeeded"),
						TimeCreated:       timeCreated,
						DiskSizeGB:        ptr.To[int32](5),
					},
					ID:   ptr.To("subscriptions/subscription/resourceGroups/rg/providers/Microsoft.Compute/snapshots/snapname"),
					Name: ptr.To("snapname"),
				}

				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetSnapshotClientForSub(gomock.Any()).Return(mockSnapshotClient, nil).AnyTimes()
				if d.GetWaitForSnapshotReady() {
					mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshotNotProvisioned, nil).Times(3)
					mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshotProvisioned, nil).Times(2)
					mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshotComplete, nil).Times(2)
				} else {
					mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshotNotProvisioned, nil).Times(4)
					mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshotProvisioned, nil).Times(2)
					mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshotComplete, nil).Times(2)
				}
				resp, err := d.CreateSnapshot(context.Background(), req)
				if err == nil && !d.GetWaitForSnapshotReady() {
					for range 3 {
						resp, err = d.CreateSnapshot(context.Background(), req) // retry without waiting for snapshot ready
						if err != nil {
							break
						}
					}
				}
				if !reflect.DeepEqual(err, nil) {
					t.Errorf("actualErr: (%v), expectedErr: nil", err)
				} else if !resp.Snapshot.ReadyToUse {
					t.Errorf("Snapshot not ready to use, expected: true, got: %v", resp.Snapshot.ReadyToUse)
				}
			},
		},
		{
			name: "Wait snapshot ready error ",
			testFunc: func(t *testing.T) {
				parameter := make(map[string]string)
				parameter["tags"] = "unit=test"
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: testVolumeID,
					Name:           "unit-test",
					Parameters:     parameter,
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := fakeDriverFn(cntl)
				d.setCloud(&azure.Cloud{})
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mock_snapshotclient.NewMockInterface(ctrl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetSnapshotClientForSub(gomock.Any()).Return(mockSnapshotClient, nil).AnyTimes()

				snapshot := &armcompute.Snapshot{}
				mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
				mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshot, fmt.Errorf("get snapshot error")).AnyTimes()
				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := status.Errorf(codes.Internal, "waitForSnapshotReady(, rg, unit-test) failed with get snapshot error")
				if !d.GetWaitForSnapshotReady() {
					expectedErr = status.Errorf(codes.Internal, "get snapshot unit-test from rg(rg) error: get snapshot error")
				}
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "Get snapshot ID error ",
			testFunc: func(t *testing.T) {
				parameter := make(map[string]string)
				parameter["tags"] = "unit=test"
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: testVolumeID,
					Name:           "unit-test",
					Parameters:     parameter,
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := fakeDriverFn(cntl)
				d.setCloud(&azure.Cloud{})
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mock_snapshotclient.NewMockInterface(ctrl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetSnapshotClientForSub(gomock.Any()).Return(mockSnapshotClient, nil).AnyTimes()

				provisioningState := "succeeded"
				DiskSize := int32(10)
				snapshotID := "test"
				snapshot := &armcompute.Snapshot{
					Properties: &armcompute.SnapshotProperties{
						TimeCreated:       &time.Time{},
						ProvisioningState: &provisioningState,
						DiskSizeGB:        &DiskSize,
					},
					ID: &snapshotID,
				}
				mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1)
				mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
				if d.GetWaitForSnapshotReady() {
					gomock.InOrder(
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshot, nil).Times(1),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("get snapshot error")).AnyTimes(),
					)
				} else {
					gomock.InOrder(
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("get snapshot error")).AnyTimes(),
					)
				}
				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := status.Errorf(codes.Internal, "get snapshot unit-test from rg(rg) error: get snapshot error")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "create snapshot error - cross region",
			testFunc: func(t *testing.T) {
				parameter := make(map[string]string)
				parameter["tags"] = "unit=test"
				parameter["location"] = "eastus"
				parameter["incremental"] = "true"
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: testVolumeID,
					Name:           "snapname",
					Parameters:     parameter,
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := fakeDriverFn(cntl)
				d.setCloud(&azure.Cloud{})
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mock_snapshotclient.NewMockInterface(ctrl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetSnapshotClientForSub(gomock.Any()).Return(mockSnapshotClient, nil).AnyTimes()
				provisioningState := "succeeded"
				DiskSize := int32(10)
				snapshotID := "test"
				snapshot := &armcompute.Snapshot{
					Properties: &armcompute.SnapshotProperties{
						TimeCreated:       &time.Time{},
						ProvisioningState: &provisioningState,
						DiskSizeGB:        &DiskSize,
					},
					ID: &snapshotID,
				}
				if d.GetWaitForSnapshotReady() {
					gomock.InOrder(
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1),
						mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshot, nil).Times(2),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1),
						mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("test")).Times(1),
					)
				} else {
					gomock.InOrder(
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1),
						mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshot, nil).Times(1),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1),
						mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("test")).Times(1),
					)
				}
				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := status.Errorf(codes.Internal, "create snapshot error: test")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "create snapshot already exist - cross region",
			testFunc: func(t *testing.T) {
				parameter := make(map[string]string)
				parameter["tags"] = "unit=test"
				parameter["location"] = "eastus"
				parameter["incremental"] = "true"
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: testVolumeID,
					Name:           "snapname",
					Parameters:     parameter,
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := fakeDriverFn(cntl)
				d.setCloud(&azure.Cloud{})
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mock_snapshotclient.NewMockInterface(ctrl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetSnapshotClientForSub(gomock.Any()).Return(mockSnapshotClient, nil).AnyTimes()
				provisioningState := "succeeded"
				DiskSize := int32(10)
				snapshotID := "test"
				snapshot := &armcompute.Snapshot{
					Properties: &armcompute.SnapshotProperties{
						TimeCreated:       &time.Time{},
						ProvisioningState: &provisioningState,
						DiskSizeGB:        &DiskSize,
					},
					ID: &snapshotID,
				}
				if d.GetWaitForSnapshotReady() {
					gomock.InOrder(
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1),
						mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshot, nil).Times(2),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1),
						mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("existing disk")).Times(1),
					)
				} else {
					gomock.InOrder(
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1),
						mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshot, nil).Times(1),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1),
						mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("existing disk")).Times(1),
					)
				}
				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := status.Errorf(codes.AlreadyExists, "request snapshot(snapname) under rg(rg) already exists, but the SourceVolumeId is different, error details: existing disk")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "Wait snapshot ready error - cross region",
			testFunc: func(t *testing.T) {
				parameter := make(map[string]string)
				parameter["tags"] = "unit=test"
				parameter["location"] = "eastus"
				parameter["incremental"] = "true"
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: testVolumeID,
					Name:           "unit-test",
					Parameters:     parameter,
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := fakeDriverFn(cntl)
				d.setCloud(&azure.Cloud{})
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mock_snapshotclient.NewMockInterface(ctrl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetSnapshotClientForSub(gomock.Any()).Return(mockSnapshotClient, nil).AnyTimes()

				provisioningState := "succeeded"
				DiskSize := int32(10)
				snapshotID := "test"
				snapshot := &armcompute.Snapshot{
					Properties: &armcompute.SnapshotProperties{
						TimeCreated:       &time.Time{},
						ProvisioningState: &provisioningState,
						DiskSizeGB:        &DiskSize,
					},
					ID: &snapshotID,
				}
				mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1)
				mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
				if d.GetWaitForSnapshotReady() {
					gomock.InOrder(
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshot, nil).Times(2),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("get snapshot error")).AnyTimes(),
					)
				} else {
					gomock.InOrder(
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshot, nil).Times(1),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("get snapshot error")).AnyTimes(),
					)
				}
				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := status.Errorf(codes.Internal, "waitForSnapshotReady(, rg, unit-test) failed with get snapshot error")
				if !d.GetWaitForSnapshotReady() {
					expectedErr = status.Errorf(codes.Internal, "rpc error: code = Internal desc = get snapshot unit-test from rg(rg) error: get snapshot error")
				}
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "Get snapshot ID error - cross region",
			testFunc: func(t *testing.T) {
				parameter := make(map[string]string)
				parameter["tags"] = "unit=test"
				parameter["location"] = "eastus"
				parameter["incremental"] = "true"
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: testVolumeID,
					Name:           "unit-test",
					Parameters:     parameter,
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := fakeDriverFn(cntl)
				d.setCloud(&azure.Cloud{})
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mock_snapshotclient.NewMockInterface(ctrl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetSnapshotClientForSub(gomock.Any()).Return(mockSnapshotClient, nil).AnyTimes()

				provisioningState := "succeeded"
				DiskSize := int32(10)
				snapshotID := "test"
				snapshot := &armcompute.Snapshot{
					Properties: &armcompute.SnapshotProperties{
						TimeCreated:       &time.Time{},
						ProvisioningState: &provisioningState,
						DiskSizeGB:        &DiskSize,
					},
					ID: &snapshotID,
				}
				mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1)
				mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
				if d.GetWaitForSnapshotReady() {
					gomock.InOrder(
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshot, nil).Times(2),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshot, nil).Times(1),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("get snapshot error")).AnyTimes(),
					)
				} else {
					gomock.InOrder(
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshot, nil).Times(1),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("get snapshot error")).AnyTimes(),
					)
				}
				mockSnapshotClient.EXPECT().Delete(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := status.Errorf(codes.Internal, "rpc error: code = Internal desc = get snapshot unit-test from rg(rg) error: get snapshot error")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "valid request ",
			testFunc: func(t *testing.T) {
				parameter := make(map[string]string)
				parameter["tags"] = "unit=test"
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: testVolumeID,
					Name:           "testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name",
					Parameters:     parameter,
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := fakeDriverFn(cntl)
				d.setCloud(&azure.Cloud{})
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mock_snapshotclient.NewMockInterface(ctrl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetSnapshotClientForSub(gomock.Any()).Return(mockSnapshotClient, nil).AnyTimes()
				provisioningState := "succeeded"
				DiskSize := int32(10)
				snapshotID := "test"
				snapshot := &armcompute.Snapshot{
					Properties: &armcompute.SnapshotProperties{
						TimeCreated:       &time.Time{},
						ProvisioningState: &provisioningState,
						DiskSizeGB:        &DiskSize,
					},
					ID: &snapshotID,
				}

				mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
				mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshot, nil).AnyTimes()
				actualresponse, err := d.CreateSnapshot(context.Background(), req)
				tp := timestamppb.New(*snapshot.Properties.TimeCreated)
				ready := true
				expectedresponse := &csi.CreateSnapshotResponse{
					Snapshot: &csi.Snapshot{
						SizeBytes:      volumehelper.GiBToBytes(int64(*snapshot.Properties.DiskSizeGB)),
						SnapshotId:     *snapshot.ID,
						SourceVolumeId: req.SourceVolumeId,
						CreationTime:   tp,
						ReadyToUse:     ready,
					},
				}
				if !reflect.DeepEqual(expectedresponse, actualresponse) || err != nil {
					t.Errorf("actualresponse: (%+v), expectedresponse: (%+v)\n", actualresponse, expectedresponse)
					t.Errorf("err:%v", err)
				}
			},
		},
		{
			name: "valid request - set optional parameter",
			testFunc: func(t *testing.T) {
				parameter := make(map[string]string)
				parameter["tags"] = "unit=test"
				parameter["dataaccessauthmode"] = "None"
				parameter["tagvaluedelimiter"] = ","
				parameter["useragent"] = "ut"
				parameter["csi.storage.k8s.io/volumesnapshot/name"] = "VolumeSnapshotNameKeyPlaceholder"
				parameter["csi.storage.k8s.io/volumesnapshot/namespace"] = "VolumeSnapshotNamespaceKeyPlaceholder"
				parameter["csi.storage.k8s.io/volumesnapshotcontent/name"] = "VolumeSnapshotContentNameKeyPlaceholder"
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: testVolumeID,
					Name:           "testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name",
					Parameters:     parameter,
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := fakeDriverFn(cntl)
				d.setCloud(azure.GetTestCloudWithExtendedLocation(cntl))
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mock_snapshotclient.NewMockInterface(ctrl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetSnapshotClientForSub(gomock.Any()).Return(mockSnapshotClient, nil).AnyTimes()
				provisioningState := "succeeded"
				DiskSize := int32(10)
				snapshotID := "test"
				snapshot := &armcompute.Snapshot{
					Properties: &armcompute.SnapshotProperties{
						TimeCreated:       &time.Time{},
						ProvisioningState: &provisioningState,
						DiskSizeGB:        &DiskSize,
					},
					ID: &snapshotID,
				}

				mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
				mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshot, nil).AnyTimes()
				actualresponse, err := d.CreateSnapshot(context.Background(), req)
				tp := timestamppb.New(*snapshot.Properties.TimeCreated)
				ready := true
				expectedresponse := &csi.CreateSnapshotResponse{
					Snapshot: &csi.Snapshot{
						SizeBytes:      volumehelper.GiBToBytes(int64(*snapshot.Properties.DiskSizeGB)),
						SnapshotId:     *snapshot.ID,
						SourceVolumeId: req.SourceVolumeId,
						CreationTime:   tp,
						ReadyToUse:     ready,
					},
				}
				if !reflect.DeepEqual(expectedresponse, actualresponse) || err != nil {
					t.Errorf("actualresponse: (%+v), expectedresponse: (%+v)\n", actualresponse, expectedresponse)
					t.Errorf("err:%v", err)
				}
			},
		},
		{
			name: "valid request - azure stack",
			testFunc: func(t *testing.T) {
				parameter := make(map[string]string)
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: testVolumeID,
					Name:           "testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name",
					Parameters:     parameter,
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := fakeDriverFn(cntl)

				az := azure.GetTestCloud(cntl)
				az.Config.Cloud = "AZURESTACKCLOUD"
				d.setCloud(az)
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mock_snapshotclient.NewMockInterface(ctrl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetSnapshotClientForSub(gomock.Any()).Return(mockSnapshotClient, nil).AnyTimes()
				provisioningState := "succeeded"
				DiskSize := int32(10)
				snapshotID := "test"
				snapshot := &armcompute.Snapshot{
					Properties: &armcompute.SnapshotProperties{
						TimeCreated:       &time.Time{},
						ProvisioningState: &provisioningState,
						DiskSizeGB:        &DiskSize,
					},
					ID: &snapshotID,
				}

				mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
				mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshot, nil).AnyTimes()
				actualresponse, err := d.CreateSnapshot(context.Background(), req)
				tp := timestamppb.New(*snapshot.Properties.TimeCreated)
				ready := true
				expectedresponse := &csi.CreateSnapshotResponse{
					Snapshot: &csi.Snapshot{
						SizeBytes:      volumehelper.GiBToBytes(int64(*snapshot.Properties.DiskSizeGB)),
						SnapshotId:     *snapshot.ID,
						SourceVolumeId: req.SourceVolumeId,
						CreationTime:   tp,
						ReadyToUse:     ready,
					},
				}
				if !reflect.DeepEqual(expectedresponse, actualresponse) || err != nil {
					t.Errorf("actualresponse: (%+v), expectedresponse: (%+v)\n", actualresponse, expectedresponse)
					t.Errorf("err:%v", err)
				}
			},
		},
		{
			name: "valid request - cross region",
			testFunc: func(t *testing.T) {
				parameter := make(map[string]string)
				parameter["location"] = "eastus"
				parameter["incremental"] = "true"
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: testVolumeID,
					Name:           "testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name",
					Parameters:     parameter,
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := fakeDriverFn(cntl)
				d.setCloud(&azure.Cloud{})
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mock_snapshotclient.NewMockInterface(ctrl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetSnapshotClientForSub(gomock.Any()).Return(mockSnapshotClient, nil).AnyTimes()
				provisioningState := "succeeded"
				DiskSize := int32(10)
				snapshotID := "test"
				snapshot := &armcompute.Snapshot{
					Properties: &armcompute.SnapshotProperties{
						TimeCreated:       &time.Time{},
						ProvisioningState: &provisioningState,
						DiskSizeGB:        &DiskSize,
					},
					ID: &snapshotID,
				}

				mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
				mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshot, nil).AnyTimes()
				mockSnapshotClient.EXPECT().Delete(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				actualresponse, err := d.CreateSnapshot(context.Background(), req)
				tp := timestamppb.New(*snapshot.Properties.TimeCreated)
				ready := true
				expectedresponse := &csi.CreateSnapshotResponse{
					Snapshot: &csi.Snapshot{
						SizeBytes:      volumehelper.GiBToBytes(int64(*snapshot.Properties.DiskSizeGB)),
						SnapshotId:     *snapshot.ID,
						SourceVolumeId: req.SourceVolumeId,
						CreationTime:   tp,
						ReadyToUse:     ready,
					},
				}
				if !reflect.DeepEqual(expectedresponse, actualresponse) || err != nil {
					t.Errorf("actualresponse: (%+v), expectedresponse: (%+v)\n", actualresponse, expectedresponse)
					t.Errorf("err:%v", err)
				}
			},
		},
		{
			name: "valid request snapshots taking time - cross region",
			testFunc: func(t *testing.T) {
				parameter := make(map[string]string)
				parameter["location"] = "eastus"
				parameter["incremental"] = "true"
				snapshotName := "snapshotname"
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: testVolumeID,
					Name:           snapshotName,
					Parameters:     parameter,
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := fakeDriverFn(cntl)
				d.setCloud(&azure.Cloud{})
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mock_snapshotclient.NewMockInterface(ctrl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetSnapshotClientForSub(gomock.Any()).Return(mockSnapshotClient, nil).AnyTimes()
				DiskSize := int32(10)
				localSnapshotName := fmt.Sprintf("local_%s", snapshotName)
				snapshotURI := "/subscriptions/23/providers/Microsoft.Compute/snapshots/"
				snapshot := &armcompute.Snapshot{
					Properties: &armcompute.SnapshotProperties{
						TimeCreated: &time.Time{},
						DiskSizeGB:  &DiskSize,
					},
					ID: ptr.To(fmt.Sprintf("%s%s", snapshotURI, snapshotName)),
				}

				if d.GetWaitForSnapshotReady() {
					mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
					gomock.InOrder(
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ string, _ string) (*armcompute.Snapshot, error) {
							snapshot.ID = ptr.To(fmt.Sprintf("%s%s", snapshotURI, localSnapshotName))
							snapshot.Name = ptr.To(localSnapshotName)
							snapshot.Properties.ProvisioningState = ptr.To("updating")
							snapshot.Properties.CompletionPercent = ptr.To(float32(0.0))
							return snapshot, nil
						}).Times(1),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ string, _ string) (*armcompute.Snapshot, error) {
							snapshot.ID = ptr.To(fmt.Sprintf("%s%s", snapshotURI, localSnapshotName))
							snapshot.Name = ptr.To(localSnapshotName)
							snapshot.Properties.ProvisioningState = ptr.To("succeeded")
							snapshot.Properties.CompletionPercent = ptr.To(float32(100.0))
							return snapshot, nil
						}).Times(2),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ string, _ string) (*armcompute.Snapshot, error) {
							snapshot.ID = ptr.To(fmt.Sprintf("%s%s", snapshotURI, snapshotName))
							snapshot.Name = ptr.To(snapshotName)
							snapshot.Properties.ProvisioningState = ptr.To("updating")
							snapshot.Properties.CompletionPercent = ptr.To(float32(0.0))
							return snapshot, nil
						}).Times(1),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ string, _ string) (*armcompute.Snapshot, error) {
							snapshot.ID = ptr.To(fmt.Sprintf("%s%s", snapshotURI, snapshotName))
							snapshot.Name = ptr.To(snapshotName)
							snapshot.Properties.ProvisioningState = ptr.To("succeeded")
							snapshot.Properties.CompletionPercent = ptr.To(float32(100.0))
							return snapshot, nil
						}).Times(2),
					)
					mockSnapshotClient.EXPECT().Delete(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(1)
				} else {
					mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
					gomock.InOrder(
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ string, _ string) (*armcompute.Snapshot, error) {
							snapshot.ID = ptr.To(fmt.Sprintf("%s%s", snapshotURI, localSnapshotName))
							snapshot.Name = ptr.To(localSnapshotName)
							snapshot.Properties.ProvisioningState = ptr.To("updating")
							return snapshot, nil
						}).Times(1),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ string, _ string) (*armcompute.Snapshot, error) {
							snapshot.ID = ptr.To(fmt.Sprintf("%s%s", snapshotURI, localSnapshotName))
							snapshot.Name = ptr.To(localSnapshotName)
							snapshot.Properties.ProvisioningState = ptr.To("succeeded")
							return snapshot, nil
						}).Times(2),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).Times(1),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ string, _ string) (*armcompute.Snapshot, error) {
							snapshot.ID = ptr.To(fmt.Sprintf("%s%s", snapshotURI, snapshotName))
							snapshot.Name = ptr.To(snapshotName)
							snapshot.Properties.ProvisioningState = ptr.To("updating")
							return snapshot, nil
						}).Times(1),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ string, _ string) (*armcompute.Snapshot, error) {
							snapshot.ID = ptr.To(fmt.Sprintf("%s%s", snapshotURI, localSnapshotName))
							snapshot.Name = ptr.To(localSnapshotName)
							snapshot.Properties.ProvisioningState = ptr.To("succeeded")
							return snapshot, nil
						}).Times(2),
						mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ string, _ string) (*armcompute.Snapshot, error) {
							snapshot.ID = ptr.To(fmt.Sprintf("%s%s", snapshotURI, snapshotName))
							snapshot.Name = ptr.To(snapshotName)
							snapshot.Properties.ProvisioningState = ptr.To("succeeded")
							return snapshot, nil
						}).Times(2),
					)
					mockSnapshotClient.EXPECT().Delete(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(1)
				}

				actualresponse, err := d.CreateSnapshot(context.Background(), req)
				if err == nil && !actualresponse.Snapshot.ReadyToUse {
					for range 2 {
						if actualresponse.Snapshot.SnapshotId != fmt.Sprintf("%s%s", snapshotURI, snapshotName) {
							err = fmt.Errorf("snapshot ID mismatch")
						} else {
							actualresponse, err = d.CreateSnapshot(context.Background(), req)
							if err != nil {
								break
							}
						}
					}
				}
				tp := timestamppb.New(*snapshot.Properties.TimeCreated)
				ready := true
				expectedresponse := &csi.CreateSnapshotResponse{
					Snapshot: &csi.Snapshot{
						SizeBytes:      volumehelper.GiBToBytes(int64(*snapshot.Properties.DiskSizeGB)),
						SnapshotId:     fmt.Sprintf("%s%s", snapshotURI, snapshotName),
						SourceVolumeId: req.SourceVolumeId,
						CreationTime:   tp,
						ReadyToUse:     ready,
					},
				}
				if !reflect.DeepEqual(expectedresponse, actualresponse) || err != nil {
					t.Errorf("actualresponse: (%+v), expectedresponse: (%+v)\n", actualresponse, expectedresponse)
					t.Errorf("err:%v", err)
				}
			},
		},
		{
			name: "valid request - cross region with delete error still success",
			testFunc: func(t *testing.T) {
				parameter := make(map[string]string)
				parameter["tags"] = "unit=test"
				parameter["location"] = "eastus"
				parameter["incremental"] = "true"
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: testVolumeID,
					Name:           "unit-test",
					Parameters:     parameter,
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := fakeDriverFn(cntl)
				d.setCloud(&azure.Cloud{})
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mock_snapshotclient.NewMockInterface(ctrl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetSnapshotClientForSub(gomock.Any()).Return(mockSnapshotClient, nil).AnyTimes()

				provisioningState := "succeeded"
				DiskSize := int32(10)
				snapshotID := "test"
				snapshot := &armcompute.Snapshot{
					Properties: &armcompute.SnapshotProperties{
						TimeCreated:       &time.Time{},
						ProvisioningState: &provisioningState,
						DiskSizeGB:        &DiskSize,
					},
					ID: &snapshotID,
				}
				mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
				mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshot, nil).AnyTimes()
				mockSnapshotClient.EXPECT().Delete(gomock.Any(), gomock.Any(), gomock.Any()).Return(fmt.Errorf("test")).AnyTimes()
				actualresponse, err := d.CreateSnapshot(context.Background(), req)
				tp := timestamppb.New(*snapshot.Properties.TimeCreated)
				ready := true
				expectedresponse := &csi.CreateSnapshotResponse{
					Snapshot: &csi.Snapshot{
						SizeBytes:      volumehelper.GiBToBytes(int64(*snapshot.Properties.DiskSizeGB)),
						SnapshotId:     *snapshot.ID,
						SourceVolumeId: req.SourceVolumeId,
						CreationTime:   tp,
						ReadyToUse:     ready,
					},
				}
				if !reflect.DeepEqual(expectedresponse, actualresponse) || err != nil {
					t.Errorf("actualresponse: (%+v), expectedresponse: (%+v)\n", actualresponse, expectedresponse)
					t.Errorf("err:%v", err)
				}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func TestDeleteSnapshot(t *testing.T) {
	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "Snapshot ID missing",
			testFunc: func(t *testing.T) {
				req := &csi.DeleteSnapshotRequest{}
				expectedErr := status.Error(codes.InvalidArgument, "Snapshot ID must be provided")
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				_, err := d.DeleteSnapshot(context.Background(), req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "Snapshot ID invalid",
			testFunc: func(t *testing.T) {
				req := &csi.DeleteSnapshotRequest{
					SnapshotId: "/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name",
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				expectedErr := status.Errorf(codes.InvalidArgument, "invalid URI: /subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name")
				_, err := d.DeleteSnapshot(context.Background(), req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "delete Snapshot error",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				d.setCloud(&azure.Cloud{})
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mock_snapshotclient.NewMockInterface(ctrl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetSnapshotClientForSub(gomock.Any()).Return(mockSnapshotClient, nil).AnyTimes()
				req := &csi.DeleteSnapshotRequest{
					SnapshotId: "testurl/subscriptions/12/resourceGroups/23/providers/Microsoft.Compute/snapshots/snapshot-name",
				}
				mockSnapshotClient.EXPECT().Delete(gomock.Any(), gomock.Any(), gomock.Any()).Return(fmt.Errorf("get snapshot error")).AnyTimes()
				expectedErr := status.Errorf(codes.Internal, "delete snapshot error: get snapshot error")
				_, err := d.DeleteSnapshot(context.Background(), req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "Valid delete Snapshot ",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				d.setCloud(&azure.Cloud{})
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mock_snapshotclient.NewMockInterface(ctrl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetSnapshotClientForSub(gomock.Any()).Return(mockSnapshotClient, nil).AnyTimes()
				req := &csi.DeleteSnapshotRequest{
					SnapshotId: "testurl/subscriptions/12/resourceGroups/23/providers/Microsoft.Compute/snapshots/snapshot-name",
				}
				mockSnapshotClient.EXPECT().Delete(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				_, err := d.DeleteSnapshot(context.Background(), req)
				if !reflect.DeepEqual(err, nil) {
					t.Errorf("actualErr: (%v), expectedErr: nil)", err)
				}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func TestGetSnapshotByID(t *testing.T) {
	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "snapshotID not valid",
			testFunc: func(t *testing.T) {
				sourceVolumeID := "unit-test"
				ctx := context.Background()
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				d.setCloud(&azure.Cloud{})
				snapshotID := "testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name"
				expectedErr := status.Errorf(codes.InvalidArgument, "invalid URI: testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name")
				_, err := d.getSnapshotByID(ctx, d.getCloud().SubscriptionID, d.getCloud().ResourceGroup, snapshotID, sourceVolumeID)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "snapshot get error",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				d.setCloud(&azure.Cloud{})
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mock_snapshotclient.NewMockInterface(ctrl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetSnapshotClientForSub(gomock.Any()).Return(mockSnapshotClient, nil).AnyTimes()
				snapshotID := "testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name"
				snapshot := &armcompute.Snapshot{
					Properties: &armcompute.SnapshotProperties{},
					ID:         &snapshotID,
				}
				snapshotVolumeID := "unit-test"
				mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshot, fmt.Errorf("test")).AnyTimes()
				expectedErr := status.Errorf(codes.InvalidArgument, "invalid URI: testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name")
				_, err := d.getSnapshotByID(context.Background(), d.getCloud().SubscriptionID, d.getCloud().ResourceGroup, snapshotID, snapshotVolumeID)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func TestListSnapshots(t *testing.T) {
	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "snapshotID not valid",
			testFunc: func(t *testing.T) {
				req := csi.ListSnapshotsRequest{
					SnapshotId: "testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-nametestVolumeName",
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				expectedErr := status.Errorf(codes.InvalidArgument, "invalid URI: testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-nametestVolumeName")
				_, err := d.ListSnapshots(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "valid List",
			testFunc: func(t *testing.T) {
				req := csi.ListSnapshotsRequest{
					SnapshotId: "testurl/subscriptions/12/resourceGroups/23/providers/Microsoft.Compute/snapshots/snapshot-name",
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				provisioningState := "succeeded"
				DiskSize := int32(10)
				snapshotID := "test"
				snapshot := &armcompute.Snapshot{
					Properties: &armcompute.SnapshotProperties{
						TimeCreated:       &time.Time{},
						ProvisioningState: &provisioningState,
						DiskSizeGB:        &DiskSize,
					},
					ID: &snapshotID,
				}
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mock_snapshotclient.NewMockInterface(ctrl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetSnapshotClientForSub(gomock.Any()).Return(mockSnapshotClient, nil).AnyTimes()
				mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshot, nil).AnyTimes()
				expectedErr := error(nil)
				_, err := d.ListSnapshots(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "List resource error",
			testFunc: func(t *testing.T) {
				req := csi.ListSnapshotsRequest{}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				snapshot := &armcompute.Snapshot{}
				snapshots := []*armcompute.Snapshot{}
				snapshots = append(snapshots, snapshot)
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mock_snapshotclient.NewMockInterface(ctrl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetSnapshotClient().Return(mockSnapshotClient).AnyTimes()
				mockSnapshotClient.EXPECT().List(gomock.Any(), gomock.Any()).Return(snapshots, fmt.Errorf("test")).AnyTimes()
				expectedErr := status.Error(codes.Internal, "Unknown list snapshot error: test")
				_, err := d.ListSnapshots(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "snapshot property nil",
			testFunc: func(t *testing.T) {
				req := csi.ListSnapshotsRequest{}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				snapshot := &armcompute.Snapshot{}
				snapshots := []*armcompute.Snapshot{}
				snapshots = append(snapshots, snapshot)
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mock_snapshotclient.NewMockInterface(ctrl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetSnapshotClient().Return(mockSnapshotClient).AnyTimes()
				mockSnapshotClient.EXPECT().List(gomock.Any(), gomock.Any()).Return(snapshots, nil).AnyTimes()
				expectedErr := fmt.Errorf("failed to generate snapshot entry: snapshot property is nil")
				_, err := d.ListSnapshots(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "List snapshots when source volumeId is given",
			testFunc: func(t *testing.T) {
				req := csi.ListSnapshotsRequest{SourceVolumeId: "test"}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				volumeID := "test"
				DiskSize := int32(10)
				snapshotID := "test"
				provisioningState := "succeeded"
				snapshot1 := &armcompute.Snapshot{
					Properties: &armcompute.SnapshotProperties{
						TimeCreated:       &time.Time{},
						ProvisioningState: &provisioningState,
						DiskSizeGB:        &DiskSize,
						CreationData:      &armcompute.CreationData{SourceResourceID: &volumeID},
					},
					ID: &snapshotID}
				snapshot2 := &armcompute.Snapshot{}
				snapshots := []*armcompute.Snapshot{}
				snapshots = append(snapshots, snapshot1, snapshot2)
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mock_snapshotclient.NewMockInterface(ctrl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetSnapshotClient().Return(mockSnapshotClient).AnyTimes()
				mockSnapshotClient.EXPECT().List(gomock.Any(), gomock.Any()).Return(snapshots, nil).AnyTimes()
				snapshotsResponse, _ := d.ListSnapshots(context.TODO(), &req)
				if len(snapshotsResponse.Entries) != 1 {
					t.Errorf("actualNumberOfEntries: (%v), expectedNumberOfEntries: (%v)", len(snapshotsResponse.Entries), 1)
				}
				if snapshotsResponse.Entries[0].Snapshot.SourceVolumeId != volumeID {
					t.Errorf("actualVolumeId: (%v), expectedVolumeId: (%v)", snapshotsResponse.Entries[0].Snapshot.SourceVolumeId, volumeID)
				}
				if snapshotsResponse.NextToken != "2" {
					t.Errorf("actualNextToken: (%v), expectedNextToken: (%v)", snapshotsResponse.NextToken, "2")
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}

}
func TestGetCapacity(t *testing.T) {
	cntl := gomock.NewController(t)
	defer cntl.Finish()
	d, _ := NewFakeDriver(cntl)
	req := csi.GetCapacityRequest{}
	resp, err := d.GetCapacity(context.Background(), &req)
	assert.Nil(t, resp)
	if !reflect.DeepEqual(err, status.Error(codes.Unimplemented, "")) {
		t.Errorf("Unexpected error: %v", err)
	}
}

func TestListVolumes(t *testing.T) {
	volume1 := v1.PersistentVolume{
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeSource: v1.PersistentVolumeSource{
				CSI: &v1.CSIPersistentVolumeSource{
					Driver:       "disk.csi.azure.com",
					VolumeHandle: "/subscriptions/test-subscription/resourceGroups/test_resourcegroup-1/providers/Microsoft.Compute/disks/test-pv-1",
				},
			},
		},
	}
	volume2 := v1.PersistentVolume{
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeSource: v1.PersistentVolumeSource{
				CSI: &v1.CSIPersistentVolumeSource{
					Driver:       "disk.csi.azure.com",
					VolumeHandle: "/subscriptions/test-subscription/resourceGroups/test_resourcegroup-2/providers/Microsoft.Compute/disks/test-pv-2",
				},
			},
		},
	}

	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "When no KubeClient exists, Valid list without max_entries or starting_token",
			testFunc: func(t *testing.T) {
				t.SkipNow() //todo: fix this test
				req := csi.ListVolumesRequest{}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				fakeVolumeID := "test"
				disk := &armcompute.Disk{ID: &fakeVolumeID}
				disks := []*armcompute.Disk{}
				disks = append(disks, disk)
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClient().Return(diskClient).AnyTimes()
				diskClient.EXPECT().List(gomock.Any(), gomock.Any()).Return(disks, nil).AnyTimes()
				expectedErr := error(nil)
				listVolumesResponse, err := d.ListVolumes(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
				if listVolumesResponse.NextToken != "" {
					t.Errorf("actualNextToken: (%v), expectedNextToken: (%v)", listVolumesResponse.NextToken, "")
				}
			},
		},
		{
			name: "When no KubeClient exists, Valid list with max_entries",
			testFunc: func(t *testing.T) {
				t.SkipNow() //todo: fix this test
				req := csi.ListVolumesRequest{
					MaxEntries: 1,
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				fakeVolumeID := "test"
				disk1, disk2 := &armcompute.Disk{ID: &fakeVolumeID}, &armcompute.Disk{ID: &fakeVolumeID}
				disks := []*armcompute.Disk{}
				disks = append(disks, disk1, disk2)
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClient().Return(diskClient).AnyTimes()
				diskClient.EXPECT().List(gomock.Any(), gomock.Any()).Return(disks, nil).AnyTimes()
				expectedErr := error(nil)
				listVolumesResponse, err := d.ListVolumes(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
				if len(listVolumesResponse.Entries) != int(req.MaxEntries) {
					t.Errorf("Actual number of entries: (%v), Expected number of entries: (%v)", len(listVolumesResponse.Entries), req.MaxEntries)
				}
				if listVolumesResponse.NextToken != "1" {
					t.Errorf("actualNextToken: (%v), expectedNextToken: (%v)", listVolumesResponse.NextToken, "1")
				}
			},
		},
		{
			name: "When no KubeClient exists, Valid list with max_entries and starting_token",
			testFunc: func(t *testing.T) {
				t.SkipNow() //todo: fix this test
				req := csi.ListVolumesRequest{
					StartingToken: "1",
					MaxEntries:    1,
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				fakeVolumeID1, fakeVolumeID12 := "test1", "test2"
				disk1, disk2 := &armcompute.Disk{ID: &fakeVolumeID1}, &armcompute.Disk{ID: &fakeVolumeID12}
				disks := []*armcompute.Disk{}
				disks = append(disks, disk1, disk2)
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClient().Return(diskClient).AnyTimes()
				diskClient.EXPECT().List(gomock.Any(), gomock.Any()).Return(disks, nil).AnyTimes()
				expectedErr := error(nil)
				listVolumesResponse, err := d.ListVolumes(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
				if len(listVolumesResponse.Entries) != int(req.MaxEntries) {
					t.Errorf("Actual number of entries: (%v), Expected number of entries: (%v)", len(listVolumesResponse.Entries), req.MaxEntries)
				}
				if listVolumesResponse.NextToken != "" {
					t.Errorf("actualNextToken: (%v), expectedNextToken: (%v)", listVolumesResponse.NextToken, "")
				}
				if listVolumesResponse.Entries[0].Volume.VolumeId != fakeVolumeID12 {
					t.Errorf("actualVolumeId: (%v), expectedVolumeId: (%v)", listVolumesResponse.Entries[0].Volume.VolumeId, fakeVolumeID12)
				}
			},
		},
		{
			name: "When no KubeClient exists, ListVolumes request with starting token but no entries in response",
			testFunc: func(t *testing.T) {
				t.SkipNow() //todo: fix this test
				req := csi.ListVolumesRequest{
					StartingToken: "1",
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				disks := []*armcompute.Disk{}
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClient().Return(diskClient).AnyTimes()
				diskClient.EXPECT().List(gomock.Any(), gomock.Any()).Return(disks, nil).AnyTimes()
				expectedErr := status.Error(codes.FailedPrecondition, "ListVolumes starting token(1) on rg(rg) is greater than total number of volumes")
				_, err := d.ListVolumes(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "When no KubeClient exists, ListVolumes list resource error",
			testFunc: func(t *testing.T) {
				t.SkipNow() //todo: fix this test
				req := csi.ListVolumesRequest{
					StartingToken: "1",
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				disks := []*armcompute.Disk{}
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClient().Return(diskClient).AnyTimes()
				diskClient.EXPECT().List(gomock.Any(), gomock.Any()).Return(disks, fmt.Errorf("test")).AnyTimes()
				expectedErr := status.Error(codes.Internal, "ListVolumes on rg(rg) failed with error: test")
				_, err := d.ListVolumes(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "When KubeClient exists, Empty list without start token should not return error",
			testFunc: func(t *testing.T) {
				req := csi.ListVolumesRequest{}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d := getFakeDriverWithKubeClient(cntl)
				pvList := v1.PersistentVolumeList{
					Items: []v1.PersistentVolume{},
				}
				d.getCloud().KubeClient.CoreV1().PersistentVolumes().(*mockpersistentvolume.MockInterface).EXPECT().List(gomock.Any(), gomock.Any()).Return(&pvList, nil)
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClient().Return(diskClient).AnyTimes()
				diskClient.EXPECT().List(gomock.Any(), gomock.Any()).Return([]*armcompute.Disk{}, nil).AnyTimes()
				expectedErr := error(nil)
				_, err := d.ListVolumes(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "When KubeClient exists, Valid list without max_entries or starting_token",
			testFunc: func(t *testing.T) {
				t.SkipNow() //todo: fix this test
				req := csi.ListVolumesRequest{}
				fakeVolumeID := "/subscriptions/test-subscription/resourceGroups/test_resourcegroup-1/providers/Microsoft.Compute/disks/test-pv-1"
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d := getFakeDriverWithKubeClient(cntl)
				pvList := v1.PersistentVolumeList{
					Items: []v1.PersistentVolume{volume1},
				}
				d.getCloud().KubeClient.CoreV1().PersistentVolumes().(*mockpersistentvolume.MockInterface).EXPECT().List(gomock.Any(), gomock.Any()).Return(&pvList, nil)
				disk1 := &armcompute.Disk{ID: &fakeVolumeID}
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClient().Return(diskClient).AnyTimes()
				diskClient.EXPECT().List(gomock.Any(), gomock.Any()).Return([]*armcompute.Disk{disk1}, nil).AnyTimes()
				expectedErr := error(nil)
				listVolumesResponse, err := d.ListVolumes(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
				if listVolumesResponse.NextToken != "" {
					t.Errorf("actualNextToken: (%v), expectedNextToken: (%v)", listVolumesResponse.NextToken, "")
				}
			},
		},
		{
			name: "When KubeClient exists, Valid list with max_entries",
			testFunc: func(t *testing.T) {
				t.SkipNow() //todo: fix this test
				req := csi.ListVolumesRequest{
					MaxEntries: 1,
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d := getFakeDriverWithKubeClient(cntl)
				d.getCloud().SubscriptionID = "test-subscription"
				fakeVolumeID := "/subscriptions/test-subscription/resourceGroups/test_resourcegroup-1/providers/Microsoft.Compute/disks/test-pv-1"
				disk1, disk2 := &armcompute.Disk{ID: &fakeVolumeID}, &armcompute.Disk{ID: &fakeVolumeID}
				pvList := v1.PersistentVolumeList{
					Items: []v1.PersistentVolume{volume1, volume2},
				}
				d.getCloud().KubeClient.CoreV1().PersistentVolumes().(*mockpersistentvolume.MockInterface).EXPECT().List(gomock.Any(), gomock.Any()).Return(&pvList, nil)
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClient().Return(diskClient).AnyTimes()
				diskClient.EXPECT().List(gomock.Any(), gomock.Any()).Return([]*armcompute.Disk{disk1}, nil).AnyTimes()
				diskClient.EXPECT().List(gomock.Any(), gomock.Any()).Return([]*armcompute.Disk{disk2}, nil).AnyTimes()
				expectedErr := error(nil)
				listVolumesResponse, err := d.ListVolumes(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
				if len(listVolumesResponse.Entries) != int(req.MaxEntries) {
					t.Errorf("Actual number of entries: (%v), Expected number of entries: (%v)", len(listVolumesResponse.Entries), req.MaxEntries)
				}
				if listVolumesResponse.NextToken != "1" {
					t.Errorf("actualNextToken: (%v), expectedNextToken: (%v)", listVolumesResponse.NextToken, "1")
				}
			},
		},
		{
			name: "When KubeClient exists, Valid list with max_entries and starting_token",
			testFunc: func(t *testing.T) {
				t.SkipNow() //todo: fix this test
				req := csi.ListVolumesRequest{
					StartingToken: "1",
					MaxEntries:    1,
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d := getFakeDriverWithKubeClient(cntl)
				d.getCloud().SubscriptionID = "test-subscription"
				pvList := v1.PersistentVolumeList{
					Items: []v1.PersistentVolume{volume1, volume2},
				}
				fakeVolumeID11, fakeVolumeID12 := "/subscriptions/test-subscription/resourceGroups/test_resourcegroup-1/providers/Microsoft.Compute/disks/test-pv-1", "/subscriptions/test-subscription/resourceGroups/test_resourcegroup-2/providers/Microsoft.Compute/disks/test-pv-2"
				disk1, disk2 := &armcompute.Disk{ID: &fakeVolumeID11}, &armcompute.Disk{ID: &fakeVolumeID12}
				d.getCloud().KubeClient.CoreV1().PersistentVolumes().(*mockpersistentvolume.MockInterface).EXPECT().List(gomock.Any(), gomock.Any()).Return(&pvList, nil)
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClient().Return(diskClient).AnyTimes()
				diskClient.EXPECT().List(gomock.Any(), gomock.Any()).Return([]*armcompute.Disk{disk1}, nil).AnyTimes()
				diskClient.EXPECT().List(gomock.Any(), gomock.Any()).Return([]*armcompute.Disk{disk2}, nil).AnyTimes()
				expectedErr := error(nil)
				listVolumesResponse, err := d.ListVolumes(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
				if len(listVolumesResponse.Entries) != int(req.MaxEntries) {
					t.Errorf("Actual number of entries: (%v), Expected number of entries: (%v)", len(listVolumesResponse.Entries), req.MaxEntries)
				}
				if listVolumesResponse.NextToken != "" {
					t.Errorf("actualNextToken: (%v), expectedNextToken: (%v)", listVolumesResponse.NextToken, "")
				}
				if listVolumesResponse.Entries[0].Volume.VolumeId != fakeVolumeID11 {
					t.Errorf("actualVolumeId: (%v), expectedVolumeId: (%v)", listVolumesResponse.Entries[0].Volume.VolumeId, fakeVolumeID11)
				}
			},
		},
		{
			name: "When KubeClient exists, ListVolumes request with starting token but no entries in response",
			testFunc: func(t *testing.T) {
				req := csi.ListVolumesRequest{
					StartingToken: "1",
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d := getFakeDriverWithKubeClient(cntl)
				pvList := v1.PersistentVolumeList{
					Items: []v1.PersistentVolume{},
				}
				d.getCloud().KubeClient.CoreV1().PersistentVolumes().(*mockpersistentvolume.MockInterface).EXPECT().List(gomock.Any(), gomock.Any()).Return(&pvList, nil)
				expectedErr := status.Error(codes.FailedPrecondition, "ListVolumes starting token(1) is greater than total number of disks")
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClient().Return(diskClient).AnyTimes()
				_, err := d.ListVolumes(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "When KubeClient exists, ListVolumes list pv error",
			testFunc: func(t *testing.T) {
				req := csi.ListVolumesRequest{
					StartingToken: "1",
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d := getFakeDriverWithKubeClient(cntl)
				rerr := fmt.Errorf("test")
				d.getCloud().KubeClient.CoreV1().PersistentVolumes().(*mockpersistentvolume.MockInterface).EXPECT().List(gomock.Any(), gomock.Any()).Return(nil, rerr)
				expectedErr := status.Error(codes.Internal, "ListVolumes failed while fetching PersistentVolumes List with error: test")
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClient().Return(diskClient).AnyTimes()
				_, err := d.ListVolumes(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func TestValidateVolumeCapabilities(t *testing.T) {
	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "Volume ID missing ",
			testFunc: func(t *testing.T) {
				req := csi.ValidateVolumeCapabilitiesRequest{}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				expectedErr := status.Errorf(codes.InvalidArgument, "Volume ID missing in the request")
				_, err := d.ValidateVolumeCapabilities(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "Volume capabilities missing ",
			testFunc: func(t *testing.T) {
				req := csi.ValidateVolumeCapabilitiesRequest{
					VolumeId: "unit-test",
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				expectedErr := status.Errorf(codes.InvalidArgument, "VolumeCapabilities missing in the request")
				_, err := d.ValidateVolumeCapabilities(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "check disk err ",
			testFunc: func(t *testing.T) {
				req := csi.ValidateVolumeCapabilitiesRequest{
					VolumeId:           "-",
					VolumeCapabilities: stdVolumeCapabilities,
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				expectedErr := status.Errorf(codes.NotFound, "Volume not found, failed with error: invalid URI: -")
				_, err := d.ValidateVolumeCapabilities(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "invalid req ",
			testFunc: func(t *testing.T) {
				req := csi.ValidateVolumeCapabilitiesRequest{
					VolumeId:           testVolumeID,
					VolumeCapabilities: stdVolumeCapabilities,
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				disk := &armcompute.Disk{
					Properties: &armcompute.DiskProperties{},
				}
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()
				diskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				expectedErr := error(nil)
				_, err := d.ValidateVolumeCapabilities(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "valid req ",
			testFunc: func(t *testing.T) {
				stdVolumeCapabilitytest := &csi.VolumeCapability{
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY,
					},
				}
				stdVolumeCapabilitiestest := []*csi.VolumeCapability{
					stdVolumeCapabilitytest,
				}
				req := csi.ValidateVolumeCapabilitiesRequest{
					VolumeId:           testVolumeID,
					VolumeCapabilities: stdVolumeCapabilitiestest,
				}
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				disk := &armcompute.Disk{
					Properties: &armcompute.DiskProperties{},
				}
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()
				diskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				expectedErr := error(nil)
				_, err := d.ValidateVolumeCapabilities(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func TestGetSourceDiskSize(t *testing.T) {
	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "max depth reached",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				_, _, err := d.GetSourceDiskSize(context.Background(), "", "test-rg", "test-disk", 2, 1)
				expectedErr := status.Errorf(codes.Internal, "current depth (2) surpassed the max depth (1) while searching for the source disk size")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "diskproperty not found",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				disk := &armcompute.Disk{}
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()
				diskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()

				_, _, err := d.GetSourceDiskSize(context.Background(), "", "test-rg", "test-disk", 0, 1)
				expectedErr := status.Error(codes.Internal, "DiskProperty not found for disk (test-disk) in resource group (test-rg)")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "nil DiskSizeGB",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				diskProperties := armcompute.DiskProperties{}
				disk := &armcompute.Disk{
					Properties: &diskProperties,
				}
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()
				diskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				_, _, err := d.GetSourceDiskSize(context.Background(), "", "test-rg", "test-disk", 0, 1)
				expectedErr := status.Error(codes.Internal, "DiskSizeGB for disk (test-disk) in resourcegroup (test-rg) is nil")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "successful search: depth 1",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				diskSizeGB := int32(8)
				diskProperties := armcompute.DiskProperties{
					DiskSizeGB: &diskSizeGB,
				}
				disk := &armcompute.Disk{
					Properties: &diskProperties,
				}
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()
				diskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				size, _, _ := d.GetSourceDiskSize(context.Background(), "", "test-rg", "test-disk", 0, 1)
				expectedOutput := diskSizeGB
				if *size != expectedOutput {
					t.Errorf("actualOutput: (%v), expectedOutput: (%v)", *size, expectedOutput)
				}
			},
		},
		{
			name: "successful search: depth 2",
			testFunc: func(t *testing.T) {
				cntl := gomock.NewController(t)
				defer cntl.Finish()
				d, _ := NewFakeDriver(cntl)
				diskSizeGB1 := int32(16)
				diskSizeGB2 := int32(8)
				sourceURI := "/subscriptions/xxxxxxxx/resourcegroups/test-rg/providers/microsoft.compute/disks/test-disk-1"
				creationData := armcompute.CreationData{
					CreateOption: to.Ptr(armcompute.DiskCreateOptionCopy),
					SourceURI:    &sourceURI,
				}
				diskProperties1 := armcompute.DiskProperties{
					CreationData: &creationData,
					DiskSizeGB:   &diskSizeGB1,
				}
				diskProperties2 := armcompute.DiskProperties{
					DiskSizeGB: &diskSizeGB2,
				}
				disk1 := &armcompute.Disk{
					Properties: &diskProperties1,
				}
				disk2 := &armcompute.Disk{
					Properties: &diskProperties2,
				}
				diskClient := mock_diskclient.NewMockInterface(cntl)
				d.getClientFactory().(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()
				diskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk1, nil).Return(disk2, nil).AnyTimes()
				size, _, _ := d.GetSourceDiskSize(context.Background(), "", "test-rg", "test-disk-1", 0, 2)
				expectedOutput := diskSizeGB2
				if *size != expectedOutput {
					t.Errorf("actualOutput: (%v), expectedOutput: (%v)", *size, expectedOutput)
				}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func getFakeDriverWithKubeClient(ctrl *gomock.Controller) FakeDriver {
	d, _ := NewFakeDriver(ctrl)

	corev1 := mockcorev1.NewMockInterface(ctrl)
	persistentvolume := mockpersistentvolume.NewMockInterface(ctrl)
	d.getCloud().KubeClient = mockkubeclient.NewMockInterface(ctrl)
	d.getCloud().KubeClient.(*mockkubeclient.MockInterface).EXPECT().CoreV1().Return(corev1).AnyTimes()
	d.getCloud().KubeClient.CoreV1().(*mockcorev1.MockInterface).EXPECT().PersistentVolumes().Return(persistentvolume).AnyTimes()
	return d
}
