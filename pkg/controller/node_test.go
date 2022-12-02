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
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/wait"
	fakev1 "k8s.io/client-go/kubernetes/fake"
	"k8s.io/klog/v2/klogr"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdiskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils/mockclient"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func NewTestNodeController(controller *gomock.Controller, namespace string, objects ...runtime.Object) *ReconcileNode {
	azDiskObjs, kubeObjs := splitObjects(objects...)
	controllerSharedState := initState(mockclient.NewMockClient(controller), azdiskfakes.NewSimpleClientset(azDiskObjs...), fakev1.NewSimpleClientset(kubeObjs...), objects...)

	return &ReconcileNode{
		SharedState: controllerSharedState,
		logger:      klogr.New(),
	}
}

func TestNodeControllerReconcile(t *testing.T) {
	tests := []struct {
		description string
		request     reconcile.Request
		setupFunc   func(*testing.T, *gomock.Controller) *ReconcileNode
		verifyFunc  func(*testing.T, *ReconcileNode, reconcile.Result, error)
	}{
		{
			description: "[Success] Should create new AzVolumeReplica when new node becomes available",
			request:     testSchedulableNodeRequest,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileNode {
				newAttachment := testPrimaryAzVolumeAttachment0.DeepCopy()
				newAttachment.Status.State = azdiskv1beta2.Attached

				newVolume := testAzVolume0.DeepCopy()
				newVolume.Status.Detail = &azdiskv1beta2.AzVolumeStatusDetail{
					VolumeID: testManagedDiskURI0,
				}

				newPod := testPod1.DeepCopy()
				newPod.Status.Phase = v1.PodRunning

				controller := NewTestNodeController(
					mockCtl,
					testNamespace,
					newVolume,
					newAttachment,
					&testPersistentVolume0,
					&testNode0,
					&testSchedulableNode1,
					newPod,
				)

				// addTestNodeInAvailableAttachmentsMap(controller.SharedState, testSchedulableNode1.Name, testNodeAvailableAttachmentCount)
				mockClients(controller.cachedClient.(*mockclient.MockClient), controller.azClient, controller.kubeClient)
				controller.priorityReplicaRequestsQueue.Push(context.TODO(), &ReplicaRequest{VolumeName: testPersistentVolume0Name, Priority: 1})

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileNode, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				roleReq, _ := azureutils.CreateLabelRequirements(consts.RoleLabel, selection.Equals, string(azdiskv1beta2.ReplicaRole))
				labelSelector := labels.NewSelector().Add(*roleReq)

				conditionFunc := func() (bool, error) {
					replicas, localError := controller.azClient.DiskV1beta2().AzVolumeAttachments(testPrimaryAzVolumeAttachment0.Namespace).List(context.TODO(), metav1.ListOptions{LabelSelector: labelSelector.String()})
					require.NoError(t, localError)
					require.NotNil(t, replicas)
					return len(replicas.Items) == 1, nil
				}
				err = wait.PollImmediate(verifyCRIInterval, verifyCRITimeout, conditionFunc)

				require.NoError(t, err)
			},
		},
		{
			description: "[Success] Should delete AzDriverNode for deleted Node",
			request:     testNode1Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileNode {
				controller := NewTestNodeController(
					mockCtl,
					testNamespace,
					&testAzDriverNode0,
					&testAzDriverNode1)

				controller.cachedClient.(*mockclient.MockClient).EXPECT().
					Get(gomock.Any(), testNode1Request.NamespacedName, gomock.Any()).
					Return(testNode1NotFoundError)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileNode, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				_, err2 := controller.azClient.DiskV1beta2().AzDriverNodes(testNamespace).Get(context.TODO(), testNode1Name, metav1.GetOptions{})
				require.EqualValues(t, testAzDriverNode1NotFoundError, err2)
			},
		},
		{
			description: "[Success] Should delete AzDriverNode and detach AzVolumeAttachments for deleted Node",
			request:     testNode1Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileNode {
				controller := NewTestNodeController(
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
			verifyFunc: func(t *testing.T, controller *ReconcileNode, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				_, err2 := controller.azClient.DiskV1beta2().AzDriverNodes(testNamespace).Get(context.TODO(), testNode1Name, metav1.GetOptions{})
				require.EqualValues(t, testAzDriverNode1NotFoundError, err2)

				nodeRequirement, _ := azureutils.CreateLabelRequirements(consts.NodeNameLabel, selection.Equals, testNode1Name)
				labelSelector := labels.NewSelector().Add(*nodeRequirement)
				conditionFunc := func() (bool, error) {
					attachments, _ := controller.azClient.DiskV1beta2().AzVolumeAttachments(testNamespace).List(context.TODO(), metav1.ListOptions{LabelSelector: labelSelector.String()})
					detachMarked := true
					for _, attachment := range attachments.Items {
						detachMarked = detachMarked && azureutils.MapContains(attachment.Status.Annotations, consts.VolumeDetachRequestAnnotation)
					}
					return detachMarked, nil
				}
				err = wait.PollImmediate(verifyCRIInterval, verifyCRITimeout, conditionFunc)
				require.NoError(t, err)
			},
		},
		{
			description: "[Success] Should not delete AzDriverNode for existing Node",
			request:     testNode1Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileNode {
				controller := NewTestNodeController(
					mockCtl,
					testNamespace,
					&testAzDriverNode0,
					&testAzDriverNode1)

				controller.cachedClient.(*mockclient.MockClient).EXPECT().
					Get(gomock.Any(), gomock.Any(), gomock.Any()).
					Return(nil).AnyTimes()

				controller.cachedClient.(*mockclient.MockClient).EXPECT().
					List(gomock.Any(), gomock.Any(), gomock.Any()).
					Return(nil).AnyTimes()

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileNode, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				_, err2 := controller.azClient.DiskV1beta2().AzDriverNodes(testNamespace).Get(context.TODO(), testNode1Name, metav1.GetOptions{})
				require.NoError(t, err2)
			},
		},
		{
			description: "[Failure] Should requeue on failure to get Node",
			request:     testNode1Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileNode {
				controller := NewTestNodeController(
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
			verifyFunc: func(t *testing.T, controller *ReconcileNode, result reconcile.Result, err error) {
				require.EqualValues(t, testNode1ServerTimeoutError, err)
				require.True(t, result.Requeue)

				_, err2 := controller.azClient.DiskV1beta2().AzDriverNodes(testNamespace).Get(context.TODO(), testNode1Name, metav1.GetOptions{})
				require.NoError(t, err2)
			},
		},
		{
			description: "[Success] Should add new AzDriverNode in availableAttachmentsMap",
			request:     testNode1Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileNode {
				controller := NewTestNodeController(
					mockCtl,
					testNamespace,
					&testAzDriverNode1,
					&testNode1)

				mockClients(controller.cachedClient.(*mockclient.MockClient), controller.azClient, controller.kubeClient)
				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileNode, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				_, err2 := controller.azClient.DiskV1beta2().AzDriverNodes(testNamespace).Get(context.TODO(), testNode1Name, metav1.GetOptions{})
				require.NoError(t, err2)
				_, nodeExists := controller.availableAttachmentsMap.Load(testNode1Name)
				require.True(t, nodeExists)
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

func TestAzDriverNodeRecover(t *testing.T) {
	tests := []struct {
		description string
		setupFunc   func(*testing.T, *gomock.Controller) *ReconcileNode
		verifyFunc  func(*testing.T, *ReconcileNode, error)
	}{
		{
			description: "[Success] Should update AzDriverNode annotation and add it to availableAttachmentsMap.",
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileNode {
				controller := NewTestNodeController(
					mockCtl,
					testNamespace,
					&testNode0,
					&testAzDriverNode0)

				mockClients(controller.cachedClient.(*mockclient.MockClient), controller.azClient, controller.kubeClient)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileNode, err error) {
				require.NoError(t, err)
				_, node0Exists := controller.availableAttachmentsMap.Load(testAzDriverNode0.Name)
				require.True(t, node0Exists)

				azDriverNodes, err := controller.azClient.DiskV1beta2().AzDriverNodes(testNamespace).List(context.TODO(), metav1.ListOptions{})
				require.NoError(t, err)
				require.Len(t, azDriverNodes.Items, 1)
				require.Equal(t, testNode0Name, azDriverNodes.Items[0].Name)
				require.Contains(t, azDriverNodes.Items[0].Annotations, consts.RecoverAnnotation)
			},
		},
		{
			description: "[Success] Should delete orphaned AzDriverNodes whose corresponding nodes have been deleted.",
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileNode {
				controller := NewTestNodeController(
					mockCtl,
					testNamespace,
					&testNode0,
					&testAzDriverNode0,
					&testAzDriverNode1)

				mockClients(controller.cachedClient.(*mockclient.MockClient), controller.azClient, controller.kubeClient)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileNode, err error) {
				require.NoError(t, err)
				azDriverNodes, err := controller.azClient.DiskV1beta2().AzDriverNodes(testNamespace).List(context.TODO(), metav1.ListOptions{})
				require.NoError(t, err)
				require.Len(t, azDriverNodes.Items, 1)
				require.Equal(t, testNode0Name, azDriverNodes.Items[0].Name)
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
