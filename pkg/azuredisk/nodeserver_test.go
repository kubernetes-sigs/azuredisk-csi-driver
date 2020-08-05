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
	"os"
	"reflect"
	"syscall"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"k8s.io/utils/exec/testing"
	"k8s.io/utils/mount"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/mounter"
)

const (
	sourceTest = "./source_test"
	targetTest = "./target_test"
)

func TestNodeGetCapabilities(t *testing.T) {
	d, _ := NewFakeDriver(t)
	capType := &csi.NodeServiceCapability_Rpc{
		Rpc: &csi.NodeServiceCapability_RPC{
			Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
		},
	}
	capList := []*csi.NodeServiceCapability{{
		Type: capType,
	}}
	d.NSCap = capList
	// Test valid request
	req := csi.NodeGetCapabilitiesRequest{}
	resp, err := d.NodeGetCapabilities(context.Background(), &req)
	assert.NotNil(t, resp)
	assert.Equal(t, resp.Capabilities[0].GetType(), capType)
	assert.NoError(t, err)
}

func TestGetFStype(t *testing.T) {
	tests := []struct {
		options  map[string]string
		expected string
	}{
		{
			nil,
			"",
		},
		{
			map[string]string{},
			"",
		},
		{
			map[string]string{"fstype": ""},
			"",
		},
		{
			map[string]string{"fstype": "xfs"},
			"xfs",
		},
		{
			map[string]string{"FSType": "xfs"},
			"xfs",
		},
		{
			map[string]string{"fstype": "EXT4"},
			"ext4",
		},
	}

	for _, test := range tests {
		result := getFStype(test.options)
		if result != test.expected {
			t.Errorf("input: %q, getFStype result: %s, expected: %s", test.options, result, test.expected)
		}
	}
}

func TestGetMaxDataDiskCount(t *testing.T) {
	tests := []struct {
		instanceType string
		expectResult int64
	}{
		{
			instanceType: "standard_d2_v2",
			expectResult: 8,
		},
		{
			instanceType: "Standard_DS14_V2",
			expectResult: 64,
		},
		{
			instanceType: "NOT_EXISTING",
			expectResult: defaultAzureVolumeLimit,
		},
		{
			instanceType: "",
			expectResult: defaultAzureVolumeLimit,
		},
	}

	for _, test := range tests {
		result := getMaxDataDiskCount(test.instanceType)
		assert.Equal(t, test.expectResult, result)
	}
}

func TestEnsureMountPoint(t *testing.T) {
	errorTarget := "./error_is_likely_target"
	alreadyExistTarget := "./false_is_likely_exist_target"
	azuredisk := "./azure.go"

	tests := []struct {
		desc        string
		target      string
		expectedErr error
	}{
		{
			desc:        "[Error] Mocked by IsLikelyNotMountPoint",
			target:      errorTarget,
			expectedErr: fmt.Errorf("fake IsLikelyNotMountPoint: fake error"),
		},
		{
			desc:        "[Error] Not a directory",
			target:      azuredisk,
			expectedErr: &os.PathError{Op: "mkdir", Path: "./azure.go", Err: syscall.ENOTDIR},
		},
		{
			desc:        "[Success] Successful run",
			target:      targetTest,
			expectedErr: nil,
		},
		{
			desc:        "[Success] Already existing mount",
			target:      alreadyExistTarget,
			expectedErr: nil,
		},
	}

	// Setup
	_ = makeDir(alreadyExistTarget)
	d, _ := NewFakeDriver(t)
	fakeMounter := &fakeMounter{}
	fakeExec := &testingexec.FakeExec{ExactOrder: true}
	d.mounter = &mount.SafeFormatAndMount{
		Interface: fakeMounter,
		Exec:      fakeExec,
	}

	for _, test := range tests {
		err := d.ensureMountPoint(test.target)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
		}
	}

	// Clean up
	err := os.RemoveAll(alreadyExistTarget)
	assert.NoError(t, err)
	err = os.RemoveAll(targetTest)
	assert.NoError(t, err)
}

