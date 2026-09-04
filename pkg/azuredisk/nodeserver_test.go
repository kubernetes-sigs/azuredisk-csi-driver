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
	"log"
	"os"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v7"
	"github.com/container-storage-interface/spec/lib/go/csi"
	directvolume "github.com/kata-containers/kata-containers/src/runtime/pkg/direct-volume"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	testingexec "k8s.io/utils/exec/testing"
	"k8s.io/utils/ptr"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/mounter"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/optimization/mockoptimization"
	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/testutil"
	mockvmclient "sigs.k8s.io/cloud-provider-azure/pkg/azclient/virtualmachineclient/mock_virtualmachineclient"
)

const (
	virtualMachineURIFormat = "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachines/%s"
)

var (
	sourceTest string
	targetTest string

	testSubscription  = "01234567-89ab-cdef-0123-456789abcdef"
	testResourceGroup = "rg"

	provisioningStateSucceeded = "Succeeded"

	testVMName     = fakeNodeID
	testVMURI      = fmt.Sprintf(virtualMachineURIFormat, testSubscription, testResourceGroup, testVMName)
	testVMSize     = armcompute.VirtualMachineSizeTypesStandardD3V2
	testVMLocation = "westus"
	testVMZones    = []*string{ptr.To("1")}
	testVM         = armcompute.VirtualMachine{
		Name:     &testVMName,
		ID:       &testVMURI,
		Location: &testVMLocation,
		Zones:    testVMZones,
		Properties: &armcompute.VirtualMachineProperties{
			ProvisioningState: &provisioningStateSucceeded,
			HardwareProfile: &armcompute.HardwareProfile{
				VMSize: &testVMSize,
			},
			StorageProfile: &armcompute.StorageProfile{
				DataDisks: []*armcompute.DataDisk{},
			},
		},
	}
)

type kataStubDirectVolume struct {
	addMountInfo    func(string, directvolume.MountInfo) error
	remove          func(string) error
	isVolumeMounted func(string) (bool, error)
}

func (f *kataStubDirectVolume) AddMountInfo(volumePath string, mountInfo directvolume.MountInfo) error {
	return f.addMountInfo(volumePath, mountInfo)
}

func (f *kataStubDirectVolume) Remove(volumePath string) error {
	return f.remove(volumePath)
}

func (f *kataStubDirectVolume) IsVolumeMounted(volumePath string) (bool, error) {
	return f.isVolumeMounted(volumePath)
}

func TestMain(m *testing.M) {
	var err error
	sourceTest, err = testutil.GetWorkDirPath("source_test")
	if err != nil {
		log.Printf("failed to get source test path: %v\n", err)
		os.Exit(1)
	}
	targetTest, err = testutil.GetWorkDirPath("target_test")
	if err != nil {
		log.Printf("failed to get target test path: %v\n", err)
		os.Exit(1)
	}

	_ = m.Run()

}

func TestNodeGetCapabilities(t *testing.T) {
	cntl := gomock.NewController(t)
	defer cntl.Finish()
	d, _ := NewFakeDriver(cntl)
	capType := &csi.NodeServiceCapability_Rpc{
		Rpc: &csi.NodeServiceCapability_RPC{
			Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
		},
	}
	capList := []*csi.NodeServiceCapability{{
		Type: capType,
	}}
	d.setNodeCapabilities(capList)
	// Test valid request
	req := csi.NodeGetCapabilitiesRequest{}
	resp, err := d.NodeGetCapabilities(context.Background(), &req)
	assert.NotNil(t, resp)
	assert.Equal(t, resp.Capabilities[0].GetType(), capType)
	assert.NoError(t, err)
}

func TestKataGetMountPod(t *testing.T) {
	tests := []struct {
		name             string
		volumeContext    map[string]string
		runtimeClassName *string
		runtimeClass     *nodev1.RuntimeClass
		wantPod          bool
		wantErr          string
	}{
		{
			name: "annotated runtime class",
			volumeContext: map[string]string{
				podNameField:      "pod",
				podNamespaceField: "namespace",
			},
			runtimeClassName: ptr.To("kata"),
			runtimeClass: &nodev1.RuntimeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "kata",
					Annotations: map[string]string{kataRuntimeClassAnnotationKey: kataRuntimeClassAnnotationValue},
				},
				Handler: "kata",
			},
			wantPod: true,
		},
		{
			name: "runtime class without direct volume annotation",
			volumeContext: map[string]string{
				podNameField:      "pod",
				podNamespaceField: "namespace",
			},
			runtimeClassName: ptr.To("kata"),
			runtimeClass: &nodev1.RuntimeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "kata"},
				Handler:    "kata",
			},
		},
		{
			name: "runtime class not found",
			volumeContext: map[string]string{
				podNameField:      "pod",
				podNamespaceField: "namespace",
			},
			runtimeClassName: ptr.To("missing"),
			wantErr:          "get runtime class \"missing\"",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			objects := []k8sruntime.Object{}
			if test.runtimeClassName != nil {
				objects = append(objects, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "namespace"},
					Spec:       corev1.PodSpec{RuntimeClassName: test.runtimeClassName},
				})
			}
			if test.runtimeClass != nil {
				objects = append(objects, test.runtimeClass)
			}

			got, err := kataGetMountPod(context.Background(), fake.NewSimpleClientset(objects...), test.volumeContext)
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			if test.wantPod {
				require.NotNil(t, got)
				assert.Equal(t, "pod", got.Name)
			} else {
				assert.Nil(t, got)
			}
		})
	}
}

func TestGetPodFSGroup(t *testing.T) {
	fsGroup := int64(3000)
	zeroFSGroup := int64(0)
	onRootMismatch := corev1.FSGroupChangeOnRootMismatch
	filesystemVolume := &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{
		Mount: &csi.VolumeCapability_MountVolume{},
	}}
	blockVolume := &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Block{
		Block: &csi.VolumeCapability_BlockVolume{},
	}}

	tests := []struct {
		name                    string
		pod                     *corev1.Pod
		volumeCapability        *csi.VolumeCapability
		readOnly                bool
		wantFSGroup             *int64
		wantFSGroupChangePolicy *corev1.PodFSGroupChangePolicy
	}{
		{
			name: "fsGroup without policy",
			pod: &corev1.Pod{Spec: corev1.PodSpec{SecurityContext: &corev1.PodSecurityContext{
				FSGroup: &fsGroup,
			}}},
			volumeCapability: filesystemVolume,
			wantFSGroup:      &fsGroup,
		},
		{
			name: "zero group with OnRootMismatch",
			pod: &corev1.Pod{Spec: corev1.PodSpec{SecurityContext: &corev1.PodSecurityContext{
				FSGroup:             &zeroFSGroup,
				FSGroupChangePolicy: &onRootMismatch,
			}}},
			volumeCapability:        filesystemVolume,
			wantFSGroup:             &zeroFSGroup,
			wantFSGroupChangePolicy: &onRootMismatch,
		},
		{
			name:             "nil pod",
			volumeCapability: filesystemVolume,
		},
		{
			name:             "nil security context",
			pod:              &corev1.Pod{},
			volumeCapability: filesystemVolume,
		},
		{
			name: "nil fsGroup",
			pod: &corev1.Pod{Spec: corev1.PodSpec{
				SecurityContext: &corev1.PodSecurityContext{},
			}},
			volumeCapability: filesystemVolume,
		},
		{
			name: "container runAsGroup is not a volume fsGroup",
			pod: &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
				SecurityContext: &corev1.SecurityContext{RunAsGroup: &fsGroup},
			}}}},
			volumeCapability: filesystemVolume,
		},
		{
			name: "read-only filesystem",
			pod: &corev1.Pod{Spec: corev1.PodSpec{SecurityContext: &corev1.PodSecurityContext{
				FSGroup: &fsGroup,
			}}},
			volumeCapability: filesystemVolume,
			readOnly:         true,
		},
		{
			name: "block volume",
			pod: &corev1.Pod{Spec: corev1.PodSpec{SecurityContext: &corev1.PodSecurityContext{
				FSGroup: &fsGroup,
			}}},
			volumeCapability: blockVolume,
		},
		{
			name: "nil volume capability",
			pod: &corev1.Pod{Spec: corev1.PodSpec{SecurityContext: &corev1.PodSecurityContext{
				FSGroup: &fsGroup,
			}}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fsGroup, fsGroupChangePolicy := getPodFSGroup(test.pod, test.volumeCapability, test.readOnly)
			assert.Equal(t, test.wantFSGroup, fsGroup)
			assert.Equal(t, test.wantFSGroupChangePolicy, fsGroupChangePolicy)
		})
	}
}

func TestGetMaxDataDiskCount(t *testing.T) {
	tests := []struct {
		instanceType string
		expectResult int64
		expectExists bool
	}{
		{
			instanceType: "standard_d2_v2",
			expectResult: 8,
			expectExists: true,
		},
		{
			instanceType: "Standard_DS14_V2",
			expectResult: 64,
			expectExists: true,
		},
		{
			instanceType: "NOT_EXISTING",
			expectResult: defaultAzureVolumeLimit,
			expectExists: false,
		},
		{
			instanceType: "",
			expectResult: defaultAzureVolumeLimit,
			expectExists: false,
		},
	}

	for _, test := range tests {
		result, exists := GetMaxDataDiskCount(test.instanceType)
		assert.Equal(t, test.expectResult, result)
		assert.Equal(t, test.expectExists, exists)
	}
}

