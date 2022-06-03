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
	"k8s.io/klog/v2/klogr"
	azdiskv1beta1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta1"
	azdiskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller/mockattachmentprovisioner"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller/mockclient"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func NewTestAttachDetachController(controller *gomock.Controller, namespace string, objects ...runtime.Object) *ReconcileAttachDetach {
	azDiskObjs, kubeObjs := splitObjects(objects...)
	controllerSharedState := initState(mockclient.NewMockClient(controller), azdiskfakes.NewSimpleClientset(azDiskObjs...), fakev1.NewSimpleClientset(kubeObjs...), objects...)

	return &ReconcileAttachDetach{
		cloudDiskAttacher:     mockattachmentprovisioner.NewMockAttachmentProvisioner(controller),
		stateLock:             &sync.Map{},
		retryInfo:             newRetryInfo(),
		controllerSharedState: controllerSharedState,
		logger:                klogr.New(),
	}
}

func mockClientsAndAttachmentProvisioner(controller *ReconcileAttachDetach) {
	mockClients(controller.controllerSharedState.cachedClient.(*mockclient.MockClient), controller.controllerSharedState.azClient, controller.controllerSharedState.kubeClient)

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
				newAttachment.Status.State = azdiskv1beta1.AttachmentPending

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
					azVolumeAttachment, localError := controller.controllerSharedState.azClient.DiskV1beta1().AzVolumeAttachments(testPrimaryAzVolumeAttachment0.Namespace).Get(context.TODO(), testPrimaryAzVolumeAttachment0.Name, metav1.GetOptions{})
					if localError != nil {
						return false, nil
					}
					return azVolumeAttachment.Status.State == azdiskv1beta1.Attached, nil
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
				newAttachment.Status.State = azdiskv1beta1.Attached
				newAttachment.Status.Annotations = map[string]string{consts.VolumeDetachRequestAnnotation: "crdProvisioner"}
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
					azVolumeAttachment, localError := controller.controllerSharedState.azClient.DiskV1beta1().AzVolumeAttachments(testPrimaryAzVolumeAttachment0.Namespace).Get(context.TODO(), testPrimaryAzVolumeAttachment0.Name, metav1.GetOptions{})
					if localError != nil {
						return false, nil
					}
					return azVolumeAttachment.Status.State == azdiskv1beta1.Detached, nil
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
				newAttachment.Status.Detail = &azdiskv1beta1.AzVolumeAttachmentStatusDetail{
					PublishContext: map[string]string{},
					Role:           azdiskv1beta1.ReplicaRole,
				}
				newAttachment.Labels = map[string]string{consts.RoleLabel: string(azdiskv1beta1.PrimaryRole)}
				newAttachment.Spec.RequestedRole = azdiskv1beta1.PrimaryRole
				newAttachment.Status.State = azdiskv1beta1.Attached

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

				azVolumeAttachment, localError := controller.controllerSharedState.azClient.DiskV1beta1().AzVolumeAttachments(testReplicaAzVolumeAttachment.Namespace).Get(context.TODO(), testReplicaAzVolumeAttachment.Name, metav1.GetOptions{})
				require.NoError(t, localError)
				require.NotNil(t, azVolumeAttachment)
				require.NotNil(t, azVolumeAttachment.Status.Detail)
				require.Equal(t, azVolumeAttachment.Status.Detail.Role, azdiskv1beta1.PrimaryRole)
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

				azVolumeAttachments, localErr := controller.controllerSharedState.azClient.DiskV1beta1().AzVolumeAttachments(testNamespace).List(context.TODO(), metav1.ListOptions{})
				require.NoError(t, localErr)
				require.Len(t, azVolumeAttachments.Items, 1)
			},
		},
		{
			description: "[Success] Should update AzVolumeAttachment CRIs to right state",
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAttachDetach {
				newAzVolumeAttachment0 := testPrimaryAzVolumeAttachment0.DeepCopy()
				newAzVolumeAttachment0.Status.State = azdiskv1beta1.Attaching

				newAzVolumeAttachment1 := testPrimaryAzVolumeAttachment1.DeepCopy()
				newAzVolumeAttachment1.Status.State = azdiskv1beta1.Detaching

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

				azVolumeAttachment, localErr := controller.controllerSharedState.azClient.DiskV1beta1().AzVolumeAttachments(testNamespace).Get(context.TODO(), testPrimaryAzVolumeAttachment0Name, metav1.GetOptions{})
				require.NoError(t, localErr)
				require.Equal(t, azVolumeAttachment.Status.State, azdiskv1beta1.AttachmentPending)
				require.Contains(t, azVolumeAttachment.Status.Annotations, consts.RecoverAnnotation)

				azVolumeAttachment, localErr = controller.controllerSharedState.azClient.DiskV1beta1().AzVolumeAttachments(testNamespace).Get(context.TODO(), testPrimaryAzVolumeAttachment1Name, metav1.GetOptions{})
				require.NoError(t, localErr)
				require.Equal(t, azVolumeAttachment.Status.State, azdiskv1beta1.Attached)
				require.Contains(t, azVolumeAttachment.Status.Annotations, consts.RecoverAnnotation)
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