func TestNodeGetVolumeStats(t *testing.T) {
	nonexistedPath := "/not/a/real/directory"
	fakePath := "/tmp/fake-volume-path"
	tests := []struct {
		desc        string
		req         csi.NodeGetVolumeStatsRequest
		expectedErr error
	}{
		{
			desc:        "Volume ID missing",
			req:         csi.NodeGetVolumeStatsRequest{VolumePath: targetTest},
			expectedErr: status.Error(codes.InvalidArgument, "NodeGetVolumeStats volume ID was empty"),
		},
		{
			desc:        "VolumePath missing",
			req:         csi.NodeGetVolumeStatsRequest{VolumeId: "vol_1"},
			expectedErr: status.Error(codes.InvalidArgument, "NodeGetVolumeStats volume path was empty"),
		},
		{
			desc:        "Not existed volume path",
			req:         csi.NodeGetVolumeStatsRequest{VolumePath: nonexistedPath, VolumeId: "vol_1"},
			expectedErr: status.Errorf(codes.NotFound, "path /not/a/real/directory does not exist"),
		},
		{
			desc:        "standard success",
			req:         csi.NodeGetVolumeStatsRequest{VolumePath: fakePath, VolumeId: "vol_1"},
			expectedErr: nil,
		},
	}

	// Setup
	_ = makeDir(fakePath)
	d, _ := NewFakeDriver(t)

	for _, test := range tests {
		_, err := d.NodeGetVolumeStats(context.Background(), &test.req)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
		}
	}

	// Clean up
	err := os.RemoveAll(fakePath)
	assert.NoError(t, err)
}

func TestNodeStageVolume(t *testing.T) {
	stdVolCap := &csi.VolumeCapability_Mount{
		Mount: &csi.VolumeCapability_MountVolume{},
	}

	stdVolCapBlock := &csi.VolumeCapability_Block{
		Block: &csi.VolumeCapability_BlockVolume{},
	}

	volumeCap := csi.VolumeCapability_AccessMode{Mode: 2}
	volumeCapWrong := csi.VolumeCapability_AccessMode{Mode: 10}
	publishContext := map[string]string{
		LUN: "/dev/01",
	}

	tests := []struct {
		desc        string
		req         csi.NodeStageVolumeRequest
		expectedErr error
	}{
		{
			desc:        "Volume ID missing",
			req:         csi.NodeStageVolumeRequest{},
			expectedErr: status.Error(codes.InvalidArgument, "Volume ID not provided"),
		},
		{
			desc:        "Stage target path missing",
			req:         csi.NodeStageVolumeRequest{VolumeId: "vol_1"},
			expectedErr: status.Error(codes.InvalidArgument, "Staging target not provided"),
		},
		{
			desc:        "Volume capabilities missing",
			req:         csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest},
			expectedErr: status.Error(codes.InvalidArgument, "Volume capability not provided"),
		},
		{
			desc:        "Volume capabilities not supported",
			req:         csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest, VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCapWrong}},
			expectedErr: status.Error(codes.InvalidArgument, "Volume capability not supported"),
		},
		{
			desc: "Access type is block",
			req: csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest, VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap,
				AccessType: stdVolCapBlock}},
			expectedErr: nil,
		},
		{
			desc: "Lun not provided",
			req: csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest, VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap,
				AccessType: stdVolCap}},
			expectedErr: status.Error(codes.InvalidArgument, "lun not provided"),
		},
		{
			desc: "Invalid Lun",
			req: csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest,
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap,
					AccessType: stdVolCap},
				PublishContext: publishContext},
			expectedErr: status.Error(codes.Internal, "Failed to find disk on lun /dev/01. cannot parse deviceInfo: /dev/01"),
		},
	}

	// Setup
	_ = makeDir(sourceTest)
	_ = makeDir(targetTest)
	d, _ := NewFakeDriver(t)
	d.mounter, _ = mounter.NewSafeMounter()
	for _, test := range tests {
		_, err := d.NodeStageVolume(context.Background(), &test.req)
		if test.desc == "Failed volume mount" {
			assert.Error(t, err)
		} else if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
		}
	}

	// Clean up
	err := os.RemoveAll(sourceTest)
	assert.NoError(t, err)
	err = os.RemoveAll(targetTest)
	assert.NoError(t, err)
}