func TestEnsureMountPoint(t *testing.T) {
	errorTarget, err := testutil.GetWorkDirPath("error_is_likely_target")
	assert.NoError(t, err)
	alreadyExistTarget, err := testutil.GetWorkDirPath("false_is_likely_exist_target")
	assert.NoError(t, err)
	azuredisk, err := testutil.GetWorkDirPath("azuredisk.go")
	assert.NoError(t, err)

	tests := []struct {
		desc          string
		target        string
		skipOnWindows bool
		skipOnDarwin  bool
		expectedMnt   bool
		expectedErr   testutil.TestError
	}{
		{
			desc:          "[Error] Mocked by IsLikelyNotMountPoint",
			target:        errorTarget,
			skipOnWindows: true, // no error reported in windows
			expectedErr: testutil.TestError{
				DefaultError: errors.New("fake IsLikelyNotMountPoint: fake error"),
			},
			expectedMnt: false,
		},
		{
			desc:          "[Error] Not a directory",
			target:        azuredisk,
			skipOnWindows: true, // no error reported in windows
			skipOnDarwin:  true,
			expectedErr: testutil.TestError{
				DefaultError: &os.PathError{Op: "mkdir", Path: azuredisk, Err: syscall.ENOTDIR},
			},
		},
		{
			desc:        "[Success] Successful run",
			target:      targetTest,
			expectedErr: testutil.TestError{},
			expectedMnt: false,
		},
		{
			desc:          "[Success] Already existing mount",
			target:        alreadyExistTarget,
			skipOnWindows: true, // not consistent result between Linux and Windows
			expectedErr:   testutil.TestError{},
			expectedMnt:   true,
		},
	}

	for _, test := range tests {
		// Setup
		cntl := gomock.NewController(t)
		_ = makeDir(alreadyExistTarget)
		d, _ := NewFakeDriver(cntl)
		fakeMounter, err := mounter.NewFakeSafeMounter()
		assert.NoError(t, err)
		d.setMounter(fakeMounter)
		if !(runtime.GOOS == "windows" && test.skipOnWindows) && !(runtime.GOOS == "darwin" && test.skipOnDarwin) {
			mnt, err := d.ensureMountPoint(test.target)
			if !testutil.AssertError(&test.expectedErr, err) {
				t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
			}
			if err == nil {
				assert.Equal(t, test.expectedMnt, mnt)
			}
		}
		// Clean up
		err = os.RemoveAll(alreadyExistTarget)
		assert.NoError(t, err)
		err = os.RemoveAll(targetTest)
		assert.NoError(t, err)
		cntl.Finish()
	}

}

func TestNodeGetInfo(t *testing.T) {
	notFoundErr := errors.New("not found")

	tests := []struct {
		desc         string
		expectedErr  error
		skipOnDarwin bool
		setupFunc    func(_ *testing.T, _ FakeDriver)
		validateFunc func(_ *testing.T, _ *csi.NodeGetInfoResponse)
	}{
		{
			desc:         "[Success] Get node information for existing VM",
			expectedErr:  nil,
			skipOnDarwin: true,
			setupFunc: func(t *testing.T, d FakeDriver) {
				mockVMClient := d.getCloud().ComputeClientFactory.GetVirtualMachineClient().(*mockvmclient.MockInterface)
				mockVMClient.EXPECT().
					Get(gomock.Any(), testResourceGroup, testVMName, gomock.Any()).
					Return(&testVM, nil).
					AnyTimes()

				// cloud-provider-azure's GetZone function assumes the host is a VM and returns it zones.
				// We therefore mock a return of the testVM if the hostname is used.
				hostname, err := os.Hostname()
				require.NoError(t, err)

				mockVMClient.EXPECT().
					Get(gomock.Any(), testResourceGroup, hostname, gomock.Any()).
					Return(&testVM, nil).
					AnyTimes()
				mockVMClient.EXPECT().
					Get(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Return(&armcompute.VirtualMachine{}, notFoundErr).
					AnyTimes()
			},
			validateFunc: func(t *testing.T, resp *csi.NodeGetInfoResponse) {
				assert.Equal(t, testVMName, resp.NodeId)
				maxDiskdataCount, _ := GetMaxDataDiskCount(string(testVMSize))
				assert.Equal(t, maxDiskdataCount, resp.MaxVolumesPerNode)
				assert.Len(t, resp.AccessibleTopology.Segments, 2)
			},
		},
		{
			desc:        "[Failure] Get node information for non-existing VM",
			expectedErr: status.Error(codes.Internal, fmt.Sprintf("GetNodeInfoFromLabels on node(%s) failed with %s", "fakeNodeID", "kubeClient is nil")),
			setupFunc: func(_ *testing.T, d FakeDriver) {
				mockVMClient := d.getCloud().ComputeClientFactory.GetVirtualMachineClient().(*mockvmclient.MockInterface)
				mockVMClient.EXPECT().
					Get(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Return(&armcompute.VirtualMachine{}, notFoundErr).
					AnyTimes()
			},
			validateFunc: func(t *testing.T, resp *csi.NodeGetInfoResponse) {
				assert.Equal(t, testVMName, resp.NodeId)
				assert.Equal(t, int64(defaultAzureVolumeLimit), resp.MaxVolumesPerNode)
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.desc, func(t *testing.T) {
			cntl := gomock.NewController(t)
			defer cntl.Finish()
			if test.skipOnDarwin && runtime.GOOS == "darwin" {
				t.Skip("Skip test case on Darwin")
			}
			d, err := NewFakeDriver(cntl)
			require.NoError(t, err)

			test.setupFunc(t, d)

			resp, err := d.NodeGetInfo(context.TODO(), &csi.NodeGetInfoRequest{})
			require.Equal(t, test.expectedErr, err)
			if err == nil {
				test.validateFunc(t, resp)
			}
		})
	}
}

func TestNodeGetVolumeStats(t *testing.T) {
	nonexistedPath := "/not/a/real/directory"
	fakePath := "/tmp/fake-volume-path"
	blockVolumePath := "/tmp/block-volume-path"
	blockdevAction := func() ([]byte, []byte, error) {
		return []byte(fmt.Sprintf("%d", stdCapacityRange.RequiredBytes)), []byte{}, nil
	}
	tests := []struct {
		desc          string
		setupFunc     func(*testing.T, FakeDriver)
		req           *csi.NodeGetVolumeStatsRequest
		expectedErr   error
		skipOnDarwin  bool
		skipOnWindows bool
	}{
		{
			desc:        "Volume ID missing",
			req:         &csi.NodeGetVolumeStatsRequest{VolumePath: targetTest},
			expectedErr: status.Error(codes.InvalidArgument, "NodeGetVolumeStats volume ID was empty"),
		},
		{
			desc:        "VolumePath missing",
			req:         &csi.NodeGetVolumeStatsRequest{VolumeId: "vol_1"},
			expectedErr: status.Error(codes.InvalidArgument, "NodeGetVolumeStats volume path was empty"),
		},
		{
			desc:          "Not existed volume path",
			req:           &csi.NodeGetVolumeStatsRequest{VolumePath: nonexistedPath, VolumeId: "vol_1"},
			expectedErr:   status.Errorf(codes.NotFound, "path /not/a/real/directory does not exist"),
			skipOnWindows: true,
			skipOnDarwin:  true,
		},
		{
			desc: "Block volume path success",
			setupFunc: func(_ *testing.T, d FakeDriver) {
				d.getHostUtil().(*azureutils.FakeHostUtil).SetPathIsDeviceResult(blockVolumePath, true, nil)
				d.setNextCommandOutputScripts(blockdevAction)
			},
			req:           &csi.NodeGetVolumeStatsRequest{VolumePath: blockVolumePath, VolumeId: "vol_1"},
			skipOnDarwin:  true,
			skipOnWindows: true,
			expectedErr:   nil,
		},
		{
			desc:          "standard success",
			req:           &csi.NodeGetVolumeStatsRequest{VolumePath: fakePath, VolumeId: "vol_1"},
			skipOnDarwin:  true,
			skipOnWindows: true,
			expectedErr:   nil,
		},
		{
			desc: "failed to determine block device",
			setupFunc: func(_ *testing.T, d FakeDriver) {
				d.getHostUtil().(*azureutils.FakeHostUtil).SetPathIsDeviceResult(fakePath, true, fmt.Errorf("host util is not device path"))
			},
			req:           &csi.NodeGetVolumeStatsRequest{VolumePath: fakePath, VolumeId: "vol_1"},
			skipOnDarwin:  true,
			skipOnWindows: true,
			expectedErr:   status.Errorf(codes.NotFound, "failed to determine whether %s is block device: %v", fakePath, fmt.Errorf("host util is not device path")),
		},
	}

	for _, test := range tests {
		cntl := gomock.NewController(t)
		// Setup
		_ = makeDir(fakePath)
		_ = makeDir(blockVolumePath)
		d, _ := NewFakeDriver(cntl)
		mounter, err := mounter.NewFakeSafeMounter()
		assert.NoError(t, err)
		d.setMounter(mounter)
		if !(test.skipOnDarwin && runtime.GOOS == "darwin") && !(test.skipOnWindows && runtime.GOOS == "windows") {
			if test.setupFunc != nil {
				test.setupFunc(t, d)
			}
			_, err := d.NodeGetVolumeStats(context.Background(), test.req)
			if !reflect.DeepEqual(err, test.expectedErr) {
				t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
			}
		}
		// Clean up
		err = os.RemoveAll(fakePath)
		assert.NoError(t, err)
		err = os.RemoveAll(blockVolumePath)
		assert.NoError(t, err)
		cntl.Finish()
	}
}

func TestKataVolumeStatsAndResizeNotSupported(t *testing.T) {
	cntl := gomock.NewController(t)
	d, _ := NewFakeDriver(cntl)
	target, err := testutil.GetWorkDirPath("direct_volume_operation_target")
	require.NoError(t, err)
	fallbackStatsTarget, err := testutil.GetWorkDirPath("direct_volume_stats_probe_error_target")
	require.NoError(t, err)
	require.NoError(t, makeDir(fallbackStatsTarget))
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(fallbackStatsTarget)) })
	fallbackResizeTarget := "/tmp/direct-volume-resize-probe-error-target"
	d.getHostUtil().(*azureutils.FakeHostUtil).SetPathIsDeviceResult(fallbackResizeTarget, true, nil)
	fakeMounter, err := mounter.NewFakeSafeMounter()
	require.NoError(t, err)
	d.setMounter(fakeMounter)

	d.(*fakeDriver).kataDirectVolume = &kataStubDirectVolume{
		isVolumeMounted: func(path string) (bool, error) {
			if path == fallbackStatsTarget || path == fallbackResizeTarget {
				return false, errors.New("fake direct volume probe error")
			}
			return path == target, nil
		},
	}

	_, err = d.NodeGetVolumeStats(context.Background(), &csi.NodeGetVolumeStatsRequest{
		VolumeId:   "vol_1",
		VolumePath: target,
	})
	assert.Error(t, err)

	_, err = d.NodeExpandVolume(context.Background(), &csi.NodeExpandVolumeRequest{
		VolumeId:   "vol_1",
		VolumePath: target,
	})
	assert.Error(t, err)

	_, err = d.NodeGetVolumeStats(context.Background(), &csi.NodeGetVolumeStatsRequest{
		VolumeId:   "vol_1",
		VolumePath: fallbackStatsTarget,
	})
	assert.NoError(t, err)

	_, err = d.NodeExpandVolume(context.Background(), &csi.NodeExpandVolumeRequest{
		VolumeId:   "vol_1",
		VolumePath: fallbackResizeTarget,
	})
	assert.NoError(t, err)
}

