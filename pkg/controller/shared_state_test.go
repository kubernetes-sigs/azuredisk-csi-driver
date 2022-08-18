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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakev1 "k8s.io/client-go/kubernetes/fake"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdiskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller/mockclient"
)

func NewTestSharedState(controller *gomock.Controller, namespace string, objects ...runtime.Object) *SharedState {
	azDiskObjs, kubeObjs := splitObjects(objects...)
	testSharedState := initState(mockclient.NewMockClient(controller), azdiskfakes.NewSimpleClientset(azDiskObjs...), fakev1.NewSimpleClientset(kubeObjs...), objects...)

	return testSharedState
}

func TestGetNodesForReplica(t *testing.T) {
	tests := []struct {
		description string
		volumes     []string
		pods        []v1.Pod
		setupFunc   func(*testing.T, *gomock.Controller) *ReconcileReplica
		verifyFunc  func(*testing.T, []string, error)
	}{
		{
			description: "[Success] Should not select nodes with no remaining capacity.",
			volumes:     []string{testPersistentVolume0Name},
			pods:        []v1.Pod{testPod0},
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileReplica {
				replicaAttachment := testReplicaAzVolumeAttachment
				now := metav1.Time{Time: metav1.Now().Add(-1000)}
				replicaAttachment.DeletionTimestamp = &now

				newVolume := testAzVolume0.DeepCopy()
				newVolume.Status.Detail = &azdiskv1beta2.AzVolumeStatusDetail{
					VolumeID: testManagedDiskURI0,
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

				mockClients(controller.cachedClient.(*mockclient.MockClient), controller.azClient, controller.kubeClient)
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
			pods:        []v1.Pod{testPod0},
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
				newVolume.Status.Detail = &azdiskv1beta2.AzVolumeStatusDetail{
					VolumeID: testManagedDiskURI0,
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

				mockClients(controller.cachedClient.(*mockclient.MockClient), controller.azClient, controller.kubeClient)
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
			nodes, err := controller.getRankedNodesForReplicaAttachments(context.TODO(), tt.volumes, tt.pods)
			tt.verifyFunc(t, nodes, err)
		})
	}
}

func TestRecoveryLifecycle(t *testing.T) {
	tests := []struct {
		description string
		setupFunc   func(*testing.T, *gomock.Controller) *SharedState
		verifyFunc  func(*testing.T, bool)
	}{
		{
			description: "[Success] IsRecoveryComplete should return true after MarkRecoveryComplete is called",
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				testSharedState := NewTestSharedState(mockCtl, testNamespace)
				testSharedState.MarkRecoveryComplete()

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, isRecoveryComplete bool) {
				require.True(t, isRecoveryComplete)
			},
		},
	}
	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			mockCtl := gomock.NewController(t)
			defer mockCtl.Finish()
			sharedState := tt.setupFunc(t, mockCtl)
			isRecoveryComplete := sharedState.isRecoveryComplete()
			tt.verifyFunc(t, isRecoveryComplete)
		})
	}
}