func TestNodeUnstageVolume(t *testing.T) {
	errorTarget := "./error_is_likely_target"
	targetFile := "./abc.go"
	tests := []struct {
		desc        string
		req         csi.NodeUnstageVolumeRequest
		expectedErr error
	}{
		{
			desc:        "Volume ID missing",
			req:         csi.NodeUnstageVolumeRequest{StagingTargetPath: targetTest},
			expectedErr: status.Error(codes.InvalidArgument, "Volume ID not provided"),
		},
		{
			desc:        "Staging target missing ",
			req:         csi.NodeUnstageVolumeRequest{VolumeId: "vol_1"},
			expectedErr: status.Error(codes.InvalidArgument, "Staging target not provided"),
		},
		{
			desc:        "[Error] CleanupMountPoint error mocked by IsLikelyNotMountPoint",
			req:         csi.NodeUnstageVolumeRequest{StagingTargetPath: errorTarget, VolumeId: "vol_1"},
			expectedErr: status.Error(codes.Internal, "failed to unmount staging target \"./error_is_likely_target\": fake IsLikelyNotMountPoint: fake error"),
		},
		{
			desc:        "[Success] Valid request",
			req:         csi.NodeUnstageVolumeRequest{StagingTargetPath: targetFile, VolumeId: "vol_1"},
			expectedErr: nil,
		},
	}

	//Setup
	_ = makeDir(errorTarget)
	d, _ := NewFakeDriver(t)
	fakeMounter := &fakeMounter{}
	fakeExec := &testingexec.FakeExec{ExactOrder: true}
	d.mounter = &mount.SafeFormatAndMount{
		Interface: fakeMounter,
		Exec:      fakeExec,
	}

	for _, test := range tests {
		_, err := d.NodeUnstageVolume(context.Background(), &test.req)

		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
		}
	}

	// Clean up
	err := os.RemoveAll(errorTarget)
	assert.NoError(t, err)
}

