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
	"strconv"
	"sync/atomic"
	"testing"
	"time"

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
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils/mockclient"
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
		setupFunc   func(*testing.T, *gomock.Controller) *SharedState
		verifyFunc  func(*testing.T, []string, error)
	}{
		{
			description: "[Success] Should not select nodes with no remaining capacity.",
			volumes:     []string{testPersistentVolume0Name},
			pods:        []v1.Pod{testPod0},
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				newVolume := testAzVolume0.DeepCopy()
				newVolume.Status.Detail = &azdiskv1beta2.AzVolumeStatusDetail{
					VolumeID: testManagedDiskURI0,
				}

				newNode := testNode1.DeepCopy()
				newNode.Status.Allocatable[consts.AttachableVolumesField] = resource.MustParse("1")
				newNode.Labels = azureutils.AddToMap(newNode.Labels, v1.LabelInstanceTypeStable, "BASIC_A0")

				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					newVolume,
					&testPersistentVolume0,
					&testReplicaAzVolumeAttachment,
					newNode,
					&testNode2,
					&testPod0,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
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
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
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
				newNode.Labels = azureutils.AddToMap(newNode.Labels, consts.TopologyRegionKey, "westus2")

				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					newVolume,
					newPV,
					newNode,
					&testNode1,
					&testPod0,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
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

func TestPrioritizeNodes(t *testing.T) {
	tests := []struct {
		description string
		nodes       []v1.Node
		volumes     []string
		pods        []v1.Pod
		setupFunc   func(*testing.T, *gomock.Controller) *SharedState
		verifyFunc  func(*testing.T, []v1.Node)
	}{
		{
			description: "[Success] Should prioritize nodes with more node capacity",
			nodes: []v1.Node{*convertToNode(withLabel(&testNode0, map[string]string{v1.LabelInstanceTypeStable: "BASIC_A3"})),
				*convertToNode(withLabel(&testNode1, map[string]string{v1.LabelInstanceTypeStable: "BASIC_A3"})),
				*convertToNode(withLabel(&testNode2, map[string]string{v1.LabelInstanceTypeStable: "BASIC_A3"}))},
			volumes: []string{testPersistentVolume0Name},
			pods:    []v1.Pod{testPod0},
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				primary1Node1 := createTestAzVolumeAttachment(testPersistentVolume1Name, testNode1Name, azdiskv1beta2.PrimaryRole)
				primary1Node2 := createTestAzVolumeAttachment(testPersistentVolume1Name, testNode2Name, azdiskv1beta2.PrimaryRole)
				primary2Node2 := createTestAzVolumeAttachment(testPersistentVolume2Name, testNode2Name, azdiskv1beta2.PrimaryRole)
				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					convertToNode(withLabel(&testNode0, map[string]string{v1.LabelInstanceTypeStable: "BASIC_A3"})),
					convertToNode(withLabel(&testNode1, map[string]string{v1.LabelInstanceTypeStable: "BASIC_A3"})),
					convertToNode(withLabel(&testNode2, map[string]string{v1.LabelInstanceTypeStable: "BASIC_A3"})),
					&testAzVolume0,
					&testPersistentVolume0,
					&testAzVolume1,
					&testPersistentVolume1,
					&testAzVolume2,
					&testPersistentVolume2,
					&primary1Node1,
					&primary1Node2,
					&primary2Node2,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, nodes []v1.Node) {
				require.Len(t, nodes, 3)
				require.Equal(t, nodes[0].Name, testNode0.Name)
				require.Equal(t, nodes[1].Name, testNode1.Name)
				require.Equal(t, nodes[2].Name, testNode2.Name)
			},
		},
		{
			description: "[Success] Should filter nodes with no node capacity",
			nodes:       []v1.Node{*convertToNode(withLabel(&testNode0, map[string]string{v1.LabelInstanceTypeStable: "BASIC_A0"}))},
			volumes:     []string{testPersistentVolume0Name},
			pods:        []v1.Pod{testPod0},
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				primary1Node0 := createTestAzVolumeAttachment(testPersistentVolume1Name, testNode0Name, azdiskv1beta2.PrimaryRole)
				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					convertToNode(withLabel(&testNode0, map[string]string{v1.LabelInstanceTypeStable: "BASIC_A0"})),
					&testAzVolume0,
					&testPersistentVolume0,
					&testAzVolume1,
					&testPersistentVolume1,
					&primary1Node0,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, nodes []v1.Node) {
				require.Len(t, nodes, 0)
			},
		},
		{
			description: "[Success] Should prioritize nodes that qualify for inter pod preferred affinity rule",
			nodes: []v1.Node{*convertToNode(withLabel(&testNode0, map[string]string{"nodeKey": "nodeValue", v1.LabelInstanceTypeStable: "BASIC_A0"})),
				*convertToNode(withLabel(&testNode1, map[string]string{"nodeKey": "nodeValue", v1.LabelInstanceTypeStable: "BASIC_A0"})),
				testNode2},
			volumes: []string{testPersistentVolume0Name},
			pods: []v1.Pod{createPod(testNamespace, testPod0Name, []string{testPersistentVolumeClaim0Name}, withAffinityRule(v1.Affinity{
				PodAffinity: &v1.PodAffinity{
					PreferredDuringSchedulingIgnoredDuringExecution: []v1.WeightedPodAffinityTerm{{
						PodAffinityTerm: v1.PodAffinityTerm{
							LabelSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"podKey": "podValue"},
							},
							TopologyKey: "nodeKey",
						},
						Weight: 10,
					}},
				}}))},
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				pod0 := createPod(testNamespace, testPod0Name, []string{testPersistentVolumeClaim0Name}, withAffinityRule(v1.Affinity{
					PodAffinity: &v1.PodAffinity{
						PreferredDuringSchedulingIgnoredDuringExecution: []v1.WeightedPodAffinityTerm{{
							PodAffinityTerm: v1.PodAffinityTerm{
								LabelSelector: &metav1.LabelSelector{
									MatchLabels: map[string]string{"podKey": "podValue"},
								},
								TopologyKey: "nodeKey",
							},
							Weight: 10,
						}},
					}}))

				pod1 := createPod(testNamespace, testPod1Name, []string{testPersistentVolumeClaim1Name}, withNode(testNode0Name))
				pod1.Labels = map[string]string{"podKey": "podValue"}
				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					&testAzVolume0,
					&testPersistentVolume0,
					convertToNode(withLabel(&testNode0, map[string]string{"nodeKey": "nodeValue", v1.LabelInstanceTypeStable: "BASIC_A0"})),
					convertToNode(withLabel(&testNode1, map[string]string{"nodeKey": "nodeValue", v1.LabelInstanceTypeStable: "BASIC_A0"})),
					&testNode2,
					&pod0,
					&pod1,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, nodes []v1.Node) {
				require.Len(t, nodes, 3)
				require.Equal(t, nodes[2].Name, testNode2Name)
			},
		},
		{
			description: "[Success] Should prioritize nodes that don't satisfy inter pod preferred anti affinity term",
			nodes: []v1.Node{*convertToNode(withLabel(&testNode0, map[string]string{"nodeKey": "nodeValue", v1.LabelInstanceTypeStable: "BASIC_A0"})),
				*convertToNode(withLabel(&testNode1, map[string]string{"nodeKey": "nodeValue", v1.LabelInstanceTypeStable: "BASIC_A0"})),
				testNode2},
			volumes: []string{testPersistentVolume0Name},
			pods: []v1.Pod{createPod(testNamespace, testPod0Name, []string{testPersistentVolumeClaim0Name}, withAffinityRule(v1.Affinity{
				PodAntiAffinity: &v1.PodAntiAffinity{
					PreferredDuringSchedulingIgnoredDuringExecution: []v1.WeightedPodAffinityTerm{{
						PodAffinityTerm: v1.PodAffinityTerm{
							LabelSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"podKey": "podValue"},
							},
							TopologyKey: "nodeKey",
						},
						Weight: 10,
					}},
				}}))},
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				pod0 := createPod(testNamespace, testPod0Name, []string{testPersistentVolumeClaim0Name}, withAffinityRule(v1.Affinity{
					PodAntiAffinity: &v1.PodAntiAffinity{
						PreferredDuringSchedulingIgnoredDuringExecution: []v1.WeightedPodAffinityTerm{{
							PodAffinityTerm: v1.PodAffinityTerm{
								LabelSelector: &metav1.LabelSelector{
									MatchLabels: map[string]string{"podKey": "podValue"},
								},
								TopologyKey: "nodeKey",
							},
							Weight: 10,
						}},
					}}))

				pod1 := createPod(testNamespace, testPod1Name, []string{testPersistentVolumeClaim1Name}, withNode(testNode0Name))
				pod1.Labels = map[string]string{"podKey": "podValue"}
				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					&testAzVolume0,
					&testPersistentVolume0,
					convertToNode(withLabel(&testNode0, map[string]string{"nodeKey": "nodeValue", v1.LabelInstanceTypeStable: "BASIC_A0"})),
					convertToNode(withLabel(&testNode1, map[string]string{"nodeKey": "nodeValue", v1.LabelInstanceTypeStable: "BASIC_A0"})),
					&testNode2,
					&pod0,
					&pod1,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, nodes []v1.Node) {
				require.Len(t, nodes, 3)
				require.Equal(t, nodes[0].Name, testNode2Name)
			},
		},
		{
			description: "[Success] Should prioritize nodes that don't satisfy node pod preferred affinity term",
			nodes: []v1.Node{*convertToNode(withLabel(&testNode0, map[string]string{"nodeKey0": "nodeValue", "nodeKey1": "nodeValue", v1.LabelInstanceTypeStable: "BASIC_A0"})),
				*convertToNode(withLabel(&testNode1, map[string]string{"nodeKey0": "nodeValue", v1.LabelInstanceTypeStable: "BASIC_A0"})),
				testNode2},
			volumes: []string{testPersistentVolume0Name},
			pods: []v1.Pod{createPod(testNamespace, testPod0Name, []string{testPersistentVolumeClaim0Name}, withAffinityRule(v1.Affinity{
				NodeAffinity: &v1.NodeAffinity{
					PreferredDuringSchedulingIgnoredDuringExecution: []v1.PreferredSchedulingTerm{{
						Preference: v1.NodeSelectorTerm{
							MatchExpressions: []v1.NodeSelectorRequirement{{
								Key:      "nodeKey0",
								Operator: v1.NodeSelectorOpIn,
								Values:   []string{"nodeValue"},
							}},
						},
						Weight: 10,
					}},
				}})),
				createPod(testNamespace, testPod1Name, []string{testPersistentVolumeClaim0Name}, withAffinityRule(v1.Affinity{
					NodeAffinity: &v1.NodeAffinity{
						PreferredDuringSchedulingIgnoredDuringExecution: []v1.PreferredSchedulingTerm{{
							Preference: v1.NodeSelectorTerm{
								MatchExpressions: []v1.NodeSelectorRequirement{{
									Key:      "nodeKey1",
									Operator: v1.NodeSelectorOpIn,
									Values:   []string{"nodeValue"},
								}},
							},
							Weight: 10,
						}},
					}}))},
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				pod0 := createPod(testNamespace, testPod0Name, []string{testPersistentVolumeClaim0Name}, withAffinityRule(v1.Affinity{
					NodeAffinity: &v1.NodeAffinity{
						PreferredDuringSchedulingIgnoredDuringExecution: []v1.PreferredSchedulingTerm{{
							Preference: v1.NodeSelectorTerm{
								MatchExpressions: []v1.NodeSelectorRequirement{{
									Key:      "nodeKey0",
									Operator: v1.NodeSelectorOpIn,
									Values:   []string{"nodeValue"},
								}},
							},
							Weight: 10,
						}},
					}}))

				pod1 := createPod(testNamespace, testPod1Name, []string{testPersistentVolumeClaim0Name}, withAffinityRule(v1.Affinity{
					NodeAffinity: &v1.NodeAffinity{
						PreferredDuringSchedulingIgnoredDuringExecution: []v1.PreferredSchedulingTerm{{
							Preference: v1.NodeSelectorTerm{
								MatchExpressions: []v1.NodeSelectorRequirement{{
									Key:      "nodeKey1",
									Operator: v1.NodeSelectorOpIn,
									Values:   []string{"nodeValue"},
								}},
							},
							Weight: 10,
						}},
					}}))

				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					&testAzVolume0,
					&testPersistentVolume0,
					convertToNode(withLabel(&testNode0, map[string]string{"nodeKey0": "nodeValue", "nodeKey1": "nodeValue", v1.LabelInstanceTypeStable: "BASIC_A0"})),
					convertToNode(withLabel(&testNode1, map[string]string{"nodeKey0": "nodeValue", v1.LabelInstanceTypeStable: "BASIC_A0"})),
					&testNode2,
					&pod0,
					&pod1,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, nodes []v1.Node) {
				require.Len(t, nodes, 3)
				require.Equal(t, nodes[0].Name, testNode0Name)
				require.Equal(t, nodes[1].Name, testNode1Name)
				require.Equal(t, nodes[2].Name, testNode2Name)
			},
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			mockCtl := gomock.NewController(t)
			defer mockCtl.Finish()
			sharedState := tt.setupFunc(t, mockCtl)
			nodes := sharedState.prioritizeNodes(context.TODO(), tt.pods, tt.volumes, tt.nodes)
			tt.verifyFunc(t, nodes)
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
		{
			description: "[Success] Should not select tainted node that pod cannot tolerate",
			nodes:       []v1.Node{*withTaints(&testNode0, []v1.Taint{{Key: "nodeKey", Value: "nodeValue", Effect: v1.TaintEffectNoSchedule}}), testNode1, testNode2},
			volumes:     []string{testPersistentVolume0Name},
			pods:        []v1.Pod{testPod0},
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					&testAzVolume0,
					&testPersistentVolume0,
					withTaints(&testNode0, []v1.Taint{{Key: "nodeKey", Value: "nodeValue", Effect: v1.TaintEffectNoSchedule}}),
					&testNode1,
					&testNode2,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, nodes []v1.Node, err error) {
				require.NoError(t, err)
				require.Len(t, nodes, 2)
				requiredNodeNames := []string{testNode1Name, testNode2Name}
				require.Contains(t, requiredNodeNames, nodes[0].Name)
				require.Contains(t, requiredNodeNames, nodes[1].Name)
			},
		},
		{
			description: "[Success] Should select tainted node if pod can tolerate",
			nodes:       []v1.Node{*withTaints(&testNode0, []v1.Taint{{Key: "nodeKey", Value: "nodeValue", Effect: v1.TaintEffectNoSchedule}})},
			volumes:     []string{testPersistentVolume0Name},
			pods:        []v1.Pod{createPod(testNamespace, testPod0Name, []string{testPersistentVolumeClaim0Name}, withToleration([]v1.Toleration{{Key: "nodeKey", Operator: v1.TolerationOpEqual, Value: "nodeValue"}}))},
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				pod := createPod(testNamespace, testPod0Name, []string{testPersistentVolumeClaim0Name}, withToleration([]v1.Toleration{{Key: "nodeKey", Operator: v1.TolerationOpEqual, Value: "nodeValue"}}))
				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					&pod,
					&testAzVolume0,
					&testPersistentVolume0,
					withTaints(&testNode0, []v1.Taint{{Key: "nodeKey", Value: "nodeValue", Effect: v1.TaintEffectNoSchedule}}),
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, nodes []v1.Node, err error) {
				require.NoError(t, err)
				require.Len(t, nodes, 1)
				require.Equal(t, nodes[0].Name, testNode0Name)
			},
		},
		{
			description: "[Success] Should not select node that does not qualify node affinity label selector",
			nodes:       []v1.Node{*convertToNode(withLabel(&testNode0, map[string]string{"testKey": "testValue"})), testNode1, testNode2},
			volumes:     []string{testPersistentVolume0Name},
			pods: []v1.Pod{createPod(testNamespace, testPod0Name, []string{testPersistentVolumeClaim0Name}, withAffinityRule(v1.Affinity{
				NodeAffinity: &v1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
						NodeSelectorTerms: []v1.NodeSelectorTerm{{
							MatchExpressions: []v1.NodeSelectorRequirement{{
								Key:      "testKey",
								Operator: v1.NodeSelectorOpIn,
								Values:   []string{"testValue"},
							}},
						}},
					}}}))},
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					&testAzVolume0,
					&testPersistentVolume0,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, nodes []v1.Node, err error) {
				require.NoError(t, err)
				require.Len(t, nodes, 1)
				require.Equal(t, nodes[0].Name, testNode0.Name)
			},
		},
		{
			description: "[Success] Should not select node that does not qualify node affinity field selector",
			nodes:       []v1.Node{*convertToNode(withLabel(&testNode0, map[string]string{"testKey": "testValue"})), testNode1, testNode2},
			volumes:     []string{testPersistentVolume0Name},
			pods: []v1.Pod{createPod(testNamespace, testPod0Name, []string{testPersistentVolumeClaim0Name}, withAffinityRule(v1.Affinity{
				NodeAffinity: &v1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
						NodeSelectorTerms: []v1.NodeSelectorTerm{{
							MatchFields: []v1.NodeSelectorRequirement{{
								Key:      "metadata.name",
								Operator: v1.NodeSelectorOpIn,
								Values:   []string{testNode0Name},
							}},
						}},
					}}}))},
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					&testNode0,
					&testAzVolume0,
					&testPersistentVolume0,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, nodes []v1.Node, err error) {
				require.NoError(t, err)
				require.Len(t, nodes, 1)
				require.Equal(t, nodes[0].Name, testNode0.Name)
			},
		},
		{
			description: "[Success] Should not select node that does not qualify inter pod affinity rule",
			nodes: []v1.Node{*convertToNode(withLabel(&testNode0, map[string]string{"nodeKey": "nodeValue"})),
				*convertToNode(withLabel(&testNode1, map[string]string{"nodeKey": "nodeValue"})),
				testNode2},
			volumes: []string{testPersistentVolume0Name},
			pods: []v1.Pod{createPod(testNamespace, testPod0Name, []string{testPersistentVolumeClaim0Name}, withAffinityRule(v1.Affinity{
				PodAffinity: &v1.PodAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: []v1.PodAffinityTerm{{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"podKey": "podValue"},
						},
						TopologyKey: "nodeKey",
					}},
				}}))},
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				pod0 := createPod(testNamespace, testPod0Name, []string{testPersistentVolumeClaim0Name}, withAffinityRule(v1.Affinity{
					PodAffinity: &v1.PodAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: []v1.PodAffinityTerm{{
							LabelSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"podKey": "podValue"},
							},
							TopologyKey: "nodeKey",
						}},
					}}))

				pod1 := createPod(testNamespace, testPod1Name, []string{testPersistentVolumeClaim1Name}, withNode(testNode0Name))
				pod1.Labels = map[string]string{"podKey": "podValue"}

				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					&testAzVolume0,
					&testPersistentVolume0,
					convertToNode(withLabel(&testNode0, map[string]string{"nodeKey": "nodeValue"})),
					convertToNode(withLabel(&testNode1, map[string]string{"nodeKey": "nodeValue"})),
					&testNode2,
					&pod0,
					&pod1,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, nodes []v1.Node, err error) {
				require.NoError(t, err)
				require.Len(t, nodes, 2)
				requiredNodeNames := []string{testNode0.Name, testNode1.Name}
				require.Contains(t, requiredNodeNames, nodes[0].Name)
				require.Contains(t, requiredNodeNames, nodes[1].Name)
			},
		},
		{
			description: "[Success] Should not select node with pod matching affinity requirement but in different namespace",
			nodes: []v1.Node{*convertToNode(withLabel(&testNode0, map[string]string{"nodeKey": "nodeValue"})),
				*convertToNode(withLabel(&testNode1, map[string]string{"nodeKey": "nodeValue"})),
				testNode2},
			volumes: []string{testPersistentVolume0Name},
			pods: []v1.Pod{createPod(testNamespace, testPod0Name, []string{testPersistentVolumeClaim0Name}, withAffinityRule(v1.Affinity{
				PodAffinity: &v1.PodAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: []v1.PodAffinityTerm{{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"podKey": "podValue"},
						},
						TopologyKey: "nodeKey",
					}},
				}}))},
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				pod0 := createPod(testNamespace, testPod0Name, []string{testPersistentVolumeClaim0Name}, withAffinityRule(v1.Affinity{
					PodAffinity: &v1.PodAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: []v1.PodAffinityTerm{{
							LabelSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"podKey": "podValue"},
							},
							TopologyKey: "nodeKey",
						}},
					}}))

				pod1 := createPod("testNamespace-2", testPod1Name, []string{testPersistentVolumeClaim1Name}, withNode(testNode0Name))
				pod1.Labels = map[string]string{"podKey": "podValue"}

				require.NotEqual(t, pod1.Namespace, pod0.Namespace)

				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					&testAzVolume0,
					&testPersistentVolume0,
					convertToNode(withLabel(&testNode0, map[string]string{"nodeKey": "nodeValue"})),
					convertToNode(withLabel(&testNode1, map[string]string{"nodeKey": "nodeValue"})),
					&testNode2,
					&pod0,
					&pod1,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, nodes []v1.Node, err error) {
				require.NoError(t, err)
				require.Len(t, nodes, 0)
			},
		},
		{
			description: "[Success] Should not select node that matches inter pod anti affinity rule",
			nodes: []v1.Node{*convertToNode(withLabel(&testNode0, map[string]string{"nodeKey": "nodeValue"})),
				*convertToNode(withLabel(&testNode1, map[string]string{"nodeKey": "nodeValue"})),
				testNode2},
			volumes: []string{testPersistentVolume0Name},
			pods: []v1.Pod{createPod(testNamespace, testPod0Name, []string{testPersistentVolumeClaim0Name}, withAffinityRule(v1.Affinity{
				PodAntiAffinity: &v1.PodAntiAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: []v1.PodAffinityTerm{{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"podKey": "podValue"},
						},
						TopologyKey: "nodeKey",
					}},
				}}))},
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				pod0 := createPod(testNamespace, testPod0Name, []string{testPersistentVolumeClaim0Name}, withAffinityRule(v1.Affinity{
					PodAntiAffinity: &v1.PodAntiAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: []v1.PodAffinityTerm{{
							LabelSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"podKey": "podValue"},
							},
							TopologyKey: "nodeKey",
						}},
					}}))

				pod1 := createPod(testNamespace, testPod1Name, []string{testPersistentVolumeClaim1Name}, withNode(testNode0Name))
				pod1.Labels = map[string]string{"podKey": "podValue"}

				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					&testAzVolume0,
					&testPersistentVolume0,
					convertToNode(withLabel(&testNode0, map[string]string{"nodeKey": "nodeValue"})),
					convertToNode(withLabel(&testNode1, map[string]string{"nodeKey": "nodeValue"})),
					&testNode2,
					&pod0,
					&pod1,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, nodes []v1.Node, err error) {
				require.NoError(t, err)
				require.Len(t, nodes, 1)
				require.Equal(t, nodes[0].Name, testNode2Name)
			},
		},
		{
			description: "[Success] Should not select node that doesn't qualify for pod node selector rule",
			nodes: []v1.Node{*convertToNode(withLabel(&testNode0, map[string]string{"nodeKey": "nodeValue"})),
				*convertToNode(withLabel(&testNode1, map[string]string{"nodeKey": "nodeValue"})),
				testNode2},
			volumes: []string{testPersistentVolume0Name},
			pods:    []v1.Pod{createPod(testNamespace, testPod0Name, []string{testPersistentVolumeClaim0Name}, withNodeSelector(map[string]string{"nodeKey": "nodeValue"}))},
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					&testAzVolume0,
					&testPersistentVolume0,
					convertToNode(withLabel(&testNode0, map[string]string{"nodeKey": "nodeValue"})),
					convertToNode(withLabel(&testNode1, map[string]string{"nodeKey": "nodeValue"})),
					&testNode2,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, nodes []v1.Node, err error) {
				require.NoError(t, err)
				require.Len(t, nodes, 2)
				requiredNodeNames := []string{testNode0.Name, testNode1.Name}
				require.Contains(t, requiredNodeNames, nodes[0].Name)
				require.Contains(t, requiredNodeNames, nodes[1].Name)
			},
		},
		{
			description: "[Success] Should not select node that doesn't qualify for volume node label selector",
			nodes: []v1.Node{*convertToNode(withLabel(&testNode0, map[string]string{"nodeKey": "nodeValue"})),
				*convertToNode(withLabel(&testNode1, map[string]string{"nodeKey": "nodeValue"})),
				testNode2},
			volumes: []string{testPersistentVolume0Name},
			pods:    []v1.Pod{testPod0},
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				pv := testPersistentVolume0.DeepCopy()
				pv.Spec.NodeAffinity = &v1.VolumeNodeAffinity{
					Required: &v1.NodeSelector{
						NodeSelectorTerms: []v1.NodeSelectorTerm{{
							MatchExpressions: []v1.NodeSelectorRequirement{{
								Key:      "nodeKey",
								Operator: v1.NodeSelectorOpIn,
								Values:   []string{"nodeValue"},
							}},
						}},
					},
				}

				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					&testPod0,
					&testAzVolume0,
					pv,
					convertToNode(withLabel(&testNode0, map[string]string{"nodeKey": "nodeValue"})),
					convertToNode(withLabel(&testNode1, map[string]string{"nodeKey": "nodeValue"})),
					&testNode2,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, nodes []v1.Node, err error) {
				require.NoError(t, err)
				require.Len(t, nodes, 2)
				requiredNodeNames := []string{testNode0.Name, testNode1.Name}
				require.Contains(t, requiredNodeNames, nodes[0].Name)
				require.Contains(t, requiredNodeNames, nodes[1].Name)
			},
		},
		{
			description: "[Success] Should not select node that does not qualify volume node field selector",
			nodes:       []v1.Node{*convertToNode(withLabel(&testNode0, map[string]string{"testKey": "testValue"})), testNode1, testNode2},
			volumes:     []string{testPersistentVolume0Name},
			pods:        []v1.Pod{createPod(testNamespace, testPod0Name, []string{testPersistentVolumeClaim0Name})},
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				pv := testPersistentVolume0.DeepCopy()
				pv.Spec.NodeAffinity = &v1.VolumeNodeAffinity{
					Required: &v1.NodeSelector{
						NodeSelectorTerms: []v1.NodeSelectorTerm{{
							MatchFields: []v1.NodeSelectorRequirement{{
								Key:      "metadata.name",
								Operator: v1.NodeSelectorOpIn,
								Values:   []string{testNode0Name},
							}},
						}},
					},
				}

				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					&testNode0,
					&testAzVolume0,
					pv,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, nodes []v1.Node, err error) {
				require.NoError(t, err)
				require.Len(t, nodes, 1)
				require.Equal(t, nodes[0].Name, testNode0.Name)
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

func TestReplicaAttachmentFailures(t *testing.T) {
	tests := []struct {
		description string
		volumeNames []string
		setupFunc   func(*testing.T, *gomock.Controller) *SharedState
		verifyFunc  func(*testing.T, error)
	}{
		{
			description: "[Success] Volume requesting additional replicas beyond the current capacity should not return an error",
			volumeNames: []string{testPersistentVolume0Name},
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				newVolume0 := testAzVolume0.DeepCopy()
				newVolume0.Status.Detail = &azdiskv1beta2.AzVolumeStatusDetail{
					VolumeID: testManagedDiskURI0,
				}
				newVolume0.Spec.MaxMountReplicaCount = 2

				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					newVolume0,
					&testPersistentVolume0,
					&testNode0,
					&testNode1,
					&testPod0,
					&testReplicaAzVolumeAttachment,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
		},
	}
	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			mockCtl := gomock.NewController(t)
			defer mockCtl.Finish()
			sharedState := tt.setupFunc(t, mockCtl)
			ctx := context.TODO()
			startTime := time.Now()
			for i, volumeName := range tt.volumeNames {
				err := sharedState.manageReplicas(ctx, volumeName)
				tt.verifyFunc(t, err)
				time.Sleep(time.Duration(testEventTTLInSec*i/2) * time.Second)
			}
			time.Sleep(time.Duration(testEventTTLInSec*2)*time.Second - time.Since(startTime)) // wait for at least two event refresh cycles
			// events := s.eventRecorder.(*record.FakeRecorder).Events
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

func TestFilterAndSortNodes(t *testing.T) {
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
			description: "[Success] Should return filtered and sorted list of nodes when AzVolume is valid",
			nodes:       []v1.Node{testNode0, testNode1},
			volumes:     []string{testPersistentVolume0Name, testPersistentVolume1Name},
			pods:        []v1.Pod{testPod0, testPod1},
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				newVolume := testAzVolume0.DeepCopy()
				newVolume.Status.Detail = &azdiskv1beta2.AzVolumeStatusDetail{
					VolumeID: testManagedDiskURI0,
				}

				newVolume1 := testAzVolume1.DeepCopy()
				newVolume1.Status.Detail = &azdiskv1beta2.AzVolumeStatusDetail{
					VolumeID: testManagedDiskURI0,
				}

				testSharedState := NewTestSharedState(
					mockCtl,
					testNamespace,
					newVolume,
					newVolume1,
					&testPersistentVolume0,
					&testPersistentVolume1,
					&testNode0,
					&testNode1,
					&testPod0,
					&testPod1,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, nodes []v1.Node, err error) {
				require.NoError(t, err)
				require.Len(t, nodes, 2)
			},
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			mockCtl := gomock.NewController(t)
			defer mockCtl.Finish()
			sharedState := tt.setupFunc(t, mockCtl)
			nodes, err := sharedState.filterAndSortNodes(context.TODO(), tt.nodes, tt.pods, tt.volumes)
			tt.verifyFunc(t, nodes, err)
		})
	}
}

