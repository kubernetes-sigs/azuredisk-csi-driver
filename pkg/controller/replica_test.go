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
	"time"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	fakev1 "k8s.io/client-go/kubernetes/fake"
	diskv1alpha1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	diskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller/mockclient"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	testTimeUntilGarbageCollection = time.Duration(30) * time.Second
)

func NewTestReplicaController(controller *gomock.Controller, namespace string, objects ...runtime.Object) *ReconcileReplica {
	diskv1alpha1Objs, kubeObjs := splitObjects(objects...)
	controllerSharedState := initState(objects...)

	return &ReconcileReplica{
		client:                     mockclient.NewMockClient(controller),
		azVolumeClient:             diskfakes.NewSimpleClientset(diskv1alpha1Objs...),
		kubeClient:                 fakev1.NewSimpleClientset(kubeObjs...),
		namespace:                  namespace,
		mutexLocks:                 sync.Map{},
		cleanUpMap:                 sync.Map{},
		deletionMap:                sync.Map{},
		controllerSharedState:      controllerSharedState,
		timeUntilGarbageCollection: testTimeUntilGarbageCollection,
	}
}

func TestReplicaReconcile(t *testing.T) {
	tests := []struct {
		description string
		request     reconcile.Request
		setupFunc   func(*testing.T, *gomock.Controller) *ReconcileReplica
		verifyFunc  func(*testing.T, *ReconcileReplica, reconcile.Result, error)
	}{
		{
			description: "[Success] Should create a replacement replica attachment upon replica deletion.",
			request:     testReplicaAzVolumeAttachmentRequest,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileReplica {
				replicaAttachment := testReplicaAzVolumeAttachment
				now := metav1.Time{Time: metav1.Now().Add(-1000)}
				replicaAttachment.DeletionTimestamp = &now

				newVolume := testAzVolume0.DeepCopy()
				newVolume.Status.Detail = &diskv1alpha1.AzVolumeStatusDetail{
					ResponseObject: &diskv1alpha1.AzVolumeStatusParams{
						VolumeID: testManagedDiskURI0,
					},
				}

				controller := NewTestReplicaController(
					mockCtl,
					testNamespace,
					newVolume,
					&testPersistentVolume0,
					&testAzDriverNode0,
					&testAzDriverNode1,
					&testPod0,
					&replicaAttachment)

				mockClients(controller.client.(*mockclient.MockClient), controller.azVolumeClient, controller.kubeClient)
				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileReplica, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				// delete the original replica attachment so that manageReplica can kick in
				err = controller.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(testNamespace).Delete(context.TODO(), testReplicaAzVolumeAttachmentName, metav1.DeleteOptions{})
				require.NoError(t, err)
				_, err = controller.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(testNamespace).Get(context.TODO(), testReplicaAzVolumeAttachmentName, metav1.GetOptions{})
				require.True(t, errors.IsNotFound(err))

				result, err = controller.Reconcile(context.TODO(), testReplicaAzVolumeAttachmentRequest)
				require.NoError(t, err)
				require.False(t, result.Requeue)

				conditionFunc := func() (bool, error) {
					roleReq, _ := createLabelRequirements(consts.RoleLabel, string(diskv1alpha1.ReplicaRole))
					labelSelector := labels.NewSelector().Add(*roleReq)
					replicas, localError := controller.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(testNamespace).List(context.TODO(), metav1.ListOptions{LabelSelector: labelSelector.String()})
					require.NoError(t, localError)
					require.NotNil(t, replicas)
					return len(replicas.Items) == 1, nil
				}
				err = wait.PollImmediate(verifyCRIInterval, verifyCRITimeout, conditionFunc)
				require.NoError(t, err)
			},
		},
		{
			description: "[Success] Should create a replacement replica attachment upon replica promotion.",
			request:     testReplicaAzVolumeAttachmentRequest,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileReplica {
				replicaAttachment := testReplicaAzVolumeAttachment.DeepCopy()
				replicaAttachment.Status.Detail = &diskv1alpha1.AzVolumeAttachmentStatusDetail{
					PublishContext: map[string]string{},
					Role:           diskv1alpha1.ReplicaRole,
				}
				replicaAttachment.Labels[consts.RoleLabel] = string(diskv1alpha1.PrimaryRole)
				replicaAttachment.Spec.RequestedRole = diskv1alpha1.PrimaryRole

				newVolume := testAzVolume0.DeepCopy()
				newVolume.Status.Detail = &diskv1alpha1.AzVolumeStatusDetail{
					ResponseObject: &diskv1alpha1.AzVolumeStatusParams{
						VolumeID: testManagedDiskURI0,
					},
				}

				controller := NewTestReplicaController(
					mockCtl,
					testNamespace,
					newVolume,
					&testPersistentVolume0,
					&testAzDriverNode0,
					&testAzDriverNode1,
					&testPod0,
					replicaAttachment)

				mockClients(controller.client.(*mockclient.MockClient), controller.azVolumeClient, controller.kubeClient)
				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileReplica, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				roleReq, _ := createLabelRequirements(consts.RoleLabel, string(diskv1alpha1.ReplicaRole))
				labelSelector := labels.NewSelector().Add(*roleReq)
				replicas, localError := controller.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(testNamespace).List(context.TODO(), metav1.ListOptions{LabelSelector: labelSelector.String()})
				require.NoError(t, localError)
				require.NotNil(t, replicas)
				require.Len(t, replicas.Items, 1)
			},
		},
		{
			description: "[Success] Should clean up replica AzVolumeAttachments upon primary AzVolumeAttachment deletion.",
			request:     testPrimaryAzVolumeAttachment0Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileReplica {
				primaryAttachment := testPrimaryAzVolumeAttachment0.DeepCopy()
				now := metav1.Time{Time: metav1.Now().Add(-1000)}
				primaryAttachment.DeletionTimestamp = &now
				primaryAttachment.Annotations = map[string]string{consts.VolumeDetachRequestAnnotation: "true"}

				newVolume := testAzVolume0.DeepCopy()
				newVolume.Status.Detail = &diskv1alpha1.AzVolumeStatusDetail{
					ResponseObject: &diskv1alpha1.AzVolumeStatusParams{
						VolumeID: testManagedDiskURI0,
					},
				}

				controller := NewTestReplicaController(
					mockCtl,
					testNamespace,
					newVolume,
					primaryAttachment,
					&testPersistentVolume0,
					&testAzDriverNode0,
					&testAzDriverNode1,
					&testReplicaAzVolumeAttachment)

				mockClients(controller.client.(*mockclient.MockClient), controller.azVolumeClient, controller.kubeClient)
				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileReplica, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				// wait for the garbage collection to queue
				time.Sleep(controller.timeUntilGarbageCollection + time.Minute)
				roleReq, _ := createLabelRequirements(consts.RoleLabel, string(diskv1alpha1.ReplicaRole))
				labelSelector := labels.NewSelector().Add(*roleReq)
				replicas, localError := controller.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(testNamespace).List(context.TODO(), metav1.ListOptions{LabelSelector: labelSelector.String()})
				require.NoError(t, localError)
				require.NotNil(t, replicas)
				require.Len(t, replicas.Items, 0)
			},
		},
		{
			description: "[Success] Should not clean up replica AzVolumeAttachments if a new primary is created / promoted.",
			request:     testReplicaAzVolumeAttachmentRequest,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileReplica {
				primaryAttachment := testPrimaryAzVolumeAttachment0.DeepCopy()
				now := metav1.Time{Time: metav1.Now().Add(-1000)}
				primaryAttachment.DeletionTimestamp = &now
				replicaAttachment := testReplicaAzVolumeAttachment.DeepCopy()
				replicaAttachment.Status.Detail = &diskv1alpha1.AzVolumeAttachmentStatusDetail{
					PublishContext: map[string]string{},
					Role:           diskv1alpha1.ReplicaRole,
				}

				newVolume := testAzVolume0.DeepCopy()
				newVolume.Status.Detail = &diskv1alpha1.AzVolumeStatusDetail{
					ResponseObject: &diskv1alpha1.AzVolumeStatusParams{
						VolumeID: testManagedDiskURI0,
					},
				}

				controller := NewTestReplicaController(
					mockCtl,
					testNamespace,
					newVolume,
					primaryAttachment,
					&testPersistentVolume0,
					&testAzDriverNode0,
					&testAzDriverNode1,
					&testPod0,
					replicaAttachment)

				mockClients(controller.client.(*mockclient.MockClient), controller.azVolumeClient, controller.kubeClient)

				// start garbage collection
				result, err := controller.Reconcile(context.TODO(), testPrimaryAzVolumeAttachment0Request)
				require.NoError(t, err)
				require.False(t, result.Requeue)

				// fully delete primary
				err = controller.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(primaryAttachment.Namespace).Delete(context.TODO(), primaryAttachment.Name, metav1.DeleteOptions{})
				require.NoError(t, err)

				// promote replica to primary
				replicaAttachment = replicaAttachment.DeepCopy()
				replicaAttachment.Labels[consts.RoleLabel] = string(diskv1alpha1.PrimaryRole)
				replicaAttachment.Spec.RequestedRole = diskv1alpha1.PrimaryRole

				err = controller.client.Update(context.TODO(), replicaAttachment)
				require.NoError(t, err)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileReplica, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				time.Sleep(controller.timeUntilGarbageCollection + time.Minute)
				roleReq, _ := createLabelRequirements(consts.RoleLabel, string(diskv1alpha1.ReplicaRole))
				labelSelector := labels.NewSelector().Add(*roleReq)
				replicas, localError := controller.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(testNamespace).List(context.TODO(), metav1.ListOptions{LabelSelector: labelSelector.String()})
				require.NoError(t, localError)
				require.NotNil(t, replicas)
				// clean up should not have happened
				require.Len(t, replicas.Items, 1)
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
