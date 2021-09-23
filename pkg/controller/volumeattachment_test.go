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
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakev1 "k8s.io/client-go/kubernetes/fake"
	diskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller/mockclient"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func newTestVolumeAttachmentController(controller *gomock.Controller, namespace string, objects ...runtime.Object) *ReconcileVolumeAttachment {
	diskv1alpha1Objs, kubeObjs := splitObjects(objects...)

	return &ReconcileVolumeAttachment{
		client:              mockclient.NewMockClient(controller),
		azVolumeClient:      diskfakes.NewSimpleClientset(diskv1alpha1Objs...),
		kubeClient:          fakev1.NewSimpleClientset(kubeObjs...),
		controllerRetryInfo: newRetryInfo(),
		namespace:           namespace,
	}
}

func TestVolumeAttachmentControllerReconcile(t *testing.T) {
	tests := []struct {
		description string
		request     reconcile.Request
		setupFunc   func(*testing.T, *gomock.Controller) *ReconcileVolumeAttachment
		verifyFunc  func(*testing.T, *ReconcileVolumeAttachment, reconcile.Result, error)
	}{
		{
			description: "[Success] Should annotate AzVolumeAttachment for VolumeAttachment.",
			request:     testVolumeAttachmentRequest,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileVolumeAttachment {
				controller := newTestVolumeAttachmentController(
					mockCtl,
					testNamespace,
					&testVolumeAttachment,
					&testPersistentVolume0,
					&testPrimaryAzVolumeAttachment0)

				mockClients(controller.client.(*mockclient.MockClient), controller.azVolumeClient, controller.kubeClient)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileVolumeAttachment, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				azVolumeAttachment, localErr := controller.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(testNamespace).Get(context.TODO(), testPrimaryAzVolumeAttachment0Name, metav1.GetOptions{})
				require.NoError(t, localErr)
				require.Contains(t, azVolumeAttachment.Annotations, azureutils.VolumeAttachmentExistsAnnotation)
			},
		},
		{
			description: "[Success] Should remove annotation from AzVolumeAttachment when VolumeAttachment is deleted.",
			request:     testVolumeAttachmentRequest,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileVolumeAttachment {
				deletedVolumeAttachment := testVolumeAttachment.DeepCopy()
				now := metav1.Time{Time: metav1.Now().Add(-1000)}
				deletedVolumeAttachment.DeletionTimestamp = &now

				annotatedAzVolumeAttachment := testPrimaryAzVolumeAttachment0.DeepCopy()
				annotatedAzVolumeAttachment.Annotations = map[string]string{
					azureutils.VolumeAttachmentExistsAnnotation: deletedVolumeAttachment.Name,
				}

				controller := newTestVolumeAttachmentController(
					mockCtl,
					testNamespace,
					deletedVolumeAttachment,
					&testPersistentVolume0,
					annotatedAzVolumeAttachment)

				mockClients(controller.client.(*mockclient.MockClient), controller.azVolumeClient, controller.kubeClient)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileVolumeAttachment, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				azVolumeAttachment, localErr := controller.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(testNamespace).Get(context.TODO(), testPrimaryAzVolumeAttachment0Name, metav1.GetOptions{})
				require.NoError(t, localErr)
				require.NotContains(t, azVolumeAttachment.Annotations, azureutils.VolumeAttachmentExistsAnnotation)
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

func TestVolumeAttachmentControllerRecover(t *testing.T) {
	tests := []struct {
		description string
		setupFunc   func(*testing.T, *gomock.Controller) *ReconcileVolumeAttachment
		verifyFunc  func(*testing.T, *ReconcileVolumeAttachment, error)
	}{
		{
			description: "[Success] Should annotate AzVolumeAttachment for VolumeAttachment.",
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileVolumeAttachment {
				controller := newTestVolumeAttachmentController(
					mockCtl,
					testNamespace,
					&testVolumeAttachment,
					&testPersistentVolume0,
					&testPrimaryAzVolumeAttachment0)

				mockClients(controller.client.(*mockclient.MockClient), controller.azVolumeClient, controller.kubeClient)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileVolumeAttachment, err error) {
				require.NoError(t, err)

				azVolumeAttachment, localErr := controller.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(testNamespace).Get(context.TODO(), testPrimaryAzVolumeAttachment0Name, metav1.GetOptions{})
				require.NoError(t, localErr)
				require.Contains(t, azVolumeAttachment.Annotations, azureutils.VolumeAttachmentExistsAnnotation)
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
