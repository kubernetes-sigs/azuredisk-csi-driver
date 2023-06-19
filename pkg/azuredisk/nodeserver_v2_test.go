//go:build azurediskv2
// +build azurediskv2

/*
Copyright 2017 The Kubernetes Authors.

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
	"runtime"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk/mockprovisioner"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/mounter"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/testutil"
)

func TestNodeStageVolumeMountRecovery(t *testing.T) {
	if mounter.IsFakeUsingCSIProxy() {
		t.Skip("Skipping test because CSI Proxy is used.")
	}
	d, err := newFakeDriverV2(t, newFakeDriverConfig())
	assert.NoError(t, err)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	d.crdProvisioner = mockprovisioner.NewMockCrdProvisioner(ctrl)

	publishContext := testAzVolumeAttachment.DeepCopy()
	publishContext.Status = azdiskv1beta2.AzVolumeAttachmentStatus{
		Detail: &azdiskv1beta2.AzVolumeAttachmentStatusDetail{
			PublishContext: map[string]string{
				consts.LUN: "/dev/disk/azure/scsi1/lun1",
			},
			Role: azdiskv1beta2.PrimaryRole,
		},
		State: azdiskv1beta2.Attached,
	}

	attachError := &azdiskv1beta2.AzError{
		Code:    azdiskv1beta2.AzErrorCodeInternal,
		Message: "test error it is",
	}

	stdVolCap := &csi.VolumeCapability_Mount{
		Mount: &csi.VolumeCapability_MountVolume{
			FsType: defaultLinuxFsType,
		},
	}
	volumeContext := map[string]string{
		consts.FsTypeField: defaultLinuxFsType,
	}

	volumeCap := csi.VolumeCapability_AccessMode{Mode: 2}

	blkidAction := func() ([]byte, []byte, error) {
		return []byte("DEVICE=/dev/sdd\nTYPE=ext4"), []byte{}, nil
	}
	fsckAction := func() ([]byte, []byte, error) {
		return []byte{}, []byte{}, nil
	}
	blockSizeAction := func() ([]byte, []byte, error) {
		return []byte(fmt.Sprintf("%d", stdCapacityRange.RequiredBytes)), []byte{}, nil
	}

	tests := []struct {
		desc          string
		setupFunc     func(*testing.T, *fakeDriverV2)
		req           csi.NodeStageVolumeRequest
		expectedErr   error
		skipOnDarwin  bool
		skipOnWindows bool
	}{
		{
			desc: "Should return error if recovery detachment is still in process",
			setupFunc: func(t *testing.T, d *fakeDriverV2) {
				d.deviceChecker.entry = &deviceCheckerEntry{
					diskURI:     "vol_1",
					detachState: detachInProcess,
				}
				d.crdProvisioner.(*mockprovisioner.MockCrdProvisioner).EXPECT().GetAzVolumeAttachment(gomock.Any(), gomock.Any(), gomock.Any()).Return(publishContext, err)
			},
			req: csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest,
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap,
					AccessType: stdVolCap},
				VolumeContext: volumeContext,
			},
			expectedErr: status.Errorf(codes.Internal, "recovery for volume (%s) is still in process", "vol_1"),
		},
		{
			desc: "Should return error if recovery detachment is complete but the new AzVolumeAttachment attachment failed",
			setupFunc: func(t *testing.T, d *fakeDriverV2) {
				d.deviceChecker.entry = &deviceCheckerEntry{
					diskURI:     "vol_1",
					detachState: detachCompleted,
				}
				d.crdProvisioner.(*mockprovisioner.MockCrdProvisioner).EXPECT().GetAzVolumeAttachment(gomock.Any(), gomock.Any(), gomock.Any()).Return(&testAzVolumeAttachment, nil)
				d.crdProvisioner.(*mockprovisioner.MockCrdProvisioner).EXPECT().WaitForAttach(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, util.ErrorFromAzError(attachError))
			},
			req: csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest,
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap,
					AccessType: stdVolCap},
				VolumeContext: volumeContext,
			},
			expectedErr: status.Errorf(codes.Internal, "failed to wait for volume (%s) to be attached to node (%s): %v", "vol_1", d.NodeID, util.ErrorFromAzError(attachError)),
		},
		{
			desc: "Should return success if recovery detachment is complete and the new AzVolumeAttachment attachment succeeded",
			setupFunc: func(t *testing.T, d *fakeDriverV2) {
				d.deviceChecker.entry = &deviceCheckerEntry{
					diskURI:     "vol_1",
					detachState: detachCompleted,
				}
				d.crdProvisioner.(*mockprovisioner.MockCrdProvisioner).EXPECT().GetAzVolumeAttachment(gomock.Any(), gomock.Any(), gomock.Any()).Return(&testAzVolumeAttachment, nil)
				d.setNextCommandOutputScripts(blkidAction, fsckAction, blockSizeAction, blkidAction, blockSizeAction, blkidAction)
				d.crdProvisioner.(*mockprovisioner.MockCrdProvisioner).EXPECT().WaitForAttach(gomock.Any(), gomock.Any(), gomock.Any()).Return(publishContext, nil)
			},
			req: csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest,
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap,
					AccessType: stdVolCap},
				VolumeContext: volumeContext,
			},
			expectedErr: nil,
		},
		{
			desc:         "Should succeed if recovery detachment is complete and AzVolumeAttachment CRI is in attached state",
			skipOnDarwin: true,
			setupFunc: func(t *testing.T, d *fakeDriverV2) {
				d.deviceChecker.entry = &deviceCheckerEntry{
					diskURI:     "vol_1",
					detachState: detachCompleted,
				}
				d.crdProvisioner.(*mockprovisioner.MockCrdProvisioner).EXPECT().GetAzVolumeAttachment(gomock.Any(), gomock.Any(), gomock.Any()).Return(publishContext, nil)
				d.setNextCommandOutputScripts(blkidAction, fsckAction, blockSizeAction, blkidAction, blockSizeAction, blkidAction)
			},
			req: csi.NodeStageVolumeRequest{VolumeId: "vol_1", StagingTargetPath: sourceTest,
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap,
					AccessType: stdVolCap},
				VolumeContext: volumeContext,
			},
			expectedErr: nil,
		},
	}

	// Setup
	_ = makeDir(sourceTest)
	_ = makeDir(targetTest)
	fakeMounter, err := mounter.NewFakeSafeMounter()
	assert.NoError(t, err)
	d.setMounter(fakeMounter)

	for _, test := range tests {
		if !(test.skipOnDarwin && runtime.GOOS == "darwin") && !(test.skipOnWindows && runtime.GOOS == "windows") {
			defer func() { d.deviceChecker.entry = nil }()
			if test.setupFunc != nil {
				test.setupFunc(t, d)
			}
			_, err := d.NodeStageVolume(context.Background(), &test.req)
			if test.desc == "Failed volume mount" {
				assert.Error(t, err)
			} else if !testutil.IsErrorEquivalent(err, test.expectedErr) {
				t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
			}
		}
	}

	// Clean up
	err = os.RemoveAll(sourceTest)
	assert.NoError(t, err)
	err = os.RemoveAll(targetTest)
	assert.NoError(t, err)
}

func TestRecoverMount(t *testing.T) {
	d, _ := newFakeDriverV2(t, newFakeDriverConfig())
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	d.crdProvisioner = mockprovisioner.NewMockCrdProvisioner(ctrl)

	tests := []struct {
		desc       string
		diskURI    string
		setupFunc  func()
		verifyFunc func(string)
	}{
		{
			desc:    "should recover if no other recovery is currently in process",
			diskURI: "vol-1",
			setupFunc: func() {
				d.crdProvisioner.(*mockprovisioner.MockCrdProvisioner).EXPECT().UnpublishVolume(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
				d.crdProvisioner.(*mockprovisioner.MockCrdProvisioner).EXPECT().WaitForDetach(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			},
			verifyFunc: func(diskURI string) {
				assert.NotNil(t, d.deviceChecker.entry)
				assert.Equal(t, d.deviceChecker.entry.diskURI, diskURI)
				assert.Equal(t, d.deviceChecker.entry.detachState, detachCompleted)
			},
		},
		{
			desc:    "should skip recovery if there already is recovery in process for different volume",
			diskURI: "vol-1",
			setupFunc: func() {
				d.deviceChecker.entry = &deviceCheckerEntry{
					diskURI:     "vol_2",
					detachState: detachInProcess,
				}
			},
			verifyFunc: func(diskURI string) {
				assert.NotNil(t, d.deviceChecker.entry)
				assert.NotEqual(t, d.deviceChecker.entry.diskURI, diskURI)
			},
		},
	}
	for _, test := range tests {
		tt := test
		t.Run(tt.desc, func(t *testing.T) {
			defer func() { d.deviceChecker.entry = nil }()
			if tt.setupFunc != nil {
				tt.setupFunc()
			}
			d.recoverMount(tt.diskURI)
			if tt.verifyFunc != nil {
				tt.verifyFunc(tt.diskURI)
			}
		})
	}
}