func TestAddNodeToAvailableAttachmentsMap(t *testing.T) {
	tests := []struct {
		description string
		nodeName    string
		nodeLables  map[string]string
		setupFunc   func(*testing.T, *gomock.Controller) *SharedState
		verifyFunc  func(*testing.T, any, bool)
	}{
		{
			description: "[Success] Should add node to AvailableAttachmentsMap when node is in cache",
			nodeName:    testNode0.Name,
			nodeLables:  testNode0.GetLabels(),
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				testSharedState := NewTestSharedState(mockCtl,
					testNamespace,
					&testNode0,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, remainingCapacity any, nodeExists bool) {
				require.True(t, nodeExists)
				require.NotNil(t, remainingCapacity)
			},
		},
		{
			description: "[Success] Should add node to AvailableAttachmentsMap when node is not found in cache",
			nodeName:    testNode0.Name,
			nodeLables:  testNode0.GetLabels(),
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				testSharedState := NewTestSharedState(mockCtl,
					testNamespace,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, remainingCapacity any, nodeExists bool) {
				require.True(t, nodeExists)
				require.NotNil(t, remainingCapacity)
			},
		},
		{
			description: "[Success] Should only consider attached AzVolumeAttachment when calculating remaining capacity",
			nodeName:    testNode0.Name,
			nodeLables:  testNode0.GetLabels(),
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				testPrimaryAzVolumeAttachment0 := testPrimaryAzVolumeAttachment0
				testPrimaryAzVolumeAttachment1 := testPrimaryAzVolumeAttachment1
				testPrimaryAzVolumeAttachment0.Status.State = azdiskv1beta2.Attached
				testPrimaryAzVolumeAttachment1.Status.State = azdiskv1beta2.Attaching

				testSharedState := NewTestSharedState(mockCtl,
					testNamespace,
					&testNode0,
					&testPrimaryAzVolumeAttachment0,
					&testPrimaryAzVolumeAttachment1,
				)

				mockClients(testSharedState.cachedClient.(*mockclient.MockClient), testSharedState.azClient, testSharedState.kubeClient)
				return testSharedState
			},
			verifyFunc: func(t *testing.T, remainingCapacity any, nodeExists bool) {
				require.True(t, nodeExists)
				require.NotNil(t, remainingCapacity)
				n, err := strconv.ParseInt(testAttachableVolumesValue, 10, 32)
				require.Nil(t, err)
				require.Equal(t, int32(n)-1, remainingCapacity.(*atomic.Int32).Load())
			},
		},
	}
	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			mockCtl := gomock.NewController(t)
			defer mockCtl.Finish()
			sharedState := tt.setupFunc(t, mockCtl)
			sharedState.addNodeToAvailableAttachmentsMap(context.TODO(), tt.nodeName, tt.nodeLables)
			remainingCapacity, nodeExists := sharedState.availableAttachmentsMap.Load(tt.nodeName)
			tt.verifyFunc(t, remainingCapacity, nodeExists)
		})
	}
}