func TestFilterNodes(t *testing.T) {
	tests := []struct {
		description string
		nodes       []v1.Node
		volumes     []string
		pods        []v1.Pod
		setupFunc   func(*testing.T, *gomock.Controller) *SharedState
		verifyFunc  func(*testing.T, []v1.Node, error)
	}{
		{
			description: "[Failure] Should throw error when AzVolume not found",
			nodes:       []v1.Node{testNode0},
			volumes:     []string{"random-volume-name"},
			pods:        []v1.Pod{testPod0},
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				newVolume := testAzVolume0.DeepCopy()
				newVolume.Status.Detail = &azdiskv1beta2.AzVolumeStatusDetail{
					VolumeID: testManagedDiskURI0,
				}

				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					newVolume,
					&testPersistentVolume0,
					&testNode2,
					&testPod0,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, nodes []v1.Node, err error) {
				require.Error(t, err)
				require.Nil(t, nodes)
			},
		},
		{
			description: "[Success] Successfully filter the given nodes",
			nodes:       []v1.Node{testNode0, testNode1, testNode2},
			volumes:     []string{testPersistentVolume0Name},
			pods:        []v1.Pod{testPod0},
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				newVolume := testAzVolume0.DeepCopy()
				newVolume.Status.Detail = &azdiskv1beta2.AzVolumeStatusDetail{
					VolumeID: testManagedDiskURI0,
				}

				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					newVolume,
					&testPersistentVolume0,
					&testNode2,
					&testPod0,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, nodes []v1.Node, err error) {
				require.NoError(t, err)
				require.Len(t, nodes, 3)
			},
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			mockCtl := gomock.NewController(t)
			defer mockCtl.Finish()
			sharedState := tt.setupFunc(t, mockCtl)
			nodes, err := sharedState.filterNodes(context.TODO(), tt.nodes, tt.pods, tt.volumes)
			tt.verifyFunc(t, nodes, err)
		})
	}
}

func TestGetVolumesFromPod(t *testing.T) {
	tests := []struct {
		description string
		podName     string
		setupFunc   func(*testing.T, *gomock.Controller) *SharedState
		verifyFunc  func(*testing.T, []string, error)
	}{
		{
			description: "[Failure] Should throw an error when PodName is not present in the podToClaims map",
			podName:     "Random-Pod-Name",
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					&testPersistentVolume0,
					&testNode2,
					&testPod0,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, volumes []string, err error) {
				require.Error(t, err)
				require.Nil(t, volumes)
			},
		},
		{
			description: "[Failure] Should throw an error when value in podToClaimsMap is not of type []string",
			podName:     "test-pod-0",
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					&testPod0,
				)
				testSharedState.podToClaimsMap.Store(testPod0Name, 1) // Inserting a value which is not of type []string

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, volumes []string, err error) {
				require.Error(t, err)
				require.Nil(t, volumes)
			},
		},
		{
			description: "[Failure] Should throw an error when volume in claimToVolumeMap is not of type string",
			podName:     "test-pod-0",
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					&testPod0,
				)
				testSharedState.podToClaimsMap.Store(testPod0Name, []string{testPersistentVolumeClaim0Name, testPersistentVolumeClaim1Name})
				testSharedState.claimToVolumeMap.Store(testPersistentVolumeClaim0Name, 1) // Inserting a value which is not of type string

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, volumes []string, err error) {
				require.Error(t, err)
				require.Nil(t, volumes)
			},
		},
		{
			description: "[Success] Successfully return Volumes for the given podName",
			podName:     "test-pod-0",
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					&testPod0,
				)
				testSharedState.podToClaimsMap.Store(testPod0Name, []string{testPersistentVolumeClaim0Name, testPersistentVolumeClaim1Name})
				testSharedState.claimToVolumeMap.Store(testPersistentVolumeClaim0Name, testPersistentVolume0Name)
				testSharedState.claimToVolumeMap.Store(testPersistentVolumeClaim1Name, testPersistentVolume1Name)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, volumes []string, err error) {
				require.NoError(t, err)
				require.Len(t, volumes, 2)
			},
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			mockCtl := gomock.NewController(t)
			defer mockCtl.Finish()
			sharedState := tt.setupFunc(t, mockCtl)
			volumes, err := sharedState.getVolumesFromPod(context.TODO(), test.podName)
			tt.verifyFunc(t, volumes, err)
		})
	}
}