// TestResolveFSType runs on every platform, including the Windows CI runner, because it
// needs no mounter: NewFakeSafeMounter returns a real csi-proxy mounter on Windows, which
// is why the NodeStageVolume cases below are all skipped there.
func TestResolveFSType(t *testing.T) {
	mountCap := func(fsType string, flags ...string) *csi.VolumeCapability {
		return &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Mount{
				Mount: &csi.VolumeCapability_MountVolume{FsType: fsType, MountFlags: flags},
			},
		}
	}
	blockCap := &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}},
	}

	t.Run("block access type has no fsType", func(t *testing.T) {
		fstype, flags, err := resolveFSType(blockCap, map[string]string{consts.FsTypeField: "vfat"})
		require.NoError(t, err)
		assert.Empty(t, fstype)
		assert.Empty(t, flags)
	})

	t.Run("default is used when nothing is set", func(t *testing.T) {
		fstype, _, err := resolveFSType(mountCap(""), nil)
		require.NoError(t, err)
		assert.Equal(t, getDefaultFsType(), fstype)
	})

	t.Run("mount flags are preserved", func(t *testing.T) {
		_, flags, err := resolveFSType(mountCap("", "barrier=1", "acl"), nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"barrier=1", "acl"}, flags)
	})

	if runtime.GOOS == "windows" {
		// The Windows mounters ignore fsType and always format NTFS, so an unsupported
		// value must degrade to the default rather than fail staging.
		t.Run("unsupported fsType falls back to ntfs", func(t *testing.T) {
			for _, fsType := range []string{"ext4", "xfs", "btrfs", "vfat", "ext4;rm -rf /"} {
				fstype, flags, err := resolveFSType(mountCap(fsType, "acl"), nil)
				require.NoError(t, err, "fsType %q must not fail staging on windows", fsType)
				assert.Equal(t, defaultWindowsFsType, fstype, "fsType %q", fsType)
				assert.Equal(t, []string{"acl"}, flags, "fsType %q", fsType)
			}
		})

		t.Run("unsupported fsType from volumeAttributes falls back to ntfs", func(t *testing.T) {
			fstype, _, err := resolveFSType(mountCap("ntfs"), map[string]string{consts.FsTypeField: "ext4"})
			require.NoError(t, err)
			assert.Equal(t, defaultWindowsFsType, fstype)
		})

		t.Run("ntfs is accepted", func(t *testing.T) {
			fstype, _, err := resolveFSType(mountCap("NTFS"), nil)
			require.NoError(t, err)
			assert.Equal(t, defaultWindowsFsType, fstype)
		})
	} else {
		t.Run("unsupported fsType is rejected", func(t *testing.T) {
			for _, fsType := range []string{"ntfs", "vfat", "ext4;rm -rf /"} {
				_, _, err := resolveFSType(mountCap(fsType), nil)
				require.Error(t, err, "fsType %q must be rejected", fsType)
			}
		})

		t.Run("volumeAttributes overrides the capability", func(t *testing.T) {
			fstype, _, err := resolveFSType(mountCap("ext4"), map[string]string{consts.FsTypeField: "xfs"})
			require.NoError(t, err)
			assert.Equal(t, "xfs", fstype)
		})

		t.Run("casing is normalized", func(t *testing.T) {
			fstype, _, err := resolveFSType(mountCap("XFS"), nil)
			require.NoError(t, err)
			assert.Equal(t, "xfs", fstype)
		})
	}
}

// assertMountedXFSWithNouuid checks that the fstype resolved by NodeStageVolume is what
// actually reached mount, and that collectMountOptions saw it (xfs implies "nouuid").
func assertMountedXFSWithNouuid(t *testing.T, d FakeDriver) {
	t.Helper()
	fake, ok := d.getMounter().Interface.(*mounter.FakeSafeMounter)
	require.True(t, ok, "expected a FakeSafeMounter")
	require.Len(t, fake.MountCalls, 1, "expected exactly one mount")
	assert.Equal(t, "xfs", fake.MountCalls[0].FSType)
	assert.Contains(t, fake.MountCalls[0].Options, "nouuid")
}

