/*
Copyright 2021 The Kubernetes Authors.

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

package controller

import (
	"context"
	"sync"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	fakev1 "k8s.io/client-go/kubernetes/fake"
	diskv1alpha2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha2"
	diskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller/mockattachmentprovisioner"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller/mockclient"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func NewTestAttachDetachController(controller *gomock.Controller, namespace string, objects ...runtime.Object) *ReconcileAttachDetach {
	azDiskObjs, kubeObjs := splitObjects(objects...)
	controllerSharedState := initState(objects...)

	return &ReconcileAttachDetach{
		client:                mockclient.NewMockClient(controller),
		azVolumeClient:        diskfakes.NewSimpleClientset(azDiskObjs...),
		kubeClient:            fakev1.NewSimpleClientset(kubeObjs...),
		cloudDiskAttacher:     mockattachmentprovisioner.NewMockAttachmentProvisioner(controller),
		stateLock:             &sync.Map{},
		retryInfo:             newRetryInfo(),
		controllerSharedState: controllerSharedState,
	}
}

func mockClientsAndAttachmentProvisioner(controller *ReconcileAttachDetach) {
	mockClients(controller.client.(*mockclient.MockClient), controller.azVolumeClient, controller.kubeClient)

	controller.cloudDiskAttacher.(*mockattachmentprovisioner.MockAttachmentProvisioner).EXPECT().
		PublishVolume(gomock.Any(), testManagedDiskURI0, gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, volumeID, nodeID string, volumeContext map[string]string) (map[string]string, error) {
			return volumeContext, nil
		}).
		MaxTimes(1)
	controller.cloudDiskAttacher.(*mockattachmentprovisioner.MockAttachmentProvisioner).EXPECT().
		UnpublishVolume(gomock.Any(), testManagedDiskURI0, gomock.Any()).
		DoAndReturn(func(ctx context.Context, volumeID, nodeID string) error {
			return nil
		}).
		MaxTimes(1)
}

func TestAttachDetachReconcile(t *testing.T) {
	tests := []struct {
		description string
		request     reconcile.Request
		setupFunc   func(*testing.T, *gomock.Controller) *ReconcileAttachDetach
		verifyFunc  func(*testing.T, *ReconcileAttachDetach, reconcile.Result, error)
	}{
		{
			description: "[Success] Should attach volume when new primary AzVolumeAttachment is created.",
			request:     testPrimaryAzVolumeAttachment0Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAttachDetach {
				newAttachment := testPrimaryAzVolumeAttachment0.DeepCopy()
				newAttachment.Status.State = diskv1alpha2.AttachmentPending

				controller := NewTestAttachDetachController(
					mockCtl,
					testNamespace,
					&testAzVolume0,
					newAttachment)

				mockClientsAndAttachmentProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAttachDetach, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				conditionFunc := func() (bool, error) {
					azVolumeAttachment, localError := controller.azVolumeClient.DiskV1alpha2().AzVolumeAttachments(testPrimaryAzVolumeAttachment0.Namespace).Get(context.TODO(), testPrimaryAzVolumeAttachment0.Name, metav1.GetOptions{})
					if localError != nil {
						return false, nil
					}
					return azVolumeAttachment.Status.State == diskv1alpha2.Attached, nil
				}

				conditionError := wait.PollImmediate(verifyCRIInterval, verifyCRITimeout, conditionFunc)
				require.NoError(t, conditionError)
			},
		},
		{
			description: "[Success] Should detach volume when new primary AzVolumeAttachment is deleted.",
			request:     testPrimaryAzVolumeAttachment0Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAttachDetach {
				newAttachment := testPrimaryAzVolumeAttachment0.DeepCopy()
				newAttachment.Status.State = diskv1alpha2.Attached
				newAttachment.ObjectMeta.Annotations = map[string]string{consts.VolumeDetachRequestAnnotation: "crdProvisioner"}
				now := metav1.Time{Time: metav1.Now().Add(-1000)}
				newAttachment.DeletionTimestamp = &now

				controller := NewTestAttachDetachController(
					mockCtl,
					testNamespace,
					&testAzVolume0,
					newAttachment)

				mockClientsAndAttachmentProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAttachDetach, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				conditionFunc := func() (bool, error) {
					azVolumeAttachment, localError := controller.azVolumeClient.DiskV1alpha2().AzVolumeAttachments(testPrimaryAzVolumeAttachment0.Namespace).Get(context.TODO(), testPrimaryAzVolumeAttachment0.Name, metav1.GetOptions{})
					if localError != nil {
						return false, nil
					}
					return azVolumeAttachment.Status.State == diskv1alpha2.Detached, nil
				}

				conditionError := wait.PollImmediate(verifyCRIInterval, verifyCRITimeout, conditionFunc)
				require.NoError(t, conditionError)
			},
		},
		{
			description: "[Success] Should promote AzVolumeAttachment upon request.",
			request:     testReplicaAzVolumeAttachmentRequest,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAttachDetach {
				newAttachment := testReplicaAzVolumeAttachment.DeepCopy()
				newAttachment.Status.Detail = &diskv1alpha2.AzVolumeAttachmentStatusDetail{
					PublishContext: map[string]string{},
					Role:           diskv1alpha2.ReplicaRole,
				}
				newAttachment.Spec.RequestedRole = diskv1alpha2.PrimaryRole
				newAttachment.Status.State = diskv1alpha2.Attached

				controller := NewTestAttachDetachController(
					mockCtl,
					testNamespace,
					&testAzVolume0,
					newAttachment)

				mockClientsAndAttachmentProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAttachDetach, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				azVolumeAttachment, localError := controller.azVolumeClient.DiskV1alpha2().AzVolumeAttachments(testReplicaAzVolumeAttachment.Namespace).Get(context.TODO(), testReplicaAzVolumeAttachment.Name, metav1.GetOptions{})
				require.NoError(t, localError)
				require.NotNil(t, azVolumeAttachment)
				require.NotNil(t, azVolumeAttachment.Status.Detail)
				require.Equal(t, azVolumeAttachment.Status.Detail.Role, diskv1alpha2.PrimaryRole)

				// check role label
				require.Contains(t, azVolumeAttachment.Labels, consts.RoleLabel)
				require.Equal(t, string(diskv1alpha2.PrimaryRole), azVolumeAttachment.Labels[consts.RoleLabel])
			},
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			mockCtl := gomock.NewController(t)
			defer mockCtl.Finish()
			controller := tt.setupFunc(t, mockCtl)
			result, err := controller.Reconcile(context.TODO(), tt.request)
			tt.verifyFunc(t, controller, result, err)
		})
	}
}

func TestAttachDetachRecover(t *testing.T) {
	tests := []struct {
		description string
		setupFunc   func(*testing.T, *gomock.Controller) *ReconcileAttachDetach
		verifyFunc  func(*testing.T, *ReconcileAttachDetach, error)
	}{
		{
			description: "[Success] Should create AzVolumeAttachment instances for VolumeAttachment of PV using Azure Disk CSI Driver.",
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAttachDetach {

				controller := NewTestAttachDetachController(
					mockCtl,
					testNamespace,
					&testVolumeAttachment,
					&testPersistentVolume0)

				mockClientsAndAttachmentProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAttachDetach, err error) {
				require.NoError(t, err)

				azVolumeAttachments, localErr := controller.azVolumeClient.DiskV1alpha2().AzVolumeAttachments(testNamespace).List(context.TODO(), metav1.ListOptions{})
				require.NoError(t, localErr)
				require.Len(t, azVolumeAttachments.Items, 1)
			},
		},
		{
			description: "[Success] Should update AzVolumeAttachment CRIs to right state",
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAttachDetach {
				newAzVolumeAttachment0 := testPrimaryAzVolumeAttachment0.DeepCopy()
				newAzVolumeAttachment0.Status.State = diskv1alpha2.Attaching

				newAzVolumeAttachment1 := testPrimaryAzVolumeAttachment1.DeepCopy()
				newAzVolumeAttachment1.Status.State = diskv1alpha2.Detaching

				controller := NewTestAttachDetachController(
					mockCtl,
					testNamespace,
					newAzVolumeAttachment0,
					newAzVolumeAttachment1)

				mockClientsAndAttachmentProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAttachDetach, err error) {
				require.NoError(t, err)

				azVolumeAttachment, localErr := controller.azVolumeClient.DiskV1alpha2().AzVolumeAttachments(testNamespace).Get(context.TODO(), testPrimaryAzVolumeAttachment0Name, metav1.GetOptions{})
				require.NoError(t, localErr)
				require.Equal(t, azVolumeAttachment.Status.State, diskv1alpha2.AttachmentPending)
				require.Contains(t, azVolumeAttachment.ObjectMeta.Annotations, consts.RecoverAnnotation)

				azVolumeAttachment, localErr = controller.azVolumeClient.DiskV1alpha2().AzVolumeAttachments(testNamespace).Get(context.TODO(), testPrimaryAzVolumeAttachment1Name, metav1.GetOptions{})
				require.NoError(t, localErr)
				require.Equal(t, azVolumeAttachment.Status.State, diskv1alpha2.Attached)
				require.Contains(t, azVolumeAttachment.ObjectMeta.Annotations, consts.RecoverAnnotation)
			},
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			mockCtl := gomock.NewController(t)
			defer mockCtl.Finish()
			controller := tt.setupFunc(t, mockCtl)
			err := controller.Recover(context.TODO())
			tt.verifyFunc(t, controller, err)
		})
	}
}