func TestDeleteNodeToAvailableAttachmentsMap(t *testing.T) {
	tests := []struct {
		description string
		nodeName    string
		setupFunc   func(*testing.T, *gomock.Controller) *SharedState
		verifyFunc  func(*testing.T, any, bool)
	}{
		{
			description: "[Success] Should delete node from AvailableAttachmentsMap",
			nodeName:    testNode0.Name,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				testSharedState := NewTestSharedState(mockCtl,
					testNamespace,
					&testNode0,
				)

				return testSharedState
			},
			verifyFunc: func(t *testing.T, remainingCapacity any, nodeExists bool) {
				require.False(t, nodeExists)
				require.Nil(t, remainingCapacity)
			},
		},
	}
	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			mockCtl := gomock.NewController(t)
			defer mockCtl.Finish()
			sharedState := tt.setupFunc(t, mockCtl)
			sharedState.deleteNodeFromAvailableAttachmentsMap(context.TODO(), tt.nodeName)
			remainingCapacity, nodeExists := sharedState.availableAttachmentsMap.Load(tt.nodeName)
			tt.verifyFunc(t, remainingCapacity, nodeExists)
		})
	}
}

func TestDecrementNodeCapacity(t *testing.T) {
	tests := []struct {
		description string
		nodeName    string
		setupFunc   func(*testing.T, *gomock.Controller) *SharedState
		verifyFunc  func(*testing.T, any, bool, bool)
	}{
		{
			description: "[Success] Should decrement for the node's remaining capacity of disk attachment in AvailableAttachmentsMap",
			nodeName:    testNode0.Name,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				testSharedState := NewTestSharedState(mockCtl,
					testNamespace,
					&testNode0,
				)

				addTestNodeInAvailableAttachmentsMap(testSharedState, testNode0.Name, testNodeAvailableAttachmentCount)

				return testSharedState
			},
			verifyFunc: func(t *testing.T, remainingCapacity any, nodeExists bool, isDerementSucceeded bool) {
				require.True(t, nodeExists)
				require.True(t, isDerementSucceeded)
				require.NotNil(t, remainingCapacity)
				require.Equal(t, remainingCapacity.(*atomic.Int32).Load(), int32(7))
			},
		},
		{
			description: "[Failure] Should return false when decrement for the node's remaining capacity of disk attachment in AvailableAttachmentsMap but the node dose not exist",
			nodeName:    testNode0.Name,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				testSharedState := NewTestSharedState(mockCtl,
					testNamespace,
					&testNode0,
				)

				return testSharedState
			},
			verifyFunc: func(t *testing.T, remainingCapacity any, nodeExists bool, isDerementSucceeded bool) {
				require.False(t, nodeExists)
				require.False(t, isDerementSucceeded)
				require.Nil(t, remainingCapacity)
			},
		},
		{
			description: "[Failure] Should return false when decrement for the node's remaining capacity of disk attachment in AvailableAttachmentsMap is 0",
			nodeName:    testNode0.Name,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				testSharedState := NewTestSharedState(mockCtl,
					testNamespace,
					&testNode0,
				)

				addTestNodeInAvailableAttachmentsMap(testSharedState, testNode0.Name, testNodeNoAvailableAttachmentCount)

				return testSharedState
			},
			verifyFunc: func(t *testing.T, remainingCapacity any, nodeExists bool, isDerementSucceeded bool) {
				require.True(t, nodeExists)
				require.False(t, isDerementSucceeded)
				require.NotNil(t, remainingCapacity)
				require.Equal(t, remainingCapacity.(*atomic.Int32).Load(), int32(0))
			},
		},
	}
	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			mockCtl := gomock.NewController(t)
			defer mockCtl.Finish()
			sharedState := tt.setupFunc(t, mockCtl)
			isDerementSucceeded := sharedState.decrementNodeCapacity(context.TODO(), tt.nodeName)
			remainingCapacity, nodeExists := sharedState.availableAttachmentsMap.Load(tt.nodeName)
			tt.verifyFunc(t, remainingCapacity, nodeExists, isDerementSucceeded)
		})
	}
}