func TestNodeStageVolume(t *testing.T) {
	cntl := gomock.NewController(t)
	defer cntl.Finish()
	d, _ := NewFakeDriver(cntl)

	stdVolCap := &csi.VolumeCapability_Mount{
		Mount: &csi.VolumeCapability_MountVolume{
			FsType: defaultLinuxFsType,
		},
	}
	volumeContext := map[string]string{
		consts.FsTypeField: defaultLinuxFsType,
	}
	volumeContextWithResize := map[string]string{
		consts.ResizeRequired: "true",
	}
	volumeContextWithMaxShare := map[string]string{
		consts.MaxSharesField: "0.1",
	}
	volumeContextWithPerfProfileField := map[string]string{
		consts.PerfProfileField: "wrong",
	}
	volumeContextWithInvalidFSType := map[string]string{
		consts.FsTypeField: "vfat",
	}
	volumeContextWithXFS := map[string]string{
		consts.FsTypeField: "xfs",
	}
	volumeContextWithNTFS := map[string]string{
		consts.FsTypeField: "ntfs",
	}

	// fsType casing is normalized before it reaches mkfs/mount and collectMountOptions.
	stdVolCapUpperXFS := &csi.VolumeCapability_Mount{
		Mount: &csi.VolumeCapability_MountVolume{
			FsType: "XFS",
		},
	}

	stdVolCapBlock := &csi.VolumeCapability_Block{
		Block: &csi.VolumeCapability_BlockVolume{},
	}

	volumeCap := csi.VolumeCapability_AccessMode{Mode: 2}
	volumeCapWrong := csi.VolumeCapability_AccessMode{Mode: 10}
	invalidLUN := map[string]string{
		consts.LUN: "/dev/01",
	}
	publishContext := map[string]string{
		consts.LUN: "/dev/disk/azure/scsi1/lun1",
	}

	blkidAction := func() ([]byte, []byte, error) {
		return []byte("DEVICE=/dev/sdd\nTYPE=ext4"), []byte{}, nil
	}
	xfsBlkidAction := func() ([]byte, []byte, error) {
		return []byte("DEVICE=/dev/sdd\nTYPE=xfs"), []byte{}, nil
	}
	fsckAction := func() ([]byte, []byte, error) {
		return []byte{}, []byte{}, nil
	}
	blockSizeAction := func() ([]byte, []byte, error) {
		return []byte(fmt.Sprintf("%d", stdCapacityRange.RequiredBytes)), []byte{}, nil
	}
	resize2fsAction := func() ([]byte, []byte, error) {
		return []byte{}, []byte{}, nil
	}

	tests := []struct {
		desc        string
		setupFunc   func(*testing.T, FakeDriver)
		req         *csi.NodeStageVolumeRequest
		expectedErr error
		// expectedCode/expectedErrContains assert on the status code plus message
		// substrings, for cases where the exact wording is not what's under test.
		expectedCode        codes.Code
		expectedErrContains []string
		skipOnDarwin        bool
		skipOnWindows       bool
		validateFunc        func(*testing.T, FakeDriver)
		cleanupFunc         func(*testing.T, FakeDriver)
	}{
		{
			desc:        "Volume ID missing",
			req:         &csi.NodeStageVolumeRequest{},
			expectedErr: status.Error(codes.InvalidArgument, "Volume ID not provided"),
		},
		{
			desc:        "Stage target path missing",
			req:         &csi.NodeStageVolumeRequest{VolumeId: "vol_1"},
			expectedErr: status.Error(codes.InvalidArgument, "Staging target not provided"),
		},
		{
			desc:        "Volume capabilities missing",
			req:         &csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest},
			expectedErr: status.Error(codes.InvalidArgument, "Volume capability not provided"),
		},
		{
			desc:        "Volume capabilities not supported",
			req:         &csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest, VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCapWrong}},
			expectedErr: status.Error(codes.InvalidArgument, "invalid access mode: [access_mode:{mode:10}]"),
		},
		{
			desc: "MaxShares value not supported",
			req: &csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest, VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap,
				AccessType: stdVolCapBlock}, VolumeContext: volumeContextWithMaxShare},
			expectedErr: status.Error(codes.InvalidArgument, "MaxShares value not supported"),
		},
		{
			desc: "Volume operation in progress",
			setupFunc: func(_ *testing.T, d FakeDriver) {
				d.getVolumeLocks().TryAcquire("vol_1")
			},
			req: &csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest, VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap,
				AccessType: stdVolCapBlock}},
			expectedErr: status.Error(codes.Aborted, fmt.Sprintf(volumeOperationAlreadyExistsFmt, "vol_1")),
			cleanupFunc: func(_ *testing.T, d FakeDriver) {
				d.getVolumeLocks().Release("vol_1")
			},
		},
		{
			desc: "Lun not provided",
			req: &csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest, VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap,
				AccessType: stdVolCap}},
			expectedErr: status.Error(codes.InvalidArgument, "lun not provided"),
		},
		{
			desc:         "Invalid Lun",
			skipOnDarwin: true,
			req: &csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest,
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap,
					AccessType: stdVolCap},
				PublishContext: invalidLUN,
				VolumeContext:  volumeContext,
			},
			expectedErr: status.Error(codes.Internal, "failed to find disk on lun /dev/01. cannot parse deviceInfo: /dev/01"),
		},
		{
			desc:          "Unsupported fsType",
			skipOnDarwin:  true,
			skipOnWindows: true,
			req: &csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest,
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap,
					AccessType: stdVolCap},
				PublishContext: publishContext,
				VolumeContext:  volumeContextWithInvalidFSType,
			},
			expectedCode:        codes.InvalidArgument,
			expectedErrContains: []string{"vfat", "not supported"},
		},
		{
			// No PublishContext: fsType must be rejected before the LUN is even read, so
			// that a bad value cannot first trigger a device rescan that polls for minutes.
			desc:          "invalid fsType rejected before LUN handling",
			skipOnDarwin:  true,
			skipOnWindows: true,
			req: &csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest,
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap,
					AccessType: stdVolCap},
				VolumeContext: volumeContextWithInvalidFSType,
			},
			expectedCode:        codes.InvalidArgument,
			expectedErrContains: []string{"vfat", "not supported"},
		},
		{
			// The Linux image ships no NTFS formatter. Windows takes the opposite path and
			// falls back to the default instead of erroring, which needs a Windows runner.
			desc:          "ntfs rejected on Linux",
			skipOnDarwin:  true,
			skipOnWindows: true,
			req: &csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest,
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap,
					AccessType: stdVolCap},
				PublishContext: publishContext,
				VolumeContext:  volumeContextWithNTFS,
			},
			expectedCode:        codes.InvalidArgument,
			expectedErrContains: []string{"ntfs", "not supported"},
		},
		{
			// volumeAttributes.fsType overrides the volume capability, so "nouuid" must be
			// derived from the override rather than from the capability's ext4.
			desc:          "xfs from volumeAttributes overrides volume capability and gets nouuid",
			skipOnDarwin:  true,
			skipOnWindows: true,
			setupFunc: func(_ *testing.T, d FakeDriver) {
				d.setNextCommandOutputScripts(xfsBlkidAction, fsckAction, blockSizeAction, xfsBlkidAction, blockSizeAction, xfsBlkidAction)
			},
			req: &csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest,
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap,
					AccessType: stdVolCap},
				PublishContext: publishContext,
				VolumeContext:  volumeContextWithXFS,
			},
			expectedErr:  nil,
			validateFunc: assertMountedXFSWithNouuid,
		},
		{
			desc:          "uppercase XFS in volume capability is normalized and gets nouuid",
			skipOnDarwin:  true,
			skipOnWindows: true,
			setupFunc: func(_ *testing.T, d FakeDriver) {
				d.setNextCommandOutputScripts(xfsBlkidAction, fsckAction, blockSizeAction, xfsBlkidAction, blockSizeAction, xfsBlkidAction)
			},
			req: &csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest,
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap,
					AccessType: stdVolCapUpperXFS},
				PublishContext: publishContext,
			},
			expectedErr:  nil,
			validateFunc: assertMountedXFSWithNouuid,
		},
		{
			desc:          "Successfully staged",
			skipOnDarwin:  true,
			skipOnWindows: true,
			setupFunc: func(_ *testing.T, d FakeDriver) {
				d.setNextCommandOutputScripts(blkidAction, fsckAction, blockSizeAction, blkidAction, blockSizeAction, blkidAction)
			},
			req: &csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest,
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap,
					AccessType: stdVolCap},
				PublishContext: publishContext,
				VolumeContext:  volumeContext,
			},
			expectedErr: nil,
		},
		{
			desc:          "Successfully with resize",
			skipOnDarwin:  true,
			skipOnWindows: true,
			setupFunc: func(_ *testing.T, d FakeDriver) {
				d.setNextCommandOutputScripts(blkidAction, fsckAction, blkidAction, resize2fsAction)
			},
			req: &csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest,
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap,
					AccessType: stdVolCap},
				PublishContext: publishContext,
				VolumeContext:  volumeContextWithResize,
			},
			expectedErr: nil,
		},
		{
			desc:          "failed to get perf attributes",
			skipOnDarwin:  true,
			skipOnWindows: true,
			setupFunc: func(_ *testing.T, d FakeDriver) {
				d.setPerfOptimizationEnabled(true)
				d.setNextCommandOutputScripts(blkidAction, fsckAction, blockSizeAction, blockSizeAction)
			},
			req: &csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest,
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap,
					AccessType: stdVolCap},
				PublishContext: publishContext,
				VolumeContext:  volumeContextWithPerfProfileField,
			},
			cleanupFunc: func(_ *testing.T, d FakeDriver) {
				d.setPerfOptimizationEnabled(false)
			},
			expectedErr: status.Errorf(codes.Internal, "failed to get perf attributes for /dev/sdd. Error: %v", fmt.Errorf("Perf profile wrong is invalid")),
		},
		{
			desc:          "Successfully staged with performance optimizations",
			skipOnDarwin:  true,
			skipOnWindows: true,
			setupFunc: func(_ *testing.T, d FakeDriver) {
				d.setPerfOptimizationEnabled(true)
				mockoptimization := d.getDeviceHelper().(*mockoptimization.MockInterface)
				diskSupportsPerfOptimizationCall := mockoptimization.EXPECT().
					DiskSupportsPerfOptimization(gomock.Any(), gomock.Any()).
					Return(true)
				mockoptimization.EXPECT().
					OptimizeDiskPerformance(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Return(nil).
					After(diskSupportsPerfOptimizationCall)

				d.setNextCommandOutputScripts(blkidAction, fsckAction, blockSizeAction, blockSizeAction)
			},
			req: &csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest,
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap,
					AccessType: stdVolCap},
				PublishContext: publishContext,
				VolumeContext:  volumeContext,
			},
			cleanupFunc: func(_ *testing.T, d FakeDriver) {
				d.setPerfOptimizationEnabled(false)
			},
			expectedErr: nil,
		},
		{
			desc:          "failed to optimize device performance",
			skipOnDarwin:  true,
			skipOnWindows: true,
			setupFunc: func(_ *testing.T, d FakeDriver) {
				d.setPerfOptimizationEnabled(true)
				mockoptimization := d.getDeviceHelper().(*mockoptimization.MockInterface)
				diskSupportsPerfOptimizationCall := mockoptimization.EXPECT().
					DiskSupportsPerfOptimization(gomock.Any(), gomock.Any()).
					Return(true)
				mockoptimization.EXPECT().
					OptimizeDiskPerformance(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Return(fmt.Errorf("failed to optimize device performance")).
					After(diskSupportsPerfOptimizationCall)

				d.setNextCommandOutputScripts(blkidAction, fsckAction, blockSizeAction, blockSizeAction)
			},
			req: &csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest,
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap,
					AccessType: stdVolCap},
				PublishContext: publishContext,
				VolumeContext:  volumeContext,
			},
			cleanupFunc: func(_ *testing.T, d FakeDriver) {
				d.setPerfOptimizationEnabled(false)
			},
			expectedErr: status.Errorf(codes.Internal, "failed to optimize device performance for target(/dev/sdd) error(%s)", fmt.Errorf("failed to optimize device performance")),
		},
		{
			desc:          "Successfully staged with perf optimization is disabled",
			skipOnDarwin:  true,
			skipOnWindows: true,
			setupFunc: func(_ *testing.T, d FakeDriver) {
				d.setPerfOptimizationEnabled(true)
				mockoptimization := d.getDeviceHelper().(*mockoptimization.MockInterface)
				mockoptimization.EXPECT().
					DiskSupportsPerfOptimization(gomock.Any(), gomock.Any()).
					Return(false)

				d.setNextCommandOutputScripts(blkidAction, fsckAction, blockSizeAction, blockSizeAction)
			},
			req: &csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest,
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap,
					AccessType: stdVolCap},
				PublishContext: publishContext,
				VolumeContext:  volumeContext,
			},
			cleanupFunc: func(_ *testing.T, d FakeDriver) {
				d.setPerfOptimizationEnabled(false)
			},
			expectedErr: nil,
		},
	}

	for _, test := range tests {
		// Setup
		_ = makeDir(sourceTest)
		_ = makeDir(targetTest)
		fakeMounter, err := mounter.NewFakeSafeMounter()
		assert.NoError(t, err)
		d.setMounter(fakeMounter)
		if !(test.skipOnDarwin && runtime.GOOS == "darwin") && !(test.skipOnWindows && runtime.GOOS == "windows") {
			if test.setupFunc != nil {
				test.setupFunc(t, d)
			}
			_, err := d.NodeStageVolume(context.Background(), test.req)
			if test.desc == "Failed volume mount" {
				assert.Error(t, err)
			} else if len(test.expectedErrContains) > 0 {
				require.Error(t, err)
				assert.Equal(t, test.expectedCode, status.Code(err), "desc: %s", test.desc)
				for _, substr := range test.expectedErrContains {
					assert.Contains(t, status.Convert(err).Message(), substr, "desc: %s", test.desc)
				}
			} else if !reflect.DeepEqual(err, test.expectedErr) {
				t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
			}
			if test.validateFunc != nil {
				test.validateFunc(t, d)
			}
			if test.cleanupFunc != nil {
				test.cleanupFunc(t, d)
			}
		}
		// Clean up
		err = os.RemoveAll(sourceTest)
		assert.NoError(t, err)
		err = os.RemoveAll(targetTest)
		assert.NoError(t, err)
	}

}

