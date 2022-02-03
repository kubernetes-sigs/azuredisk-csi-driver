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
	"time"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/wait"
	fakev1 "k8s.io/client-go/kubernetes/fake"
	diskv1alpha1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	diskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
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
					&testNode0,
					&testNode1,
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
					roleReq, _ := azureutils.CreateLabelRequirements(consts.RoleLabel, selection.Equals, string(diskv1alpha1.ReplicaRole))
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
				replicaAttachment.Status = diskv1alpha1.AzVolumeAttachmentStatus{
					Detail: &diskv1alpha1.AzVolumeAttachmentStatusDetail{
						PublishContext: map[string]string{},
						Role:           diskv1alpha1.ReplicaRole,
					},
					State: diskv1alpha1.Attached,
				}

				replicaAttachment.Spec.RequestedRole = diskv1alpha1.PrimaryRole
				replicaAttachment = updateRole(replicaAttachment, diskv1alpha1.PrimaryRole)

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
					&testNode0,
					&testNode1,
					&testPod0,
					replicaAttachment)

				mockClients(controller.client.(*mockclient.MockClient), controller.azVolumeClient, controller.kubeClient)
				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileReplica, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)
				conditionFunc := func() (bool, error) {
					roleReq, _ := azureutils.CreateLabelRequirements(consts.RoleLabel, selection.Equals, string(diskv1alpha1.ReplicaRole))
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
					&testNode0,
					&testNode1,
					&testReplicaAzVolumeAttachment)

				mockClients(controller.client.(*mockclient.MockClient), controller.azVolumeClient, controller.kubeClient)
				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileReplica, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				// wait for the garbage collection to queue
				time.Sleep(controller.timeUntilGarbageCollection + time.Minute)
				roleReq, _ := azureutils.CreateLabelRequirements(consts.RoleLabel, selection.Equals, string(diskv1alpha1.ReplicaRole))
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
				replicaAttachment.Status = diskv1alpha1.AzVolumeAttachmentStatus{
					Detail: &diskv1alpha1.AzVolumeAttachmentStatusDetail{
						PublishContext: map[string]string{},
						Role:           diskv1alpha1.ReplicaRole,
					},
					State: diskv1alpha1.Attached,
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
					&testNode0,
					&testNode1,
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
				replicaAttachment.Spec.RequestedRole = diskv1alpha1.PrimaryRole
				replicaAttachment = updateRole(replicaAttachment.DeepCopy(), diskv1alpha1.PrimaryRole)

				err = controller.client.Update(context.TODO(), replicaAttachment)
				require.NoError(t, err)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileReplica, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				time.Sleep(controller.timeUntilGarbageCollection + time.Minute)
				roleReq, _ := azureutils.CreateLabelRequirements(consts.RoleLabel, selection.Equals, string(diskv1alpha1.ReplicaRole))
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

func TestGetNodesForReplica(t *testing.T) {
	tests := []struct {
		description string
		volumes     []string
		pods        []string
		setupFunc   func(*testing.T, *gomock.Controller) *ReconcileReplica
		verifyFunc  func(*testing.T, []string, error)
	}{
		{
			description: "[Success] Should not select nodes with no remaining capacity.",
			volumes:     []string{testPersistentVolume0Name},
			pods:        []string{getQualifiedName(testNamespace, testPod0Name)},
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

				newNode := testNode0.DeepCopy()
				newNode.Status.Allocatable[consts.AttachableVolumesField] = resource.MustParse("0")

				controller := NewTestReplicaController(
					mockCtl,
					testNamespace,
					newVolume,
					&testPersistentVolume0,
					newNode,
					&testNode2,
					&testPod0,
				)

				mockClients(controller.client.(*mockclient.MockClient), controller.azVolumeClient, controller.kubeClient)
				return controller
			},
			verifyFunc: func(t *testing.T, nodes []string, err error) {
				require.NoError(t, err)
				require.Len(t, nodes, 1)
				require.NotEqual(t, nodes[0], testNode0Name)
			},
		},
		{
			description: "[Success] Should not create replica attachment on a node that does not match volume's node affinity rule",
			volumes:     []string{testPersistentVolume0Name},
			pods:        []string{getQualifiedName(testNamespace, testPod0Name)},
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileReplica {
				replicaAttachment := testReplicaAzVolumeAttachment
				now := metav1.Time{Time: metav1.Now().Add(-1000)}
				replicaAttachment.DeletionTimestamp = &now

				newPV := testPersistentVolume0.DeepCopy()
				newPV.Spec.NodeAffinity = &v1.VolumeNodeAffinity{
					Required: &v1.NodeSelector{
						NodeSelectorTerms: []v1.NodeSelectorTerm{{
							MatchExpressions: []v1.NodeSelectorRequirement{{
								Key:      consts.TopologyRegionKey,
								Operator: v1.NodeSelectorOpIn,
								Values:   []string{"westus2"},
							}},
						}},
					},
				}

				newVolume := testAzVolume0.DeepCopy()
				newVolume.Status.Detail = &diskv1alpha1.AzVolumeStatusDetail{
					ResponseObject: &diskv1alpha1.AzVolumeStatusParams{
						VolumeID: testManagedDiskURI0,
					},
				}

				newNode := testNode0.DeepCopy()
				newNode.Labels = map[string]string{consts.TopologyRegionKey: "westus2"}

				controller := NewTestReplicaController(
					mockCtl,
					testNamespace,
					newVolume,
					newPV,
					newNode,
					&testNode1,
					&testPod0,
				)

				mockClients(controller.client.(*mockclient.MockClient), controller.azVolumeClient, controller.kubeClient)
				return controller
			},
			verifyFunc: func(t *testing.T, nodes []string, err error) {
				require.NoError(t, err)
				require.Len(t, nodes, 1)
				require.NotEqual(t, nodes[0], testNode1Name)
			},
		},
	}
	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			mockCtl := gomock.NewController(t)
			defer mockCtl.Finish()
			controller := tt.setupFunc(t, mockCtl)
			nodes, err := getRankedNodesForReplicaAttachments(context.TODO(), controller, tt.volumes, tt.pods)
			tt.verifyFunc(t, nodes, err)
		})
	}
}