func TestNodePublishVolume(t *testing.T) {
	volumeCap := csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER}
	publishContext := map[string]string{
		LUN: "/dev/01",
	}
	errorMountSource := "./error_mount_source"
	alreadyMountedTarget := "./false_is_likely_exist_target"
	azuredisk := "./azure.go"
	stdVolCap := &csi.VolumeCapability_Mount{
		Mount: &csi.VolumeCapability_MountVolume{},
	}
	stdVolCapBlock := &csi.VolumeCapability_Block{
		Block: &csi.VolumeCapability_BlockVolume{},
	}

	tests := []struct {
		desc        string
		req         csi.NodePublishVolumeRequest
		expectedErr error
	}{
		{
			desc:        "Volume capabilities missing",
			req:         csi.NodePublishVolumeRequest{},
			expectedErr: status.Error(codes.InvalidArgument, "Volume capability missing in request"),
		},
		{
			desc:        "Volume ID missing",
			req:         csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap}},
			expectedErr: status.Error(codes.InvalidArgument, "Volume ID missing in request"),
		},
		{
			desc: "Staging target path missing",
			req: csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
				VolumeId: "vol_1"},
			expectedErr: status.Error(codes.InvalidArgument, "Staging target not provided"),
		},
		{
			desc: "Target path missing",
			req: csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
				VolumeId:          "vol_1",
				StagingTargetPath: sourceTest},
			expectedErr: status.Error(codes.InvalidArgument, "Target path not provided"),
		},
		{
			desc: "[Error] Not a directory",
			req: csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: stdVolCap},
				VolumeId:          "vol_1",
				TargetPath:        azuredisk,
				StagingTargetPath: sourceTest,
				Readonly:          true},
			expectedErr: status.Errorf(codes.Internal, "Could not mount target \"./azure.go\": mkdir ./azure.go: not a directory"),
		},
		{
			desc: "[Error] Lun not provided",
			req: csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: stdVolCapBlock},
				VolumeId:          "vol_1",
				TargetPath:        azuredisk,
				StagingTargetPath: sourceTest,
				Readonly:          true},
			expectedErr: status.Error(codes.InvalidArgument, "lun not provided"),
		},
		{
			desc: "[Error] Lun not valid",
			req: csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: stdVolCapBlock},
				VolumeId:          "vol_1",
				TargetPath:        azuredisk,
				StagingTargetPath: sourceTest,
				PublishContext:    publishContext,
				Readonly:          true},
			expectedErr: status.Error(codes.Internal, "Failed to find device path with lun /dev/01. cannot parse deviceInfo: /dev/01"),
		},
		{
			desc: "[Error] Mount error mocked by Mount",
			req: csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
				VolumeId:          "vol_1",
				TargetPath:        targetTest,
				StagingTargetPath: errorMountSource,
				Readonly:          true},
			expectedErr: status.Errorf(codes.Internal, "Could not mount \"./error_mount_source\" at \"./target_test\": fake Mount: source error"),
		},
		{
			desc: "[Success] Valid request already mounted",
			req: csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
				VolumeId:          "vol_1",
				TargetPath:        alreadyMountedTarget,
				StagingTargetPath: sourceTest,
				Readonly:          true},
			expectedErr: nil,
		},
		{
			desc: "[Success] Valid request",
			req: csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
				VolumeId:          "vol_1",
				TargetPath:        targetTest,
				StagingTargetPath: sourceTest,
				Readonly:          true},
			expectedErr: nil,
		},
	}

	// Setup
	_ = makeDir(alreadyMountedTarget)
	d, _ := NewFakeDriver(t)
	fakeMounter := &fakeMounter{}
	fakeExec := &testingexec.FakeExec{ExactOrder: true}
	d.mounter = &mount.SafeFormatAndMount{
		Interface: fakeMounter,
		Exec:      fakeExec,
	}

	for _, test := range tests {
		_, err := d.NodePublishVolume(context.Background(), &test.req)

		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
		}
	}

	// Clean up
	err := os.RemoveAll(targetTest)
	assert.NoError(t, err)
	err = os.RemoveAll(alreadyMountedTarget)
	assert.NoError(t, err)
}

func TestNodeUnpublishVolume(t *testing.T) {
	errorTarget := "./error_is_likely_target"
	targetFile := "./abc.go"
	tests := []struct {
		desc        string
		req         csi.NodeUnpublishVolumeRequest
		expectedErr error
	}{
		{
			desc:        "Volume ID missing",
			req:         csi.NodeUnpublishVolumeRequest{TargetPath: targetTest},
			expectedErr: status.Error(codes.InvalidArgument, "Volume ID missing in request"),
		},
		{
			desc:        "Target missing",
			req:         csi.NodeUnpublishVolumeRequest{VolumeId: "vol_1"},
			expectedErr: status.Error(codes.InvalidArgument, "Target path missing in request"),
		},
		{
			desc:        "[Error] Unmount error mocked by IsLikelyNotMountPoint",
			req:         csi.NodeUnpublishVolumeRequest{TargetPath: errorTarget, VolumeId: "vol_1"},
			expectedErr: status.Error(codes.Internal, "failed to unmount target \"./error_is_likely_target\": fake IsLikelyNotMountPoint: fake error"),
		},
		{
			desc:        "[Success] Valid request",
			req:         csi.NodeUnpublishVolumeRequest{TargetPath: targetFile, VolumeId: "vol_1"},
			expectedErr: nil,
		},
	}

	// Setup
	_ = makeDir(errorTarget)
	d, _ := NewFakeDriver(t)
	fakeMounter := &fakeMounter{}
	fakeExec := &testingexec.FakeExec{ExactOrder: true}
	d.mounter = &mount.SafeFormatAndMount{
		Interface: fakeMounter,
		Exec:      fakeExec,
	}

	for _, test := range tests {
		_, err := d.NodeUnpublishVolume(context.Background(), &test.req)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
		}
	}

	// Clean up
	err := os.RemoveAll(errorTarget)
	assert.NoError(t, err)
}

