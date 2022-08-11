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
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2/klogr"
	azdiskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller/mockclient"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func NewTestAzDriverNodeController(controller *gomock.Controller, namespace string, objects ...runtime.Object) *ReconcileAzDriverNode {
	azDiskObjs, _ := splitObjects(objects...)
	controllerSharedState := initState(mockclient.NewMockClient(controller), azdiskfakes.NewSimpleClientset(azDiskObjs...), nil, objects...)

	return &ReconcileAzDriverNode{
		SharedState: controllerSharedState,
		logger:      klogr.New(),
	}
}

func TestAzDriverNodeControllerReconcile(t *testing.T) {
	tests := []struct {
		description string
		request     reconcile.Request
		setupFunc   func(*testing.T, *gomock.Controller) *ReconcileAzDriverNode
		verifyFunc  func(*testing.T, *ReconcileAzDriverNode, reconcile.Result, error)
	}{
		{
			description: "[Success] Should delete AzDriverNode for deleted Node",
			request:     testNode1Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAzDriverNode {
				controller := NewTestAzDriverNodeController(
					mockCtl,
					testNamespace,
					&testAzDriverNode0,
					&testAzDriverNode1)

				controller.cachedClient.(*mockclient.MockClient).EXPECT().
					Get(gomock.Any(), testNode1Request.NamespacedName, gomock.Any()).
					Return(testNode1NotFoundError)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAzDriverNode, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				_, err2 := controller.azClient.DiskV1beta2().AzDriverNodes(testNamespace).Get(context.TODO(), testNode1Name, metav1.GetOptions{})
				require.EqualValues(t, testAzDriverNode1NotFoundError, err2)
			},
		},
		{
			description: "[Success] Should delete AzDriverNode and AzVolumeAttachments for deleted Node",
			request:     testNode1Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAzDriverNode {
				controller := NewTestAzDriverNodeController(
					mockCtl,
					testNamespace,
					&testAzDriverNode0,
					&testAzDriverNode1,
					&testPrimaryAzVolumeAttachment0,
					&testReplicaAzVolumeAttachment,
					&testPersistentVolume0,
				)

				controller.cachedClient.(*mockclient.MockClient).EXPECT().
					Get(gomock.Any(), testNode1Request.NamespacedName, gomock.Any()).
					Return(testNode1NotFoundError).
					AnyTimes()

				mockClients(controller.cachedClient.(*mockclient.MockClient), controller.azClient, nil)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAzDriverNode, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				_, err2 := controller.azClient.DiskV1beta2().AzDriverNodes(testNamespace).Get(context.TODO(), testNode1Name, metav1.GetOptions{})
				require.EqualValues(t, testAzDriverNode1NotFoundError, err2)

				nodeRequirement, _ := azureutils.CreateLabelRequirements(consts.NodeNameLabel, selection.Equals, testNode1Name)
				labelSelector := labels.NewSelector().Add(*nodeRequirement)
				conditionFunc := func() (bool, error) {
					attachments, _ := controller.azClient.DiskV1beta2().AzVolumeAttachments(testNamespace).List(context.TODO(), metav1.ListOptions{LabelSelector: labelSelector.String()})
					return len(attachments.Items) == 0, nil
				}
				err = wait.PollImmediate(verifyCRIInterval, verifyCRITimeout, conditionFunc)
				require.NoError(t, err)
			},
		},
		{
			description: "[Success] Should not delete AzDriverNode for existing Node",
			request:     testNode1Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAzDriverNode {
				controller := NewTestAzDriverNodeController(
					mockCtl,
					testNamespace,
					&testAzDriverNode0,
					&testAzDriverNode1)

				controller.cachedClient.(*mockclient.MockClient).EXPECT().
					Get(gomock.Any(), testNode1Request.NamespacedName, gomock.Any()).
					Return(nil).
					AnyTimes()

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAzDriverNode, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				_, err2 := controller.azClient.DiskV1beta2().AzDriverNodes(testNamespace).Get(context.TODO(), testNode1Name, metav1.GetOptions{})
				require.NoError(t, err2)
			},
		},
		{
			description: "[Failure] Should requeue on failure to get Node",
			request:     testNode1Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAzDriverNode {
				controller := NewTestAzDriverNodeController(
					mockCtl,
					testNamespace,
					&testAzDriverNode0,
					&testAzDriverNode1)

				controller.cachedClient.(*mockclient.MockClient).EXPECT().
					Get(gomock.Any(), testNode1Request.NamespacedName, gomock.Any()).
					Return(testNode1ServerTimeoutError).
					AnyTimes()

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAzDriverNode, result reconcile.Result, err error) {
				require.EqualValues(t, testNode1ServerTimeoutError, err)
				require.True(t, result.Requeue)

				_, err2 := controller.azClient.DiskV1beta2().AzDriverNodes(testNamespace).Get(context.TODO(), testNode1Name, metav1.GetOptions{})
				require.NoError(t, err2)
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