func TestIncrementNodeCapacity(t *testing.T) {
	tests := []struct {
		description string
		nodeName    string
		setupFunc   func(*testing.T, *gomock.Controller) *SharedState
		verifyFunc  func(*testing.T, any, bool, bool)
	}{
		{
			description: "[Success] Should increment for the node's remaining capacity of disk attachment in AvailableAttachmentsMap",
			nodeName:    testNode0.Name,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				testSharedState := NewTestSharedState(mockCtl,
					testNamespace,
					&testNode0,
				)

				addTestNodeInAvailableAttachmentsMap(testSharedState, testNode0.Name, testNodeNoAvailableAttachmentCount)

				return testSharedState
			},
			verifyFunc: func(t *testing.T, remainingCapacity any, nodeExists bool, isIncrementSucceeded bool) {
				require.True(t, nodeExists)
				require.True(t, isIncrementSucceeded)
				require.NotNil(t, remainingCapacity)
				require.Equal(t, remainingCapacity.(*atomic.Int32).Load(), int32(1))
			},
		},
		{
			description: "[Failure] Should return false when increment for the node's remaining capacity of disk attachment in AvailableAttachmentsMap but the node dose not exist",
			nodeName:    testNode0.Name,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *SharedState {
				testSharedState := NewTestSharedState(mockCtl,
					testNamespace,
					&testNode0,
				)

				return testSharedState
			},
			verifyFunc: func(t *testing.T, remainingCapacity any, nodeExists bool, isIncrementSucceeded bool) {
				require.False(t, nodeExists)
				require.False(t, isIncrementSucceeded)
				require.Nil(t, remainingCapacity)
			},
		},
	}
	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			mockCtl := gomock.NewController(t)
			defer mockCtl.Finish()
			sharedState := tt.setupFunc(t, mockCtl)
			isIncrementSucceeded := sharedState.incrementNodeCapacity(context.TODO(), tt.nodeName)
			remainingCapacity, nodeExists := sharedState.availableAttachmentsMap.Load(tt.nodeName)
			tt.verifyFunc(t, remainingCapacity, nodeExists, isIncrementSucceeded)
		})
	}
}