func TestNodeUnstageVolume(t *testing.T) {
	cntl := gomock.NewController(t)
	defer cntl.Finish()
	d, _ := NewFakeDriver(cntl)
	targetFile, err := testutil.GetWorkDirPath("abc.go")
	assert.NoError(t, err)

	tests := []struct {
		setup         func()
		desc          string
		req           *csi.NodeUnstageVolumeRequest
		skipOnWindows bool
		skipOnDarwin  bool
		expectedErr   testutil.TestError
		cleanup       func()
	}{
		{
			desc: "Volume ID missing",
			req:  &csi.NodeUnstageVolumeRequest{StagingTargetPath: targetTest},
			expectedErr: testutil.TestError{
				DefaultError: status.Error(codes.InvalidArgument, "Volume ID not provided"),
			},
		},
		{
			desc: "Staging target missing ",
			req:  &csi.NodeUnstageVolumeRequest{VolumeId: "vol_1"},
			expectedErr: testutil.TestError{
				DefaultError: status.Error(codes.InvalidArgument, "Staging target not provided"),
			},
		},
		{
			desc: "[Error] Volume operation in progress",
			setup: func() {
				d.getVolumeLocks().TryAcquire("vol_1")
			},
			req: &csi.NodeUnstageVolumeRequest{StagingTargetPath: targetFile, VolumeId: "vol_1"},
			expectedErr: testutil.TestError{
				DefaultError: status.Error(codes.Aborted, fmt.Sprintf(volumeOperationAlreadyExistsFmt, "vol_1")),
			},
			cleanup: func() {
				d.getVolumeLocks().Release("vol_1")
			},
		},
		{
			desc:          "[Success] Valid request",
			req:           &csi.NodeUnstageVolumeRequest{StagingTargetPath: targetFile, VolumeId: "vol_1"},
			skipOnWindows: true, // error on Windows
			expectedErr:   testutil.TestError{},
		},
	}

	for _, test := range tests {
		if test.setup != nil {
			test.setup()
		}
		if !(runtime.GOOS == "windows" && test.skipOnWindows) &&
			!(runtime.GOOS == "darwin" && test.skipOnDarwin) {
			_, err := d.NodeUnstageVolume(context.Background(), test.req)
			if !testutil.AssertError(&test.expectedErr, err) {
				t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
			}
		}
		if test.cleanup != nil {
			test.cleanup()
		}
	}
}

func TestNodePublishVolume(t *testing.T) {
	cntl := gomock.NewController(t)
	defer cntl.Finish()
	d, _ := NewFakeDriver(cntl)

	volumeCap := csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER}
	volumeCapRWO := csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_MULTI_WRITER}
	volumeCapSingleWriter := csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}
	volumeCapReadOnlyMany := csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY}
	volumeCapWrong := csi.VolumeCapability_AccessMode{Mode: 10}
	volumeContextWithMaxShare := map[string]string{
		consts.MaxSharesField: "0.1",
	}
	invalidPublishContext := map[string]string{
		consts.LUN: "/dev/01",
	}
	publishContext := map[string]string{
		consts.LUN: "/dev/disk/azure/scsi1/lun1",
	}
	directVolumeContext := map[string]string{
		podNameField:      "direct-volume-pod",
		podNamespaceField: "default",
	}
	directFSGroupVolumeContext := map[string]string{
		podNameField:      "direct-volume-fsgroup-pod",
		podNamespaceField: "default",
	}
	directVolumeErrorTarget, err := testutil.GetWorkDirPath("direct_volume_error_target")
	assert.NoError(t, err)
	standardTarget, err := testutil.GetWorkDirPath("standard_publish_target")
	assert.NoError(t, err)
	lookupErrorTarget, err := testutil.GetWorkDirPath("runtime_lookup_error_target")
	assert.NoError(t, err)
	directBlockTarget, err := testutil.GetWorkDirPath("direct_block_target")
	assert.NoError(t, err)
	directFSGroupTarget, err := testutil.GetWorkDirPath("direct_fsgroup_target")
	assert.NoError(t, err)

	stdVolCap := &csi.VolumeCapability_Mount{
		Mount: &csi.VolumeCapability_MountVolume{
			MountFlags: []string{"discard"},
		},
	}
	stdVolCapBlock := &csi.VolumeCapability_Block{
		Block: &csi.VolumeCapability_BlockVolume{},
	}

	tests := []struct {
		desc          string
		setup         func()
		req           *csi.NodePublishVolumeRequest
		skipOnWindows bool
		skipOnDarwin  bool
		expectedErr   testutil.TestError
		expectedInfo  *directvolume.MountInfo
		expectedMount bool
		expectTarget  bool
		cleanup       func()
	}{
		{
			desc: "Volume capabilities missing",
			req:  &csi.NodePublishVolumeRequest{VolumeId: "vol_1"},
			expectedErr: testutil.TestError{
				DefaultError: status.Error(codes.InvalidArgument, "Volume capability missing in request"),
			},
		},
		{
			desc: "Volume ID missing",
			req:  &csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: stdVolCapBlock}},
			expectedErr: testutil.TestError{
				DefaultError: status.Error(codes.InvalidArgument, "Volume ID missing in the request"),
			},
		},
		{
			desc: "MaxShares value not supported",
			req: &csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: stdVolCapBlock},
				VolumeId:      "vol_1",
				VolumeContext: volumeContextWithMaxShare},
			expectedErr: testutil.TestError{
				DefaultError: status.Error(codes.InvalidArgument, "MaxShares value not supported"),
			},
		},
		{
			desc: "Volume capability not supported",
			req: &csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCapWrong, AccessType: stdVolCapBlock},
				VolumeId: "vol_1"},
			expectedErr: testutil.TestError{
				DefaultError: status.Error(codes.InvalidArgument, "invalid access mode: [block:{}  access_mode:{mode:10}]"),
			},
		},
		{
			desc: "Staging target path missing",
			req: &csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: stdVolCapBlock},
				VolumeId: "vol_1"},
			expectedErr: testutil.TestError{
				DefaultError: status.Error(codes.InvalidArgument, "Staging target not provided"),
			},
		},
		{
			desc: "Target path missing",
			req: &csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: stdVolCapBlock},
				VolumeId:          "vol_1",
				StagingTargetPath: sourceTest},
			expectedErr: testutil.TestError{
				DefaultError: status.Error(codes.InvalidArgument, "Target path not provided"),
			},
		},
		{
			desc: "[Error] Lun not provided",
			req: &csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: stdVolCapBlock},
				VolumeId:          "vol_1",
				TargetPath:        targetTest,
				StagingTargetPath: sourceTest,
				Readonly:          true},
			expectedErr: testutil.TestError{
				DefaultError: status.Error(codes.InvalidArgument, "lun not provided"),
			},
		},
		{
			desc: "[Error] Lun not valid",
			req: &csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: stdVolCapBlock},
				VolumeId:          "vol_1",
				TargetPath:        targetTest,
				StagingTargetPath: sourceTest,
				PublishContext:    invalidPublishContext,
				Readonly:          true},
			expectedErr: testutil.TestError{
				DefaultError: status.Error(codes.Internal, "failed to find device path with lun /dev/01. cannot parse deviceInfo: /dev/01"),
			},
		},
		{
			desc: "[Success] Standard mount for pod without runtime metadata",
			req: &csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCapRWO, AccessType: stdVolCap},
				VolumeId:          "vol_1",
				TargetPath:        standardTarget,
				StagingTargetPath: sourceTest,
				Readonly:          true},
			expectedErr:   testutil.TestError{},
			expectedMount: true,
		},
		{
			desc: "[Error] Kata filesystem volume with ReadWriteOnce access mode",
			req: &csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCapRWO, AccessType: stdVolCap},
				VolumeId:          "vol_1",
				TargetPath:        targetTest,
				StagingTargetPath: sourceTest,
				VolumeContext:     directVolumeContext},
			expectedErr: testutil.TestError{
				DefaultError: status.Error(codes.FailedPrecondition, "volume needs ReadWriteOncePod access mode with Kata"),
			},
		},
		{
			desc: "[Error] Kata filesystem volume with legacy single-writer access mode",
			req: &csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCapSingleWriter, AccessType: stdVolCap},
				VolumeId:          "vol_1",
				TargetPath:        targetTest,
				StagingTargetPath: sourceTest,
				VolumeContext:     directVolumeContext},
			expectedErr: testutil.TestError{
				DefaultError: status.Error(codes.FailedPrecondition, "volume needs ReadWriteOncePod access mode with Kata"),
			},
		},
		{
			desc: "[Error] Kata filesystem volume with ReadOnlyMany access mode",
			req: &csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCapReadOnlyMany, AccessType: stdVolCap},
				VolumeId:          "vol_1",
				TargetPath:        targetTest,
				StagingTargetPath: sourceTest,
				VolumeContext:     directVolumeContext},
			expectedErr: testutil.TestError{
				DefaultError: status.Error(codes.InvalidArgument, "mountVolume is not supported for access mode: MULTI_NODE_READER_ONLY"),
			},
		},
		{
			desc: "[Error] Kata raw block volume with ReadWriteOnce access mode",
			req: &csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCapRWO, AccessType: stdVolCapBlock},
				VolumeId:          "vol_1",
				TargetPath:        directBlockTarget,
				StagingTargetPath: sourceTest,
				VolumeContext:     directVolumeContext},
			expectedErr: testutil.TestError{
				DefaultError: status.Error(codes.FailedPrecondition, "volume needs ReadWriteOncePod access mode with Kata"),
			},
		},
		{
			desc: "[Success] Pod lookup error falls back to standard mount",
			req: &csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: stdVolCap},
				VolumeId:          "vol_1",
				TargetPath:        lookupErrorTarget,
				StagingTargetPath: sourceTest,
				VolumeContext: map[string]string{
					podNameField:      "missing-pod",
					podNamespaceField: "default",
				},
				Readonly: true},
			expectedErr:   testutil.TestError{},
			expectedMount: true,
		},
		{
			desc: "[Error] Add direct volume",
			req: &csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: stdVolCap},
				VolumeId:          "vol_1",
				TargetPath:        directVolumeErrorTarget,
				StagingTargetPath: sourceTest,
				PublishContext:    publishContext,
				VolumeContext:     directFSGroupVolumeContext,
				Readonly:          true},
			expectedErr: testutil.TestError{
				DefaultError: status.Error(codes.Internal, "failed to add direct volume: fake add direct volume error"),
			},
		},
		{
			desc: "[Success] Valid request",
			req: &csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: stdVolCap},
				VolumeId:          "vol_1",
				TargetPath:        targetTest,
				StagingTargetPath: sourceTest,
				PublishContext:    publishContext,
				VolumeContext:     directVolumeContext,
				Readonly:          true},
			expectedErr: testutil.TestError{},
			expectedInfo: &directvolume.MountInfo{
				VolumeType: kataDirectVolumeType,
				Device:     "/dev/sdd",
				FsType:     defaultLinuxFsType,
				Metadata:   map[string]string{},
				Options:    []string{"discard"},
			},
		},
		{
			desc: "[Success] Kata filesystem volume with fsGroup",
			req: &csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: stdVolCap},
				VolumeId:          "vol_1",
				TargetPath:        directFSGroupTarget,
				StagingTargetPath: sourceTest,
				PublishContext:    publishContext,
				VolumeContext:     directFSGroupVolumeContext},
			expectedErr: testutil.TestError{},
			expectedInfo: &directvolume.MountInfo{
				VolumeType: kataDirectVolumeType,
				Device:     "/dev/sdd",
				FsType:     defaultLinuxFsType,
				Metadata: map[string]string{
					directvolume.FSGroupMetadataKey: "3000",
				},
				Options: []string{"discard"},
			},
			expectTarget: true,
		},
		{
			desc: "[Success] Kata raw block volume",
			req: &csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: stdVolCapBlock},
				VolumeId:          "vol_1",
				TargetPath:        directBlockTarget,
				StagingTargetPath: sourceTest,
				PublishContext:    publishContext,
				VolumeContext:     directFSGroupVolumeContext,
				Readonly:          true},
			expectedErr: testutil.TestError{},
			expectedInfo: &directvolume.MountInfo{
				VolumeType: kataDirectVolumeType,
				Device:     "/dev/sdd",
				FsType:     defaultLinuxFsType,
				Metadata:   map[string]string{},
			},
		},
	}

	// Setup
	fakeMounter, err := mounter.NewFakeSafeMounter()
	assert.NoError(t, err)
	d.setMounter(fakeMounter)
	runtimeClassName := "kata"
	fsGroup := int64(3000)
	d.(*fakeDriver).kubeClient = fake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "direct-volume-pod", Namespace: "default"},
			Spec:       corev1.PodSpec{RuntimeClassName: &runtimeClassName},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "direct-volume-fsgroup-pod", Namespace: "default"},
			Spec: corev1.PodSpec{
				RuntimeClassName: &runtimeClassName,
				SecurityContext:  &corev1.PodSecurityContext{FSGroup: &fsGroup},
			},
		},
		&nodev1.RuntimeClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:        runtimeClassName,
				Annotations: map[string]string{kataRuntimeClassAnnotationKey: kataRuntimeClassAnnotationValue},
			},
			Handler: "kata",
		},
	)
	var addedTarget string
	var addedMountInfo directvolume.MountInfo
	d.(*fakeDriver).kataDirectVolume = &kataStubDirectVolume{
		addMountInfo: func(target string, mountInfo directvolume.MountInfo) error {
			if target == directVolumeErrorTarget {
				return errors.New("fake add direct volume error")
			}
			addedTarget = target
			addedMountInfo = mountInfo
			return nil
		},
	}

	for _, test := range tests {
		addedTarget = ""
		addedMountInfo = directvolume.MountInfo{}
		if test.setup != nil {
			test.setup()
		}
		if !(test.skipOnWindows && runtime.GOOS == "windows") && !(test.skipOnDarwin && runtime.GOOS == "darwin") {
			var err error
			_, err = d.NodePublishVolume(context.Background(), test.req)
			if !testutil.AssertError(&test.expectedErr, err) && (err == nil || !strings.Contains(err.Error(), "invalid access mode")) {
				t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
			}
			if test.expectedInfo != nil {
				assert.Equal(t, test.req.TargetPath, addedTarget, test.desc)
				assert.Equal(t, *test.expectedInfo, addedMountInfo, test.desc)
			}
			if test.expectedMount {
				info, err := os.Stat(test.req.TargetPath)
				require.NoError(t, err, test.desc)
				assert.True(t, info.IsDir(), test.desc)
				assert.Empty(t, addedTarget, test.desc)
			}
			if test.expectTarget {
				info, err := os.Stat(test.req.TargetPath)
				require.NoError(t, err, test.desc)
				assert.True(t, info.IsDir(), test.desc)
			}
		}
		if test.cleanup != nil {
			test.cleanup()
		}
	}

	// Clean up
	err = os.RemoveAll(targetTest)
	assert.NoError(t, err)
	err = os.RemoveAll(standardTarget)
	assert.NoError(t, err)
	err = os.RemoveAll(lookupErrorTarget)
	assert.NoError(t, err)
	err = os.RemoveAll(directBlockTarget)
	assert.NoError(t, err)
	err = os.RemoveAll(directFSGroupTarget)
	assert.NoError(t, err)
}

