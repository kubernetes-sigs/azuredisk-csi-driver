//go:build azurediskv2
// +build azurediskv2

/*
Copyright 2019 The Kubernetes Authors.

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

package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	crypto "crypto/rand"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	fakeCoreClientSet "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	schedulerapi "k8s.io/kube-scheduler/extender/v1"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	azdiskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
	azdiskinformers "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/informers/externalversions"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

const (
	testDefaultAccessMode = v1.ReadWriteOnce
	testDefaultVMType     = "STANDARD_D2"
)

var (
	handlerFilter          = http.HandlerFunc(handleFilterRequest)
	handlerPrioritize      = http.HandlerFunc(handlePrioritizeRequest)
	KubeConfigFileEnvVar   = "KUBECONFIG"
	validKubeConfigPath    = "valid-Kube-Config-Path"
	validKubeConfigContent = `
	apiVersion: v1
    clusters:
    - cluster:
        server: https://foo-cluster-dns-57e0bda1.hcp.westus2.azmk8s.io:443
      name: foo-cluster
    contexts:
    - context:
        cluster: foo-cluster
        user: clusterUser_foo-rg_foo-cluster
      name: foo-cluster
    current-context: foo-cluster
    kind: Config
    preferences: {}
    users:
    - name: clusterUser_foo-rg_foo-cluster
      user:
`
	noResyncPeriodFunc = func() time.Duration { return 0 }

	computeDiskURIFormat = "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/disks/%s"

	testSubscription  = "12345678-90ab-cedf-1234-567890abcdef"
	testResourceGroup = "test-rg"
)

func TestMain(m *testing.M) {
	existingConfigPath, _ := createConfigFileAndSetEnv(validKubeConfigPath, validKubeConfigContent, KubeConfigFileEnvVar)

	exitVal := m.Run()
	if len(existingConfigPath) > 0 {
		defer cleanConfigAndRestoreEnv(validKubeConfigPath, KubeConfigFileEnvVar, existingConfigPath)
	}
	os.Exit(exitVal)
}

func TestFilterAndPrioritizeRequestResponseCode(t *testing.T) {
	tests := []struct {
		inputArgs interface{}
		want      int
	}{
		{
			inputArgs: &schedulerapi.ExtenderArgs{
				Pod:       &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod"}},
				Nodes:     &v1.NodeList{Items: []v1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node"}}}},
				NodeNames: &[]string{"node"}},
			want: http.StatusOK,
		},
		{
			inputArgs: &schedulerapi.ExtenderArgs{
				Pod:       &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod"}},
				Nodes:     &v1.NodeList{Items: nil},
				NodeNames: &[]string{"node"}},
			want: http.StatusBadRequest,
		},
	}

	// save original client
	savedKubeExtensionClientset := kubeExtensionClientset
	defer func() {
		kubeExtensionClientset = savedKubeExtensionClientset
	}()

	for _, test := range tests {
		// create fake clients
		testClientSet := azdiskfakes.NewSimpleClientset(
			getVolumeAttachment("volumeAttachment", criNamespace, "vol", "node"),
			getDriverNode("driverNode", criNamespace, "node", true),
		)

		// continue with fake client
		kubeExtensionClientset = testClientSet

		response := httptest.NewRecorder()
		requestArgs, err := json.Marshal(test.inputArgs)
		if err != nil {
			t.Fatal("Json encoding failed")
		}

		filterRequest, err := http.NewRequest("POST", filterRequestStr, bytes.NewReader(requestArgs))
		if err != nil {
			t.Fatal(err)
		}

		handlerFilter.ServeHTTP(response, filterRequest)
		if response.Code != test.want {
			t.Errorf("Filter request failed for %s. Got %d want %d", requestArgs, response.Code, test.want)
		}

		prioritizeRequest, err := http.NewRequest("POST", prioritizeRequestStr, bytes.NewReader(requestArgs))
		if err != nil {
			t.Fatal(err)
		}

		handlerPrioritize.ServeHTTP(response, prioritizeRequest)
		if response.Code != test.want {
			t.Errorf("Filter request failed for %s. Got %d want %d", requestArgs, response.Code, test.want)
		}
	}
}

func TestFilterAndPrioritizeResponses(t *testing.T) {
	tests := []struct {
		name                     string
		v1alpha1ClientSet        azdisk.Interface
		kubeClientSet            kubernetes.Interface
		schedulerArgs            schedulerapi.ExtenderArgs
		expectedFilterResult     schedulerapi.ExtenderFilterResult
		expectedPrioritizeResult schedulerapi.HostPriorityList
		expectedPrioritizeOrder  []string
	}{
		{
			name: "Test simple case of one pod/node/volume",
			v1alpha1ClientSet: azdiskfakes.NewSimpleClientset(
				getVolumeAttachment("volumeAttachment", criNamespace, "vol", "node"),
				getDriverNode("driverNode", criNamespace, "node", true),
			),
			kubeClientSet: fakeCoreClientSet.NewSimpleClientset(
				getPV("vol"),
				getPVC("claim", "vol"),
			),
			schedulerArgs: schedulerapi.ExtenderArgs{
				Pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "pod"},
					Spec: v1.PodSpec{
						Volumes: []v1.Volume{
							getVolume("vol", "claim"),
						}}},
				Nodes:     &v1.NodeList{Items: []v1.Node{getNode("node")}},
				NodeNames: &[]string{"node"},
			},
			expectedFilterResult: schedulerapi.ExtenderFilterResult{
				Nodes:       &v1.NodeList{Items: []v1.Node{getNode("node")}},
				NodeNames:   &[]string{"node"},
				FailedNodes: make(map[string]string),
				Error:       "",
			},
			expectedPrioritizeResult: schedulerapi.HostPriorityList{schedulerapi.HostPriority{Host: "node", Score: getNodeScore(1, 0, metav1.Now(), 1, 1, "node")}},
			expectedPrioritizeOrder:  []string{"node"},
		},
		{
			name: "Test simple case of pod/node/volume with pending azDriverNode",
			v1alpha1ClientSet: azdiskfakes.NewSimpleClientset(

				getVolumeAttachment("volumeAttachment", criNamespace, "vol", "node"),
				getDriverNode("driverNode", criNamespace, "node", false),
			),
			kubeClientSet: fakeCoreClientSet.NewSimpleClientset(
				getPV("vol"),
				getPVC("claim", "vol"),
			),
			schedulerArgs: schedulerapi.ExtenderArgs{
				Pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "pod"},
					Spec: v1.PodSpec{
						Volumes: []v1.Volume{
							getVolume("vol", "claim"),
						}}},
				Nodes:     &v1.NodeList{Items: []v1.Node{getNode("node")}},
				NodeNames: &[]string{"node"},
			},
			expectedFilterResult: schedulerapi.ExtenderFilterResult{
				Nodes:       &v1.NodeList{Items: nil},
				NodeNames:   nil,
				FailedNodes: map[string]string{"node": "AzDriverNode for node is not ready."},
				Error:       "",
			},
			expectedPrioritizeResult: schedulerapi.HostPriorityList{schedulerapi.HostPriority{Host: "node", Score: getNodeScore(1, 0, metav1.Now(), 1, 1, "node")}},
			expectedPrioritizeOrder:  []string{"node"},
		},
		{
			name: "Test simple case of single node/volume with no pod volume requests",
			v1alpha1ClientSet: azdiskfakes.NewSimpleClientset(
				getVolumeAttachment("volumeAttachment", criNamespace, "vol", "node"),
				getDriverNode("driverNode", criNamespace, "node", true),
			),
			kubeClientSet: fakeCoreClientSet.NewSimpleClientset(
				getPV("vol"),
				getPVC("claim", "vol"),
			),
			schedulerArgs: schedulerapi.ExtenderArgs{
				Pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "pod"}},
				Nodes:     &v1.NodeList{Items: []v1.Node{getNode("node")}},
				NodeNames: &[]string{"node"},
			},
			expectedFilterResult: schedulerapi.ExtenderFilterResult{
				Nodes:       &v1.NodeList{Items: []v1.Node{getNode("node")}},
				NodeNames:   &[]string{"node"},
				FailedNodes: nil,
				Error:       "",
			},
			expectedPrioritizeResult: schedulerapi.HostPriorityList{schedulerapi.HostPriority{Host: "node", Score: getNodeScore(0, 0, metav1.Now(), 1, 1, "node")}},
			expectedPrioritizeOrder:  []string{"node"},
		},
		{
			name: "Test case with 2 nodes and one pod/volume",
			v1alpha1ClientSet: azdiskfakes.NewSimpleClientset(
				getVolumeAttachment("volumeAttachment", criNamespace, "vol", "node0"),
				getDriverNode("driverNode0", criNamespace, "node0", true),
				getDriverNode("driverNode1", criNamespace, "node1", true),
			),
			kubeClientSet: fakeCoreClientSet.NewSimpleClientset(
				getPV("vol"),
				getPVC("claim", "vol"),
			),
			schedulerArgs: schedulerapi.ExtenderArgs{
				Pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "pod"},
					Spec: v1.PodSpec{
						Volumes: []v1.Volume{
							getVolume("vol", "claim"),
						},
					},
				},
				Nodes: &v1.NodeList{
					Items: []v1.Node{
						getNode("node0"),
						getNode("node1"),
					},
				},
				NodeNames: &[]string{"node0, node1"},
			},
			expectedFilterResult: schedulerapi.ExtenderFilterResult{
				Nodes: &v1.NodeList{
					Items: []v1.Node{
						getNode("node0"),
						getNode("node1"),
					},
				},
				NodeNames:   &[]string{"node0", "node1"},
				FailedNodes: make(map[string]string),
				Error:       "",
			},
			expectedPrioritizeResult: schedulerapi.HostPriorityList{
				schedulerapi.HostPriority{Host: "node0", Score: getNodeScore(1, 0, metav1.Now(), 2, 1, "node0")},
				schedulerapi.HostPriority{Host: "node1", Score: getNodeScore(0, 0, metav1.Now(), 2, 0, "node1")},
			},
			expectedPrioritizeOrder: []string{"node0", "node1"},
		},
		{
			name: "Test case with 1 ready and 1 pending nodes and one pod/volume",
			v1alpha1ClientSet: azdiskfakes.NewSimpleClientset(
				getVolumeAttachment("volumeAttachment", criNamespace, "vol", "node1"),
				getDriverNode("driverNode0", criNamespace, "node0", false),
				getDriverNode("driverNode1", criNamespace, "node1", true),
			),
			kubeClientSet: fakeCoreClientSet.NewSimpleClientset(
				getPV("vol"),
				getPVC("claim", "vol"),
			),
			schedulerArgs: schedulerapi.ExtenderArgs{
				Pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "pod"},
					Spec: v1.PodSpec{
						Volumes: []v1.Volume{
							getVolume("vol", "claim"),
						},
					},
				},
				Nodes: &v1.NodeList{
					Items: []v1.Node{
						getNode("node0"),
						getNode("node1"),
					},
				},
				NodeNames: &[]string{"node0, node1"},
			},
			expectedFilterResult: schedulerapi.ExtenderFilterResult{
				Nodes: &v1.NodeList{
					Items: []v1.Node{
						getNode("node1"),
					},
				},
				NodeNames:   &[]string{"node1"},
				FailedNodes: map[string]string{"node0": "AzDriverNode for node0 is not ready."},
				Error:       "",
			},
			expectedPrioritizeResult: schedulerapi.HostPriorityList{
				schedulerapi.HostPriority{Host: "node0", Score: getNodeScore(0, 0, metav1.Now(), 2, 0, "node0")},
				schedulerapi.HostPriority{Host: "node1", Score: getNodeScore(1, 0, metav1.Now(), 2, 1, "node1")},
			},
			expectedPrioritizeOrder: []string{"node1", "node0"},
		},
		{
			name: "Test case with 2 nodes where 2 requested volumes are attached to 1 node",
			v1alpha1ClientSet: azdiskfakes.NewSimpleClientset(
				getVolumeAttachment("volumeAttachment0", criNamespace, "vol0", "node0"),
				getVolumeAttachment("volumeAttachment1", criNamespace, "vol1", "node0"),
				getDriverNode("driverNode0", criNamespace, "node0", true),
				getDriverNode("driverNode1", criNamespace, "node1", true),
			),
			kubeClientSet: fakeCoreClientSet.NewSimpleClientset(
				getPV("vol0"),
				getPVC("claim0", "vol0"),
				getPV("vol1"),
				getPVC("claim1", "vol1"),
			),
			schedulerArgs: schedulerapi.ExtenderArgs{
				Pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "pod"},
					Spec: v1.PodSpec{
						Volumes: []v1.Volume{
							getVolume("vol0", "claim0"),
							getVolume("vol1", "claim1"),
						},
					},
				},
				Nodes: &v1.NodeList{
					Items: []v1.Node{
						getNode("node0"),
						getNode("node1"),
					},
				},
				NodeNames: &[]string{"node0, node1"},
			},
			expectedFilterResult: schedulerapi.ExtenderFilterResult{
				Nodes: &v1.NodeList{
					Items: []v1.Node{
						getNode("node0"),
						getNode("node1"),
					},
				},
				NodeNames:   &[]string{"node0", "node1"},
				FailedNodes: make(map[string]string),
				Error:       "",
			},
			expectedPrioritizeResult: schedulerapi.HostPriorityList{
				schedulerapi.HostPriority{Host: "node0", Score: getNodeScore(2, 0, metav1.Now(), 2, 2, "node0")},
				schedulerapi.HostPriority{Host: "node1", Score: getNodeScore(0, 0, metav1.Now(), 2, 0, "node1")},
			},
			expectedPrioritizeOrder: []string{"node0", "node1"},
		},
		{
			name: "Test case with 3 nodes where 2, 3, 1 requested volume are attached to node 0, 1, 2",
			v1alpha1ClientSet: azdiskfakes.NewSimpleClientset(
				getVolumeAttachment("volumeAttachment0", criNamespace, "vol0", "node0"),
				getVolumeAttachment("volumeAttachment1", criNamespace, "vol1", "node0"),
				getVolumeAttachment("volumeAttachment2", criNamespace, "vol0", "node1"),
				getVolumeAttachment("volumeAttachment3", criNamespace, "vol1", "node1"),
				getVolumeAttachment("volumeAttachment4", criNamespace, "vol2", "node1"),
				getVolumeAttachment("volumeAttachment5", criNamespace, "vol2", "node2"),
				getDriverNode("driverNode0", criNamespace, "node0", true),
				getDriverNode("driverNode1", criNamespace, "node1", true),
				getDriverNode("driverNode2", criNamespace, "node2", true),
			),
			kubeClientSet: fakeCoreClientSet.NewSimpleClientset(
				getPV("vol0"),
				getPVC("claim0", "vol0"),
				getPV("vol1"),
				getPVC("claim1", "vol1"),
				getPV("vol2"),
				getPVC("claim2", "vol2"),
			),
			schedulerArgs: schedulerapi.ExtenderArgs{
				Pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "pod"},
					Spec: v1.PodSpec{
						Volumes: []v1.Volume{
							getVolume("vol0", "claim0"),
							getVolume("vol1", "claim1"),
							getVolume("vol2", "claim2")}}},
				Nodes: &v1.NodeList{
					Items: []v1.Node{
						getNode("node0"),
						getNode("node1"),
						getNode("node2")}},
				NodeNames: &[]string{"node0", "node1", "node2"},
			},
			expectedFilterResult: schedulerapi.ExtenderFilterResult{
				Nodes: &v1.NodeList{
					Items: []v1.Node{
						getNode("node0"),
						getNode("node1"),
						getNode("node2")}},
				NodeNames:   &[]string{"node0", "node1", "node2"},
				FailedNodes: make(map[string]string),
				Error:       "",
			},
			expectedPrioritizeResult: schedulerapi.HostPriorityList{
				schedulerapi.HostPriority{Host: "node0", Score: getNodeScore(2, 0, metav1.Now(), 3, 2, "node0")},
				schedulerapi.HostPriority{Host: "node1", Score: getNodeScore(3, 0, metav1.Now(), 3, 3, "node1")},
				schedulerapi.HostPriority{Host: "node2", Score: getNodeScore(1, 0, metav1.Now(), 3, 1, "node2")},
			},
			expectedPrioritizeOrder: []string{"node1", "node0", "node2"},
		},
		{
			name: "Test case with 3 nodes where 2, 3 requested volumes and 1 unrequested volume are attached to node 0, 1, 2",
			v1alpha1ClientSet: azdiskfakes.NewSimpleClientset(
				getVolumeAttachment("volumeAttachment0", criNamespace, "vol0", "node0"),
				getVolumeAttachment("volumeAttachment1", criNamespace, "vol1", "node0"),
				getVolumeAttachment("volumeAttachment2", criNamespace, "vol0", "node1"),
				getVolumeAttachment("volumeAttachment3", criNamespace, "vol1", "node1"),
				getVolumeAttachment("volumeAttachment4", criNamespace, "vol2", "node1"),
				getVolumeAttachment("volumeAttachment5", criNamespace, "vol3", "node2"),
				getDriverNode("driverNode0", criNamespace, "node0", true),
				getDriverNode("driverNode1", criNamespace, "node1", true),
				getDriverNode("driverNode2", criNamespace, "node2", true),
			),
			kubeClientSet: fakeCoreClientSet.NewSimpleClientset(
				getPV("vol0"),
				getPVC("claim0", "vol0"),
				getPV("vol1"),
				getPVC("claim1", "vol1"),
				getPV("vol2"),
				getPVC("claim2", "vol2"),
				getPV("vol3"),
				getPVC("claim3", "vol3"),
			),
			schedulerArgs: schedulerapi.ExtenderArgs{
				Pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "pod"},
					Spec: v1.PodSpec{
						Volumes: []v1.Volume{
							getVolume("vol0", "claim0"),
							getVolume("vol1", "claim1"),
							getVolume("vol2", "claim2")}}},
				Nodes: &v1.NodeList{
					Items: []v1.Node{
						getNode("node0"),
						getNode("node1"),
						getNode("node2")}},
				NodeNames: &[]string{"node0", "node1", "node2"},
			},
			expectedFilterResult: schedulerapi.ExtenderFilterResult{
				Nodes: &v1.NodeList{
					Items: []v1.Node{
						getNode("node0"),
						getNode("node1"),
						getNode("node2")}},
				NodeNames:   &[]string{"node0", "node1", "node2"},
				FailedNodes: make(map[string]string),
				Error:       "",
			},
			expectedPrioritizeResult: schedulerapi.HostPriorityList{
				schedulerapi.HostPriority{Host: "node0", Score: getNodeScore(2, 0, metav1.Now(), 3, 2, "node0")},
				schedulerapi.HostPriority{Host: "node1", Score: getNodeScore(3, 0, metav1.Now(), 3, 3, "node1")},
				schedulerapi.HostPriority{Host: "node2", Score: getNodeScore(0, 0, metav1.Now(), 3, 1, "node2")},
			},
			expectedPrioritizeOrder: []string{"node1", "node0", "node2"},
		},
		{
			name: "Test case with 3 nodes where 2 requested volumes are attached to node 0, 1 and 1 unrequested volume is attached to node 1, 2",
			v1alpha1ClientSet: azdiskfakes.NewSimpleClientset(
				getVolumeAttachment("volumeAttachment0", criNamespace, "vol0", "node0"),
				getVolumeAttachment("volumeAttachment1", criNamespace, "vol1", "node0"),
				getVolumeAttachment("volumeAttachment2", criNamespace, "vol0", "node1"),
				getVolumeAttachment("volumeAttachment3", criNamespace, "vol1", "node1"),
				getVolumeAttachment("volumeAttachment4", criNamespace, "vol2", "node1"),
				getVolumeAttachment("volumeAttachment5", criNamespace, "vol2", "node2"),
				getDriverNode("driverNode0", criNamespace, "node0", true),
				getDriverNode("driverNode1", criNamespace, "node1", true),
				getDriverNode("driverNode2", criNamespace, "node2", true),
			),
			kubeClientSet: fakeCoreClientSet.NewSimpleClientset(
				getPV("vol0"),
				getPVC("claim0", "vol0"),
				getPV("vol1"),
				getPVC("claim1", "vol1"),
				getPV("vol2"),
				getPVC("claim2", "vol2"),
			),
			schedulerArgs: schedulerapi.ExtenderArgs{
				Pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "pod"},
					Spec: v1.PodSpec{
						Volumes: []v1.Volume{
							getVolume("vol0", "claim0"),
							getVolume("vol1", "claim1")}}},
				Nodes: &v1.NodeList{
					Items: []v1.Node{
						getNode("node0"),
						getNode("node1"),
						getNode("node2")}},
				NodeNames: &[]string{"node0", "node1", "node2"},
			},
			expectedFilterResult: schedulerapi.ExtenderFilterResult{
				Nodes: &v1.NodeList{
					Items: []v1.Node{
						getNode("node0"),
						getNode("node1"),
						getNode("node2")}},
				NodeNames:   &[]string{"node0", "node1", "node2"},
				FailedNodes: make(map[string]string),
				Error:       "",
			},
			expectedPrioritizeResult: schedulerapi.HostPriorityList{
				schedulerapi.HostPriority{Host: "node0", Score: getNodeScore(2, 0, metav1.Now(), 3, 2, "node0")},
				schedulerapi.HostPriority{Host: "node1", Score: getNodeScore(2, 0, metav1.Now(), 3, 3, "node1")},
				schedulerapi.HostPriority{Host: "node2", Score: getNodeScore(0, 0, metav1.Now(), 3, 1, "node2")},
			},
			expectedPrioritizeOrder: []string{"node0", "node1", "node2"},
		},
	}

	//save original client
	savedKubeExtensionClientset := kubeExtensionClientset
	savedKubeClientset := kubeClientset
	defer func() {
		kubeExtensionClientset = savedKubeExtensionClientset
		kubeClientset = savedKubeClientset
	}()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// continue with fake clients
			kubeExtensionClientset = test.v1alpha1ClientSet
			kubeClientset = test.kubeClientSet
			setupTestInformers(kubeExtensionClientset, kubeClientset)

			// encode scheduler arguments
			requestArgs, err := json.Marshal(&test.schedulerArgs)
			if err != nil {
				t.Fatal("Json encoding failed")
			}

			// check filter result
			filterRequest, err := http.NewRequest("POST", filterRequestStr, bytes.NewReader(requestArgs))
			if err != nil {
				t.Fatal(err)
			}

			filterResultRecorder := httptest.NewRecorder()
			handlerFilter.ServeHTTP(filterResultRecorder, filterRequest)

			decoder := json.NewDecoder(filterResultRecorder.Body)
			var actualFilterResult schedulerapi.ExtenderFilterResult
			if err := decoder.Decode(&actualFilterResult); err != nil {
				klog.Errorf("handleFilterRequest: Error decoding filter request: %v", err)
				t.Fatal(err)
			}

			if !gotExpectedFilterResults(actualFilterResult, test.expectedFilterResult) {
				t.Errorf("Actual filter response (%v) does not equal expected response (%v).", actualFilterResult, test.expectedFilterResult)
			}

			// check prioritize result
			prioritizeRequest, err := http.NewRequest("POST", prioritizeRequestStr, bytes.NewReader(requestArgs))
			if err != nil {
				t.Fatal(err)
			}

			prioritizeResultRecorder := httptest.NewRecorder()
			handlerPrioritize.ServeHTTP(prioritizeResultRecorder, prioritizeRequest)

			decoder = json.NewDecoder(prioritizeResultRecorder.Body)
			var actualPrioritizeList schedulerapi.HostPriorityList
			if err := decoder.Decode(&actualPrioritizeList); err != nil {
				klog.Errorf("handlePrioritizeRequest: Error decoding filter request: %v", err)
				t.Fatal(err)
			}

			if !gotExpectedPrioritizeList(actualPrioritizeList, test.expectedPrioritizeResult, test.expectedPrioritizeOrder) {
				klog.Infof("actual: %+v,\nwanted: %+v", actualPrioritizeList, test.expectedPrioritizeResult)
				t.Errorf("Actual prioritize response (%s) does not equal expected response.", prioritizeResultRecorder.Body)
			}
		})
	}
}

//TODO test only checks the response code. add check for response body
func TestFilterAndPrioritizeInRandomizedLargeCluster(t *testing.T) {
	var nodes []v1.Node
	var nodeNames []string

	//TODO increase numberOfPodsToSchedule when changing the implementation to reuse goroutines
	stressTestSetupParams := map[string]struct {
		numberOfNodes   int
		numberOfVolumes int
		numberOfPods    int
	}{
		"low":  {50, 500, 10},
		"avg":  {100, 1000, 10},
		"high": {500, 3000, 10},
	}

	//save original client
	savedKubeExtensionClientset := kubeExtensionClientset
	savedKubeClientset := kubeClientset
	defer func() {
		kubeExtensionClientset = savedKubeExtensionClientset
		kubeClientset = savedKubeClientset
	}()

	for _, setupParams := range stressTestSetupParams {
		t.Run("Stress test", func(t *testing.T) {
			var tokens = make(chan struct{}, 20)
			var wg sync.WaitGroup
			var v1alpha1Resources []runtime.Object
			var coreResources []runtime.Object
			numberOfClusterNodes := setupParams.numberOfNodes
			numberOfClusterVolumes := setupParams.numberOfVolumes
			numberOfPodsToSchedule := setupParams.numberOfPods

			// generate large number of nodes
			for i := 0; i < numberOfClusterNodes; i++ {
				nodeName := fmt.Sprintf("node%d", i)
				nodes = append(nodes, getNode(nodeName))
				nodeNames = append(nodeNames, nodeName)
				v1alpha1Resources = append(v1alpha1Resources, getDriverNode(fmt.Sprintf("driverNode%d", i), criNamespace, nodeName, true))
			}

			// generate volumes and assign to nodes
			for i := 0; i < numberOfClusterVolumes; i++ {
				volumeName := fmt.Sprintf("vol%d", i)
				v1alpha1Resources = append(v1alpha1Resources, getVolumeAttachment(fmt.Sprintf("volumeAttachment%d", i), criNamespace, volumeName, fmt.Sprintf("node%d", rand.Intn(5000))))
				coreResources = append(coreResources, getPV(volumeName), getPVC(fmt.Sprintf("claim%d", i), volumeName))
			}

			v1alpha1ClientSet := azdiskfakes.NewSimpleClientset(
				v1alpha1Resources...,
			)
			coreClientSet := fakeCoreClientSet.NewSimpleClientset(
				coreResources...,
			)

			// continue with fake clients
			kubeExtensionClientset = v1alpha1ClientSet
			kubeClientset = coreClientSet
			setupTestInformers(kubeExtensionClientset, kubeClientset)

			var errorChan = make(chan error, numberOfPodsToSchedule)
			for j := 0; j < numberOfPodsToSchedule; j++ {
				wg.Add(1)
				go func() {
					defer wg.Done()

					var testPodVolumes []v1.Volume
					seed := make([]byte, 8)
					_, err := crypto.Read(seed)
					if err != nil {
						errorChan <- fmt.Errorf("Generating rand seed for podCount failed: %v ", err)
						return
					}
					rng := rand.New(rand.NewSource(int64(binary.LittleEndian.Uint64(seed[:]))))

					// randomly assign volumes to pod
					podVolCount := rng.Intn(256)
					for i := 0; i < podVolCount; i++ {
						volNum := rand.Intn(numberOfClusterNodes)
						testPodVolumes = append(testPodVolumes, getVolume(fmt.Sprintf("vol%d", volNum), fmt.Sprintf("claim%d", volNum)))
					}

					testPod := &v1.Pod{
						ObjectMeta: metav1.ObjectMeta{Name: "pod"},
						Spec: v1.PodSpec{
							Volumes: testPodVolumes}}

					schedulerArgs := schedulerapi.ExtenderArgs{
						Pod:       testPod,
						Nodes:     &v1.NodeList{Items: nodes},
						NodeNames: &nodeNames,
					}

					responseFilter := httptest.NewRecorder()
					responsePrioritize := httptest.NewRecorder()
					requestArgs, err := json.Marshal(schedulerArgs)
					if err != nil {
						errorChan <- fmt.Errorf("Json encoding failed")
						return
					}

					tokens <- struct{}{} // acquire a token
					filterRequest, err := http.NewRequest("POST", filterRequestStr, bytes.NewReader(requestArgs))
					if err != nil {
						errorChan <- err
						return
					}

					handlerFilter.ServeHTTP(responseFilter, filterRequest)
					if responseFilter.Code != 200 {
						errorChan <- fmt.Errorf("Filter request failed for %s. Got %d want %d", requestArgs, responseFilter.Code, 200)
						return
					}

					prioritizeRequest, err := http.NewRequest("POST", prioritizeRequestStr, bytes.NewReader(requestArgs))
					if err != nil {
						errorChan <- err
						return
					}

					handlerPrioritize.ServeHTTP(responsePrioritize, prioritizeRequest)
					if responsePrioritize.Code != 200 {
						errorChan <- fmt.Errorf("Filter request failed for %s. Got %d want %d", requestArgs, responsePrioritize.Code, 200)
						return
					}

					decoder := json.NewDecoder(responseFilter.Body)
					var filterResult schedulerapi.ExtenderFilterResult
					if err := decoder.Decode(&filterResult); err != nil {
						errorChan <- fmt.Errorf("handleFilterRequest: Error decoding filter request: %v", err)
						return
					}

					decoder = json.NewDecoder(responsePrioritize.Body)
					var prioritizeList schedulerapi.HostPriorityList
					if err := decoder.Decode(&prioritizeList); err != nil {
						errorChan <- fmt.Errorf("handlePrioritizeRequest: Error decoding filter request: %v", err)
						return
					}
					errorChan <- nil
					<-tokens //release the token
				}()
			}

			go func() {
				wg.Wait()
				close(errorChan)
				close(tokens)
			}()

			j := 0
			for err := range errorChan {
				if err != nil {
					klog.Errorf("Error during stress test: %v ", err)
					t.Fatal(err)
				}
				j++
				if j > numberOfPodsToSchedule { // TODO remove. Helps with debugging otherwise unnecessary
					klog.Info("Test ran successfully.")
					break
				}
			}
		})
	}
}

func gotExpectedFilterResults(got, want schedulerapi.ExtenderFilterResult) bool {
	return reflect.DeepEqual(got, want)
}

func gotExpectedPrioritizeList(got, want schedulerapi.HostPriorityList, order []string) bool {
	sort.Slice(got[:], func(i, j int) bool {
		return got[i].Score > got[j].Score
	})
	sort.Slice(want[:], func(i, j int) bool {
		return want[i].Score > want[j].Score
	})

	for i := range got {
		if got[i].Host != want[i].Host || got[i].Host != order[i] {
			return false
		}
		// if got[i].Score > want[i].Score { //TODO add logic to validate score
		// 	return false
		// }
	}

	return true
}

func getVolumeAttachment(attachmentName, ns, volumeName, nodeName string) *azdiskv1beta2.AzVolumeAttachment {
	return &azdiskv1beta2.AzVolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      attachmentName,
			Namespace: ns,
			Labels:    map[string]string{consts.NodeNameLabel: nodeName},
		},
		Spec: azdiskv1beta2.AzVolumeAttachmentSpec{
			VolumeName:    volumeName,
			NodeName:      nodeName,
			RequestedRole: azdiskv1beta2.PrimaryRole,
		},
		Status: azdiskv1beta2.AzVolumeAttachmentStatus{
			Detail: &azdiskv1beta2.AzVolumeAttachmentStatusDetail{
				Role: azdiskv1beta2.PrimaryRole,
			},
			State: azdiskv1beta2.Attached,
		},
	}
}

func getDriverNode(driverNodeName, ns, nodeName string, ready bool) *azdiskv1beta2.AzDriverNode {
	heartbeat := metav1.Now()
	return &azdiskv1beta2.AzDriverNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      driverNodeName,
			Namespace: ns,
		},
		Spec: azdiskv1beta2.AzDriverNodeSpec{
			NodeName: nodeName,
		},
		Status: &azdiskv1beta2.AzDriverNodeStatus{
			ReadyForVolumeAllocation: &ready,
			LastHeartbeatTime:        &heartbeat,
		},
	}

}

func getTestDiskURI(pvName string) string {
	return fmt.Sprintf(computeDiskURIFormat, testSubscription, testResourceGroup, pvName)
}

func getVolume(volumeName, claimName string) v1.Volume {
	return v1.Volume{
		Name: volumeName,
		VolumeSource: v1.VolumeSource{
			PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
				ClaimName: claimName,
			},
		},
	}
}

func getNode(nodeName string) v1.Node {
	return v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
			Labels: map[string]string{
				v1.LabelInstanceTypeStable: testDefaultVMType,
			},
		},
	}
}

func getPV(volumeName string) *v1.PersistentVolume {
	return &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: volumeName,
		},
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeSource: v1.PersistentVolumeSource{
				CSI: &v1.CSIPersistentVolumeSource{
					Driver:       consts.DefaultDriverName,
					VolumeHandle: getTestDiskURI(volumeName),
				},
			},
		},
	}
}

func getPVC(claimName, volumeName string) *v1.PersistentVolumeClaim {
	return &v1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: claimName,
		},
		Spec: v1.PersistentVolumeClaimSpec{
			VolumeName:  volumeName,
			AccessModes: []v1.PersistentVolumeAccessMode{testDefaultAccessMode},
		},
	}
}

func createConfigFileAndSetEnv(path string, content string, envVariableName string) (string, error) {
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if err != nil {
		return "", err
	}

	if err := ioutil.WriteFile(path, []byte(content), 0666); err != nil {
		return "", err
	}

	envValue, _ := os.LookupEnv(envVariableName)
	err = os.Setenv(envVariableName, path)
	if err != nil {
		return "", fmt.Errorf("Failed to set env variable")
	}

	return envValue, err
}

func cleanConfigAndRestoreEnv(path string, envVariableName string, envValue string) {
	defer os.Setenv(envVariableName, envValue)
	os.Remove(path)
}

func setupTestInformers(versionedInterface azdisk.Interface, coreInterface kubernetes.Interface) {
	v1alpah1InformerFactory := azdiskinformers.NewSharedInformerFactory(kubeExtensionClientset, noResyncPeriodFunc())
	coreInformerFactory := informers.NewSharedInformerFactory(coreInterface, noResyncPeriodFunc())

	azVolumeAttachmentInformer = v1alpah1InformerFactory.Disk().V1beta2().AzVolumeAttachments()
	azDriverNodeInformer = v1alpah1InformerFactory.Disk().V1beta2().AzDriverNodes()
	pvInformer = coreInformerFactory.Core().V1().PersistentVolumes()
	pvcInformer = coreInformerFactory.Core().V1().PersistentVolumeClaims()

	stopper := make(chan struct{})
	defer close(stopper)
	go azVolumeAttachmentInformer.Informer().Run(stopper)
	go azDriverNodeInformer.Informer().Run(stopper)
	go pvInformer.Informer().Run(stopper)
	go pvcInformer.Informer().Run(stopper)
	cache.WaitForCacheSync(stopper, azVolumeAttachmentInformer.Informer().HasSynced, azDriverNodeInformer.Informer().HasSynced, pvInformer.Informer().HasSynced, pvcInformer.Informer().HasSynced)
	populateCache()
}

func populateCache() error {
	pvcList, err := pvcInformer.Lister().PersistentVolumeClaims(v1.NamespaceAll).List(labels.Everything())
	if err != nil {
		klog.Errorf("failed to get pvc list")
		return err
	}
	for _, pvc := range pvcList {
		addPvcEntry(pvc)
	}

	pvList, err := pvInformer.Lister().List(labels.Everything())
	if err != nil {
		klog.Errorf("failed to get pv list")
		return err
	}
	for _, pv := range pvList {
		addPvEntry(pv)
	}

	azVolAtt, err := azVolumeAttachmentInformer.Lister().List(labels.Everything())
	if err != nil {
		klog.Errorf("failed to get azvolatt list")
		return err
	}
	for _, att := range azVolAtt {
		onAzVolAttAdd(att)
	}

	return nil
}