func TestNodeExpandVolume(t *testing.T) {
	d, _ := NewFakeDriver(t)
	tests := []struct {
		desc        string
		req         csi.NodeExpandVolumeRequest
		expectedErr error
	}{
		{
			desc:        "Volume ID missing",
			req:         csi.NodeExpandVolumeRequest{},
			expectedErr: status.Error(codes.InvalidArgument, "Volume ID not provided"),
		},
	}
	for _, test := range tests {
		_, err := d.NodeExpandVolume(context.Background(), &test.req)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
		}
	}
}

func TestGetBlockSizeBytes(t *testing.T) {
	d, _ := NewFakeDriver(t)
	testTarget := "./test"
	tests := []struct {
		desc        string
		req         string
		expectedErr error
	}{
		{
			desc:        "no exist path",
			req:         "testpath",
			expectedErr: fmt.Errorf("error when getting size of block volume at path testpath: output: , err: exit status 1"),
		},
		{
			desc:        "invalid path",
			req:         testTarget,
			expectedErr: fmt.Errorf("error when getting size of block volume at path ./test: output: , err: exit status 1"),
		},
	}
	for _, test := range tests {
		_, err := d.getBlockSizeBytes(test.req)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
		}
	}
	//Setup
	_ = makeDir(testTarget)

	err := os.RemoveAll(testTarget)
	assert.NoError(t, err)
}

func TestEnsureBlockTargetFile(t *testing.T) {
	testTarget := "./test"
	d, _ := NewFakeDriver(t)
	tests := []struct {
		desc        string
		req         string
		expectedErr error
	}{
		{
			desc:        "valid test",
			req:         testTarget,
			expectedErr: nil,
		},
		{
			desc:        "test if file exists",
			req:         "./test/test",
			expectedErr: status.Error(codes.Internal, "Could not mount target \"test\": mkdir test: not a directory"),
		},
	}
	for _, test := range tests {
		err := d.ensureBlockTargetFile(test.req)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
		}
	}
	err := os.RemoveAll(testTarget)
	assert.NoError(t, err)
}

func makeDir(pathname string) error {
	err := os.MkdirAll(pathname, os.FileMode(0755))
	if err != nil {
		if !os.IsExist(err) {
			return err
		}
	}
	return nil
}

func TestMakeDir(t *testing.T) {
	//Successfully create directory
	err := makeDir(targetTest)
	assert.NoError(t, err)

	//Failed case
	err = makeDir("./azure.go")
	var e *os.PathError
	if !errors.As(err, &e) {
		t.Errorf("Unexpected Error: %v", err)
	}

	// Remove the directory created
	err = os.RemoveAll(targetTest)
	assert.NoError(t, err)
}

func TestGetDevicePathWithLUN(t *testing.T) {
	d, _ := NewFakeDriver(t)
	tests := []struct {
		desc        string
		req         string
		expectedErr error
	}{
		{
			desc:        "valid test",
			req:         "unit-test",
			expectedErr: fmt.Errorf("cannot parse deviceInfo: unit-test"),
		},
	}
	for _, test := range tests {
		_, err := d.getDevicePathWithLUN(test.req)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
		}
	}
}