func TestNodeUnpublishVolume(t *testing.T) {
	cntl := gomock.NewController(t)
	d, _ := NewFakeDriver(cntl)
	errorTarget, err := testutil.GetWorkDirPath("error_is_likely_target")
	assert.NoError(t, err)
	targetFile, err := testutil.GetWorkDirPath("abc.go")
	assert.NoError(t, err)
	standardTarget, err := testutil.GetWorkDirPath("standard_unpublish_target")
	assert.NoError(t, err)
	probeErrorTarget, err := testutil.GetWorkDirPath("direct_volume_probe_error_target")
	assert.NoError(t, err)
	directFSGroupTarget, err := testutil.GetWorkDirPath("direct_fsgroup_unpublish_target")
	assert.NoError(t, err)

	tests := []struct {
		setup          func()
		desc           string
		req            *csi.NodeUnpublishVolumeRequest
		skipOnWindows  bool
		skipOnDarwin   bool
		expectedErr    testutil.TestError
		expectedTarget string
		expectRemoved  bool
		cleanup        func()
	}{
		{
			desc: "Volume ID missing",
			req:  &csi.NodeUnpublishVolumeRequest{TargetPath: targetTest},
			expectedErr: testutil.TestError{
				DefaultError: status.Error(codes.InvalidArgument, "Volume ID missing in the request"),
			},
		},
		{
			desc: "Target missing",
			req:  &csi.NodeUnpublishVolumeRequest{VolumeId: "vol_1"},
			expectedErr: testutil.TestError{
				DefaultError: status.Error(codes.InvalidArgument, "Target path missing in request"),
			},
		},
		{
			desc: "[Error] Remove direct volume",
			req:  &csi.NodeUnpublishVolumeRequest{TargetPath: errorTarget, VolumeId: "vol_1"},
			expectedErr: testutil.TestError{
				DefaultError: status.Errorf(codes.Internal, "failed to remove direct volume %q: fake remove direct volume error", errorTarget),
			},
		},
		{
			desc:           "[Success] Valid request",
			req:            &csi.NodeUnpublishVolumeRequest{TargetPath: targetFile, VolumeId: "vol_1"},
			expectedErr:    testutil.TestError{},
			expectedTarget: targetFile,
		},
		{
			desc:           "[Success] Direct volume with fsGroup target",
			req:            &csi.NodeUnpublishVolumeRequest{TargetPath: directFSGroupTarget, VolumeId: "vol_1"},
			expectedErr:    testutil.TestError{},
			expectedTarget: directFSGroupTarget,
			expectRemoved:  true,
		},
		{
			desc:          "[Success] Standard mount",
			req:           &csi.NodeUnpublishVolumeRequest{TargetPath: standardTarget, VolumeId: "vol_1"},
			expectedErr:   testutil.TestError{},
			expectRemoved: true,
		},
		{
			desc:          "[Success] Direct volume probe error falls back to standard mount",
			req:           &csi.NodeUnpublishVolumeRequest{TargetPath: probeErrorTarget, VolumeId: "vol_1"},
			expectedErr:   testutil.TestError{},
			expectRemoved: true,
		},
	}

	// Setup
	_ = makeDir(standardTarget)
	_ = makeDir(probeErrorTarget)
	_ = makeDir(directFSGroupTarget)
	fakeMounter, err := mounter.NewFakeSafeMounter()
	assert.NoError(t, err)
	d.setMounter(fakeMounter)
	var removedTarget string
	d.(*fakeDriver).kataDirectVolume = &kataStubDirectVolume{
		isVolumeMounted: func(target string) (bool, error) {
			if target == errorTarget || target == targetFile || target == directFSGroupTarget {
				return true, nil
			}
			if target == probeErrorTarget {
				return false, errors.New("fake direct volume probe error")
			}
			return false, nil
		},
		remove: func(target string) error {
			if target == errorTarget {
				return errors.New("fake remove direct volume error")
			}
			removedTarget = target
			return nil
		},
	}

	for _, test := range tests {
		removedTarget = ""
		if test.setup != nil {
			test.setup()
		}
		if !(test.skipOnWindows && runtime.GOOS == "windows") &&
			!(test.skipOnDarwin && runtime.GOOS == "darwin") {
			_, err := d.NodeUnpublishVolume(context.Background(), test.req)
			if !testutil.AssertError(&test.expectedErr, err) {
				t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
			}
			assert.Equal(t, test.expectedTarget, removedTarget, test.desc)
			if test.expectRemoved {
				_, err := os.Stat(test.req.TargetPath)
				assert.ErrorIs(t, err, os.ErrNotExist, test.desc)
			}
		}
		if test.cleanup != nil {
			test.cleanup()
		}
	}
	_ = os.RemoveAll(standardTarget)
	_ = os.RemoveAll(probeErrorTarget)
	_ = os.RemoveAll(directFSGroupTarget)
}