func TestGetPodsFromVolume(t *testing.T) {
	tests := []struct {
		description string
		volumeName  string
		setupFunc   func(*testing.T, *gomock.Controller) *SharedState
		verifyFunc  func(*testing.T, []v1.Pod, error)
	}{
		{
			description: "[Failure] Should return an error when Volume name is not found",
			volumeName:  "invalid-volume",
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					&testPod0,
				)
				testSharedState.podToClaimsMap.Store(testPod0Name, []string{testPersistentVolumeClaim0Name, testPersistentVolumeClaim1Name})
				testSharedState.claimToVolumeMap.Store(testPersistentVolumeClaim0Name, testPersistentVolume0Name)
				testSharedState.claimToVolumeMap.Store(testPersistentVolumeClaim1Name, testPersistentVolume1Name)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, pods []v1.Pod, err error) {
				require.Error(t, err)
				require.Nil(t, pods)
			},
		},
		{
			description: "[Success] Should return Pods for valid Volume",
			volumeName:  testPersistentVolume0Name,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					&testPod0,
					&testPersistentVolume0,
				)
				podName := testNamespace + "/" + testPod0Name // Pods's qualified name is of the format <namespace>/<name>

				testSharedState.volumeToClaimMap.Store(testPersistentVolume0Name, testPersistentVolumeClaim0Name)
				testSharedState.claimToPodsMap.Store(testPersistentVolumeClaim0Name, newLockableEntry(set{podName: {}}))

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, pods []v1.Pod, err error) {
				require.NoError(t, err)
				require.Len(t, pods, 1)
			},
		},
	}
	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			mockCtl := gomock.NewController(t)
			defer mockCtl.Finish()
			sharedState := tt.setupFunc(t, mockCtl)
			pods, err := sharedState.getPodsFromVolume(context.TODO(), sharedState.cachedClient, test.volumeName)
			tt.verifyFunc(t, pods, err)
		})
	}
}

func TestVolumeVisitedFlow(t *testing.T) {
	tests := []struct {
		description  string
		azVolumeName string
		setupFunc    func(*testing.T, *gomock.Controller) *SharedState
		verifyFunc   func(*testing.T, bool)
	}{
		{
			description:  "[Success] isVolumeVisited should return true after markVolumeVisited is called",
			azVolumeName: testPersistentVolume0Name,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				testSharedState := NewTestSharedState(mockCtl, testNamespace)
				testSharedState.markVolumeVisited(testPersistentVolume0Name)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, isVolumeVisited bool) {
				require.True(t, isVolumeVisited)
			},
		},
		{
			description:  "[Success] isVolumeVisited should return false after unmarkVolumeVisited is called",
			azVolumeName: testPersistentVolume0Name,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				testSharedState := NewTestSharedState(mockCtl, testNamespace)
				testSharedState.markVolumeVisited(testPersistentVolume0Name)
				testSharedState.unmarkVolumeVisited(testPersistentVolume0Name)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, isVolumeVisited bool) {
				require.False(t, isVolumeVisited)
			},
		},
	}
	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			mockCtl := gomock.NewController(t)
			defer mockCtl.Finish()
			sharedState := tt.setupFunc(t, mockCtl)
			isVolumeVisited := sharedState.isVolumeVisited(tt.azVolumeName)
			tt.verifyFunc(t, isVolumeVisited)
		})
	}
}

func TestGetNodesWithReplica(t *testing.T) {
	tests := []struct {
		description string
		volumeName  string
		setupFunc   func(*testing.T, *gomock.Controller) *SharedState
		verifyFunc  func(*testing.T, []string, error)
	}{
		{
			description: "[Success] Should return Nodes with AzVolumeAttachments for a valid volume",
			volumeName:  testPersistentVolume0Name,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {

				newNode := testNode0.DeepCopy()

				testSharedState := NewTestSharedState(mockCtl,
					testNamespace,
					newNode,
					&testPersistentVolume0,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, nodes []string, err error) {
				require.NoError(t, err)
				require.NotNil(t, nodes)
			},
		},
	}
	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			mockCtl := gomock.NewController(t)
			defer mockCtl.Finish()
			sharedState := tt.setupFunc(t, mockCtl)
			nodes, err := sharedState.getNodesWithReplica(context.TODO(), tt.volumeName)
			tt.verifyFunc(t, nodes, err)
		})
	}
}
