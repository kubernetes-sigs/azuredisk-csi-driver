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
	fakev1 "k8s.io/client-go/kubernetes/fake"
	diskv1alpha1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	diskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller/mockattachmentprovisioner"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller/mockclient"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func NewTestAzVolumeAttachmentController(controller *gomock.Controller, namespace string, objects ...runtime.Object) *ReconcileAzVolumeAttachment {
	diskv1alpha1Objs, kubeObjs := splitObjects(objects...)

	return &ReconcileAzVolumeAttachment{
		client:                mockclient.NewMockClient(controller),
		azVolumeClient:        diskfakes.NewSimpleClientset(diskv1alpha1Objs...),
		kubeClient:            fakev1.NewSimpleClientset(kubeObjs...),
		syncMutex:             sync.RWMutex{},
		namespace:             namespace,
		mutexMap:              make(map[string]*sync.Mutex),
		mutexMapMutex:         sync.RWMutex{},
		volumeMap:             make(map[string]string),
		volumeMapMutex:        sync.RWMutex{},
		cleanUpMap:            make(map[string]context.CancelFunc),
		cleanUpMapMutex:       sync.RWMutex{},
		attachmentProvisioner: mockattachmentprovisioner.NewMockAttachmentProvisioner(controller),
	}
}

func mockClientsAndAttachmentProvisioner(controller *ReconcileAzVolumeAttachment) {
	mockClients(controller.client.(*mockclient.MockClient), controller.azVolumeClient, controller.kubeClient)

	controller.attachmentProvisioner.(*mockattachmentprovisioner.MockAttachmentProvisioner).EXPECT().
		PublishVolume(gomock.Any(), testManagedDiskURI, gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, volumeID, nodeID string, volumeContext map[string]string) (map[string]string, error) {
			return volumeContext, nil
		}).
		MaxTimes(1)
	controller.attachmentProvisioner.(*mockattachmentprovisioner.MockAttachmentProvisioner).EXPECT().
		UnpublishVolume(gomock.Any(), testManagedDiskURI, gomock.Any()).
		DoAndReturn(func(ctx context.Context, volumeID, nodeID string) error {
			return nil
		}).
		MaxTimes(1)
}

func TestAzVolumeAttachmentControllerReconcile(t *testing.T) {
	tests := []struct {
		description string
		request     reconcile.Request
		setupFunc   func(*testing.T, *gomock.Controller) *ReconcileAzVolumeAttachment
		verifyFunc  func(*testing.T, *ReconcileAzVolumeAttachment, reconcile.Result, error)
	}{
		{
			description: "[Success] Should attach volume when new primary AzVolumeAttachment is created.",
			request:     testPrimaryAzVolumeAttachmentRequest,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAzVolumeAttachment {
				newAttachment := testPrimaryAzVolumeAttachment.DeepCopy()
				newAttachment.Status.State = diskv1alpha1.AttachmentPending

				controller := NewTestAzVolumeAttachmentController(
					mockCtl,
					testNamespace,
					&testAzVolume0,
					newAttachment)

				mockClientsAndAttachmentProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAzVolumeAttachment, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				azVolumeAttachment, localError := controller.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(testPrimaryAzVolumeAttachment.Namespace).Get(context.TODO(), testPrimaryAzVolumeAttachment.Name, metav1.GetOptions{})
				require.NoError(t, localError)
				require.Equal(t, diskv1alpha1.Attached, azVolumeAttachment.Status.State)
			},
		},
		{
			description: "[Success] Should detach volume when new primary AzVolumeAttachment is deleted.",
			request:     testPrimaryAzVolumeAttachmentRequest,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAzVolumeAttachment {
				newAttachment := testPrimaryAzVolumeAttachment.DeepCopy()
				newAttachment.Status.State = diskv1alpha1.Attached
				now := metav1.Time{Time: metav1.Now().Add(-1000)}
				newAttachment.DeletionTimestamp = &now

				controller := NewTestAzVolumeAttachmentController(
					mockCtl,
					testNamespace,
					&testAzVolume0,
					newAttachment)

				mockClientsAndAttachmentProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAzVolumeAttachment, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				azVolumeAttachment, localError := controller.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(testPrimaryAzVolumeAttachment.Namespace).Get(context.TODO(), testPrimaryAzVolumeAttachment.Name, metav1.GetOptions{})
				require.NoError(t, localError)
				require.Equal(t, diskv1alpha1.Detached, azVolumeAttachment.Status.State)
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

func TestAzVolumeAttachmentControllerRecover(t *testing.T) {
	tests := []struct {
		description string
		setupFunc   func(*testing.T, *gomock.Controller) *ReconcileAzVolumeAttachment
		verifyFunc  func(*testing.T, *ReconcileAzVolumeAttachment, error)
	}{
		{
			description: "[Success] Should create AzVolumeAttachment instances for VolumeAttachment of PV using Azure Disk CSI Driver.",
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAzVolumeAttachment {

				controller := NewTestAzVolumeAttachmentController(
					mockCtl,
					testNamespace,
					&testVolumeAttachment,
					&testPersistentVolume0)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAzVolumeAttachment, err error) {
				require.NoError(t, err)

				azVolumeAttachments, localErr := controller.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(testNamespace).List(context.TODO(), metav1.ListOptions{})
				require.NoError(t, localErr)
				require.Len(t, azVolumeAttachments.Items, 1)
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