func TestNodeExpandVolume(t *testing.T) {
	cntl := gomock.NewController(t)
	defer cntl.Finish()
	d, _ := NewFakeDriver(cntl)
	fakeMounter, err := mounter.NewFakeSafeMounter()
	assert.NoError(t, err)
	d.setMounter(fakeMounter)
	blockVolumePath := "/tmp/block-volume-path"
	_ = makeDir(blockVolumePath)
	_ = makeDir(targetTest)
	notFoundErr := errors.New("exit status 1")

	stdCapacityRange = &csi.CapacityRange{
		RequiredBytes: volumehelper.GiBToBytes(15),
		LimitBytes:    volumehelper.GiBToBytes(10),
	}

	invalidPathErr := testutil.TestError{
		DefaultError: status.Error(codes.NotFound, "failed to determine device path for volumePath [./test]: path \"./test\" does not exist"),
	}

	devicePathErr := testutil.TestError{
		DefaultError: status.Errorf(codes.NotFound, "could not determine device path(%s), error: %v", targetTest, notFoundErr),
		WindowsError: status.Errorf(codes.NotFound, "error reading link for mount D:\\a\\azuredisk-csi-driver\\azuredisk-csi-driver\\pkg\\azuredisk\\target_test. target  err: readlink D:\\a\\azuredisk-csi-driver\\azuredisk-csi-driver\\pkg\\azuredisk\\target_test: The file or directory is not a reparse point."),
	}
	blockSizeErr := testutil.TestError{
		DefaultError: status.Error(codes.Unavailable, "NodeExpandVolume: block device size did not match requested size: rpc error: code = Internal desc = block volume at path test size check failed: current 0 GiB < requested 15 GiB"),
		WindowsError: status.Errorf(codes.NotFound, "error reading link for mount D:\\a\\azuredisk-csi-driver\\azuredisk-csi-driver\\pkg\\azuredisk\\target_test. target  err: readlink D:\\a\\azuredisk-csi-driver\\azuredisk-csi-driver\\pkg\\azuredisk\\target_test: The file or directory is not a reparse point."),
	}
	resizeErr := testutil.TestError{
		DefaultError: status.Errorf(codes.Internal, "could not resize volume \"test\" (\"test\"):  resize of device test failed: %v. resize2fs output: ", notFoundErr),
		WindowsError: status.Errorf(codes.NotFound, "error reading link for mount D:\\a\\azuredisk-csi-driver\\azuredisk-csi-driver\\pkg\\azuredisk\\target_test. target  err: readlink D:\\a\\azuredisk-csi-driver\\azuredisk-csi-driver\\pkg\\azuredisk\\target_test: The file or directory is not a reparse point."),
	}
	sizeTooSmallErr := testutil.TestError{
		DefaultError: status.Error(codes.Internal, "NodeExpandVolume: block device size did not match requested size after filesystem resize: rpc error: code = Internal desc = block volume at path test size check failed: current 8 GiB < requested 15 GiB"),
		WindowsError: status.Errorf(codes.NotFound, "error reading link for mount D:\\a\\azuredisk-csi-driver\\azuredisk-csi-driver\\pkg\\azuredisk\\target_test. target  err: readlink D:\\a\\azuredisk-csi-driver\\azuredisk-csi-driver\\pkg\\azuredisk\\target_test: The file or directory is not a reparse point."),
	}

	notFoundErrAction := func() ([]byte, []byte, error) {
		return []byte{}, []byte{}, notFoundErr
	}
	findmntAction := func() ([]byte, []byte, error) {
		return []byte("test"), []byte{}, nil
	}
	blkidAction := func() ([]byte, []byte, error) {
		return []byte("DEVICE=test\nTYPE=ext4"), []byte{}, nil
	}
	resize2fsFailedAction := func() ([]byte, []byte, error) {
		return []byte{}, []byte{}, notFoundErr
	}
	resize2fsAction := func() ([]byte, []byte, error) {
		return []byte{}, []byte{}, nil
	}
	blockdevSizeTooSmallAction := func() ([]byte, []byte, error) {
		return []byte(fmt.Sprintf("%d", stdCapacityRange.RequiredBytes/2)), []byte{}, nil
	}
	blockdevAction := func() ([]byte, []byte, error) {
		return []byte(fmt.Sprintf("%d", stdCapacityRange.RequiredBytes)), []byte{}, nil
	}
	// Action that returns 0 bytes (invalid block size)
	blockdevZeroAction := func() ([]byte, []byte, error) {
		return []byte("0"), []byte{}, nil
	}

	tests := []struct {
		desc          string
		req           *csi.NodeExpandVolumeRequest
		expectedErr   testutil.TestError
		skipOnDarwin  bool
		skipOnWindows bool
		outputScripts []testingexec.FakeAction
	}{
		{
			desc: "Volume ID missing",
			req:  &csi.NodeExpandVolumeRequest{},
			expectedErr: testutil.TestError{
				DefaultError: status.Error(codes.InvalidArgument, "Volume ID not provided"),
			},
		},
		{
			desc: "could not find path",
			req: &csi.NodeExpandVolumeRequest{
				CapacityRange: stdCapacityRange,
				VolumePath:    "./test",
				VolumeId:      "test",
			},
			expectedErr: invalidPathErr,
		},
		{
			desc: "volume path not provide",
			req: &csi.NodeExpandVolumeRequest{
				CapacityRange:     stdCapacityRange,
				StagingTargetPath: "test",
				VolumeId:          "test",
			},
			expectedErr: testutil.TestError{
				DefaultError: status.Error(codes.InvalidArgument, "volume path must be provided"),
			},
		},
		{
			desc: "Invalid device path",
			req: &csi.NodeExpandVolumeRequest{
				CapacityRange:     stdCapacityRange,
				VolumePath:        targetTest,
				VolumeId:          "test",
				StagingTargetPath: "",
			},
			expectedErr:   devicePathErr,
			outputScripts: []testingexec.FakeAction{notFoundErrAction},
		},
		{
			desc: "No block size at path",
			req: &csi.NodeExpandVolumeRequest{
				CapacityRange:     stdCapacityRange,
				VolumePath:        targetTest,
				VolumeId:          "test",
				StagingTargetPath: "test",
			},
			expectedErr:   blockSizeErr,
			skipOnDarwin:  true, // ResizeFs not supported on Darwin
			outputScripts: []testingexec.FakeAction{findmntAction, blockdevZeroAction},
		},
		{
			desc: "Resize failure",
			req: &csi.NodeExpandVolumeRequest{
				CapacityRange:     stdCapacityRange,
				VolumePath:        targetTest,
				VolumeId:          "test",
				StagingTargetPath: "test",
			},
			expectedErr:  resizeErr,
			skipOnDarwin: true, // ResizeFs not supported on Darwin
			// First blockdev call succeeds (pre-resize validation), blkid checks filesystem, resize2fs fails, second blockdev for post-resize validation
			outputScripts: []testingexec.FakeAction{findmntAction, blockdevAction, blkidAction, resize2fsFailedAction, blockdevSizeTooSmallAction},
		},
		{
			desc: "Resize too small failure",
			req: &csi.NodeExpandVolumeRequest{
				CapacityRange:     stdCapacityRange,
				VolumePath:        targetTest,
				VolumeId:          "test",
				StagingTargetPath: "test",
			},
			expectedErr:  sizeTooSmallErr,
			skipOnDarwin: true, // ResizeFs not supported on Darwin
			// First blockdev call succeeds (pre-resize validation), resize2fs succeeds, second blockdev shows size too small
			outputScripts: []testingexec.FakeAction{findmntAction, blockdevAction, blkidAction, resize2fsAction, blockdevSizeTooSmallAction},
		},
		{
			desc: "Successfully expanded",
			req: &csi.NodeExpandVolumeRequest{
				CapacityRange:     stdCapacityRange,
				VolumePath:        targetTest,
				VolumeId:          "test",
				StagingTargetPath: "test",
			},
			skipOnWindows: true,
			skipOnDarwin:  true, // ResizeFs not supported on Darwin
			// First blockdev call (pre-resize validation), resize2fs succeeds, second blockdev call (post-resize validation)
			outputScripts: []testingexec.FakeAction{findmntAction, blockdevAction, blkidAction, resize2fsAction, blockdevAction},
		},
		{
			desc: "Block volume expansion",
			req: &csi.NodeExpandVolumeRequest{
				CapacityRange:     stdCapacityRange,
				VolumePath:        blockVolumePath,
				VolumeId:          "test",
				StagingTargetPath: "test",
			},
		},
		{
			desc: "Volume capability access type mismatch",
			req: &csi.NodeExpandVolumeRequest{
				CapacityRange:     stdCapacityRange,
				VolumePath:        targetTest,
				VolumeId:          "test",
				StagingTargetPath: "test",
				VolumeCapability: &csi.VolumeCapability{
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
					AccessType: &csi.VolumeCapability_Block{
						Block: &csi.VolumeCapability_BlockVolume{},
					},
				},
			},
		},
	}

	d.getHostUtil().(*azureutils.FakeHostUtil).SetPathIsDeviceResult(blockVolumePath, true, nil)

	for _, test := range tests {
		if (test.skipOnDarwin && runtime.GOOS == "darwin") || (test.skipOnWindows && runtime.GOOS == "windows") {
			continue
		}
		if runtime.GOOS != "windows" {
			d.setNextCommandOutputScripts(test.outputScripts...)
		}

		_, err := d.NodeExpandVolume(context.Background(), test.req)
		if !testutil.AssertError(&test.expectedErr, err) {
			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
		}
	}
	err = os.RemoveAll(targetTest)
	assert.NoError(t, err)
	err = os.RemoveAll(blockVolumePath)
	assert.NoError(t, err)
}

func TestGetBlockSizeBytes(t *testing.T) {
	cntl := gomock.NewController(t)
	defer cntl.Finish()
	if runtime.GOOS == "windows" {
		t.Skip("Skipping test on Windows")
	}
	d, _ := NewFakeDriver(cntl)
	testTarget, err := testutil.GetWorkDirPath("test")
	assert.NoError(t, err)

	notFoundErr := "exit status 1"
	// exception in darwin
	if runtime.GOOS == "darwin" {
		notFoundErr = "executable file not found in $PATH"
	} else if runtime.GOOS == "windows" {
		notFoundErr = "executable file not found in %PATH%"
	}

	tests := []struct {
		desc        string
		req         string
		expectedErr testutil.TestError
	}{
		{
			desc: "no exist path",
			req:  "testpath",
			expectedErr: testutil.TestError{
				DefaultError: fmt.Errorf("error when getting size of block volume at path testpath: output: , err: %s", notFoundErr),
				WindowsError: fmt.Errorf("error when getting size of block volume at path testpath: output: , err: %s", notFoundErr),
			},
		},
		{
			desc: "invalid path",
			req:  testTarget,
			expectedErr: testutil.TestError{
				DefaultError: fmt.Errorf("error when getting size of block volume at path %s: "+
					"output: , err: %s", testTarget, notFoundErr),
				WindowsError: fmt.Errorf("error when getting size of block volume at path %s: "+
					"output: , err: %s", testTarget, notFoundErr),
			},
		},
	}
	for _, test := range tests {
		_, err := getBlockSizeBytes(test.req, d.getMounter())
		if !testutil.AssertError(&test.expectedErr, err) {
			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
		}
	}
	//Setup
	_ = makeDir(testTarget)

	err = os.RemoveAll(testTarget)
	assert.NoError(t, err)
}

func TestEnsureBlockTargetFile(t *testing.T) {
	cntl := gomock.NewController(t)
	defer cntl.Finish()
	// sip this test because `util/mount` not supported
	// on darwin
	if runtime.GOOS == "darwin" {
		t.Skip("Skipping tests on darwin")
	}
	testTarget, err := testutil.GetWorkDirPath("test")
	assert.NoError(t, err)
	testPath, err := testutil.GetWorkDirPath(fmt.Sprintf("test%ctest", os.PathSeparator))
	assert.NoError(t, err)
	d, err := NewFakeDriver(cntl)
	assert.NoError(t, err)

	tests := []struct {
		desc        string
		req         string
		expectedErr testutil.TestError
	}{
		{
			desc:        "valid test",
			req:         testTarget,
			expectedErr: testutil.TestError{},
		},
		{
			desc: "test if file exists",
			req:  testPath,
			expectedErr: testutil.TestError{
				DefaultError: status.Error(codes.Internal, fmt.Sprintf("could not mount target \"%s\": mkdir %s: not a directory", testTarget, testTarget)),
				WindowsError: status.Error(codes.Internal, fmt.Sprintf("could not remove mount target %#v: remove %s: The system cannot find the path specified.", testPath, testPath)),
			},
		},
	}
	for _, test := range tests {
		err := d.ensureBlockTargetFile(test.req)
		if !testutil.AssertError(&test.expectedErr, err) {
			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
		}
	}
	err = os.RemoveAll(testTarget)
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
	err = makeDir("./azuredisk.go")
	var e *os.PathError
	if !errors.As(err, &e) {
		t.Errorf("Unexpected Error: %v", err)
	}

	// Remove the directory created
	err = os.RemoveAll(targetTest)
	assert.NoError(t, err)
}

func TestGetDevicePathWithLUN(t *testing.T) {
	cntl := gomock.NewController(t)
	defer cntl.Finish()
	d, _ := NewFakeDriver(cntl)
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

func TestGetDevicePathWithMountPath(t *testing.T) {
	cntl := gomock.NewController(t)
	defer cntl.Finish()
	d, _ := NewFakeDriver(cntl)
	err := "exit status 1"

	if runtime.GOOS == "darwin" {
		err = "executable file not found in $PATH"
	}

	tests := []struct {
		desc          string
		req           string
		skipOnDarwin  bool
		skipOnWindows bool
		expectedErr   error
	}{
		{
			desc:        "Invalid device path",
			req:         "unit-test",
			expectedErr: fmt.Errorf("could not determine device path(unit-test), error: %v", err),
			// Skip negative tests on Windows because error messages from csi-proxy are not easily predictable.
			skipOnWindows: true,
		},
		{
			desc:          "[Success] Valid device path",
			req:           "/sys",
			skipOnDarwin:  true,
			skipOnWindows: true,
			expectedErr:   nil,
		},
	}

	for _, test := range tests {
		if !(test.skipOnDarwin && runtime.GOOS == "darwin") && !(test.skipOnWindows && runtime.GOOS == "windows") {
			_, err := getDevicePathWithMountPath(test.req, d.getMounter())
			if !reflect.DeepEqual(err, test.expectedErr) {
				t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
			}
		}
	}
}

func TestNodePublishVolumeIdempotent(t *testing.T) {
	cntl := gomock.NewController(t)
	defer cntl.Finish()
	stdVolCap := &csi.VolumeCapability_Mount{
		Mount: &csi.VolumeCapability_MountVolume{
			FsType: defaultLinuxFsType,
		},
	}
	d, _ := NewFakeDriver(cntl)
	fakeMounter, err := mounter.NewFakeSafeMounter()
	assert.NoError(t, err)
	d.setMounter(fakeMounter)
	runtimeClassName := "kata"
	d.(*fakeDriver).kubeClient = fake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "direct-volume-pod", Namespace: "default"},
			Spec:       corev1.PodSpec{RuntimeClassName: &runtimeClassName},
		},
		&nodev1.RuntimeClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:        runtimeClassName,
				Annotations: map[string]string{kataRuntimeClassAnnotationKey: kataRuntimeClassAnnotationValue},
			},
			Handler: "kata",
		},
	)

	var calls int
	d.(*fakeDriver).kataDirectVolume = &kataStubDirectVolume{
		addMountInfo: func(target string, mountInfo directvolume.MountInfo) error {
			assert.Equal(t, targetTest, target)
			assert.Equal(t, directvolume.MountInfo{
				VolumeType: kataDirectVolumeType,
				Device:     "/dev/sdd",
				FsType:     defaultLinuxFsType,
			}, mountInfo)
			calls++
			return nil
		},
	}

	volumeCap := csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER}
	req := csi.NodePublishVolumeRequest{VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap, AccessType: stdVolCap},
		VolumeId:          "vol_1",
		TargetPath:        targetTest,
		StagingTargetPath: sourceTest,
		PublishContext: map[string]string{
			consts.LUN: "/dev/disk/azure/scsi1/lun1",
		},
		VolumeContext: map[string]string{
			podNameField:      "direct-volume-pod",
			podNamespaceField: "default",
		},
		Readonly: true}

	_, err = d.NodePublishVolume(context.Background(), &req)
	assert.NoError(t, err)
	_, err = d.NodePublishVolume(context.Background(), &req)
	assert.NoError(t, err)
	assert.Equal(t, 2, calls)
}

func TestValidateBlockDeviceSize(t *testing.T) {
	cntl := gomock.NewController(t)
	defer cntl.Finish()

	d, _ := NewFakeDriver(cntl)
	fakeMounter, err := mounter.NewFakeSafeMounter()
	assert.NoError(t, err)
	d.setMounter(fakeMounter)

	notFoundErr := errors.New("exit status 1")
	blockdevAction := func() ([]byte, []byte, error) {
		return []byte(fmt.Sprintf("%d", volumehelper.GiBToBytes(20))), []byte{}, nil
	}
	blockdevSmallAction := func() ([]byte, []byte, error) {
		return []byte(fmt.Sprintf("%d", volumehelper.GiBToBytes(5))), []byte{}, nil
	}
	blockdevErrorAction := func() ([]byte, []byte, error) {
		return []byte{}, []byte{}, notFoundErr
	}

	tests := []struct {
		desc          string
		devicePath    string
		requestGiB    int64
		outputScript  testingexec.FakeAction
		expectedErr   error
		expectedBytes int64
		skipOnWindows bool
	}{
		{
			desc:          "Error getting block size",
			devicePath:    "/dev/sda",
			requestGiB:    10,
			outputScript:  blockdevErrorAction,
			expectedErr:   status.Error(codes.Internal, fmt.Sprintf("could not get size of block volume at path %s: %v", "/dev/sda", fmt.Errorf("error when getting size of block volume at path /dev/sda: output: , err: exit status 1"))),
			skipOnWindows: true,
		},
		{
			desc:          "Block size too small",
			devicePath:    "/dev/sdb",
			requestGiB:    10,
			outputScript:  blockdevSmallAction,
			expectedErr:   status.Error(codes.Internal, fmt.Sprintf("block volume at path %s size check failed: current %d GiB < requested %d GiB", "/dev/sdb", 5, 10)),
			skipOnWindows: true,
		},
		{
			desc:          "Successful validation",
			devicePath:    "/dev/sdc",
			requestGiB:    10,
			outputScript:  blockdevAction,
			expectedErr:   nil,
			expectedBytes: volumehelper.GiBToBytes(20),
			skipOnWindows: true,
		},
		{
			desc:          "Exact size match",
			devicePath:    "/dev/sdd",
			requestGiB:    20,
			outputScript:  blockdevAction,
			expectedErr:   nil,
			expectedBytes: volumehelper.GiBToBytes(20),
			skipOnWindows: true,
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			if test.skipOnWindows && runtime.GOOS == "windows" {
				t.Skip("Skipping test on Windows")
			}

			d.setNextCommandOutputScripts(test.outputScript)

			actualBytes, err := d.validateBlockDeviceSize(test.devicePath, test.requestGiB)

			if test.expectedErr == nil {
				assert.NoError(t, err)
				assert.Equal(t, test.expectedBytes, actualBytes)
			} else {
				assert.EqualError(t, err, test.expectedErr.Error())
				assert.Equal(t, int64(0), actualBytes)
			}
		})
	}
}
