/*
Copyright 2022 The Kubernetes Authors.

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

package cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

var azvaResourceAll1 AzvaResourceAll = AzvaResourceAll{
	PodName:     "test-pod-0",
	NodeName:    "test-node-0",
	ZoneName:    "eastus-0",
	Namespace:   consts.DefaultAzureDiskCrdNamespace,
	Name:        "test-azVolumeAttachment-0",
	Age:         metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
	RequestRole: "Primary",
	Role:        "Primary",
	State:       "Attached",
}

var azvaResourceAll2 AzvaResourceAll = AzvaResourceAll{
	PodName:     "test-pod-1",
	NodeName:    "test-node-0",
	ZoneName:    "eastus-0",
	Namespace:   consts.DefaultAzureDiskCrdNamespace,
	Name:        "test-azVolumeAttachment-0",
	Age:         metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
	RequestRole: "Primary",
	Role:        "Primary",
	State:       "Attached",
}

var azvaResourceAll3 AzvaResourceAll = AzvaResourceAll{
	PodName:     "test-pod-0",
	NodeName:    "test-node-1",
	ZoneName:    "eastus-1",
	Namespace:   consts.DefaultAzureDiskCrdNamespace,
	Name:        "test-azVolumeAttachment-1",
	Age:         metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
	RequestRole: "Replica",
	Role:        "Replica",
	State:       "Attached",
}

var azvaResource_pod1 AzvaResource = AzvaResource{
	ResourceType: "test-pod-0",
	Namespace:    consts.DefaultAzureDiskCrdNamespace,
	Name:         "test-azVolumeAttachment-0",
	Age:          metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
	RequestRole:  "Primary",
	Role:         "Primary",
	State:        "Attached",
}

var azvaResource_pod2 AzvaResource = AzvaResource{
	ResourceType: "test-pod-0",
	Namespace:    consts.DefaultAzureDiskCrdNamespace,
	Name:         "test-azVolumeAttachment-1",
	Age:          metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
	RequestRole:  "Replica",
	Role:         "Replica",
	State:        "Attached",
}

var azvaResource_node1 AzvaResource = AzvaResource{
	ResourceType: "test-node-0",
	Namespace:    consts.DefaultAzureDiskCrdNamespace,
	Name:         "test-azVolumeAttachment-0",
	Age:          metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
	RequestRole:  "Primary",
	Role:         "Primary",
	State:        "Attached",
}

var azvaResource_node2 AzvaResource = AzvaResource{
	ResourceType: "test-node-1",
	Namespace:    consts.DefaultAzureDiskCrdNamespace,
	Name:         "test-azVolumeAttachment-1",
	Age:          metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
	RequestRole:  "Replica",
	Role:         "Replica",
	State:        "Attached",
}

var azvaResource_node3 AzvaResource = AzvaResource{
	ResourceType: "test-node-1",
	Namespace:    consts.DefaultAzureDiskCrdNamespace,
	Name:         "test-azVolumeAttachment-2",
	Age:          metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
	RequestRole:  "Primary",
	Role:         "Primary",
	State:        "Attached",
}

var azvaResource_zone1 AzvaResource = AzvaResource{
	ResourceType: "eastus-0",
	Namespace:    consts.DefaultAzureDiskCrdNamespace,
	Name:         "test-azVolumeAttachment-0",
	Age:          metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
	RequestRole:  "Primary",
	Role:         "Primary",
	State:        "Attached",
}

var azvaResource_zone2 AzvaResource = AzvaResource{
	ResourceType: "eastus-1",
	Namespace:    consts.DefaultAzureDiskCrdNamespace,
	Name:         "test-azVolumeAttachment-1",
	Age:          metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
	RequestRole:  "Replica",
	Role:         "Replica",
	State:        "Attached",
}

var azvaResource_zone3 AzvaResource = AzvaResource{
	ResourceType: "eastus-1",
	Namespace:    consts.DefaultAzureDiskCrdNamespace,
	Name:         "test-azVolumeAttachment-2",
	Age:          metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
	RequestRole:  "Primary",
	Role:         "Primary",
	State:        "Attached",
}

func TestGetAllAzVolumeAttachements(t *testing.T) {
	fakeClientsetK8s := NewTestK8sClientset()
	fakeClientsetAzDisk := NewTestAzDiskClientset()

	tests := []struct {
		description string
		verifyFunc  func()
	}{
		{
			description: "specified namespace",
			verifyFunc: func() {
				result := GetAllAzVolumeAttachements(fakeClientsetK8s, fakeClientsetAzDisk, "default")
				expect := []AzvaResourceAll{azvaResourceAll1, azvaResourceAll2, azvaResourceAll3}

				for i := 0; i < len(result); i++ {
					require.Equal(t, result[i].PodName, expect[i].PodName)
					require.Equal(t, result[i].NodeName, expect[i].NodeName)
					require.Equal(t, result[i].ZoneName, expect[i].ZoneName)
					require.Equal(t, result[i].Namespace, expect[i].Namespace)
					require.Equal(t, result[i].Name, expect[i].Name)
					require.Equal(t, int(result[i].Age / time.Second), int(expect[i].Age / time.Second))
					require.Equal(t, result[i].RequestRole, expect[i].RequestRole)
					require.Equal(t, result[i].Role, expect[i].Role)
				}
			},
		},
		{
			description: "empty namespace",
			verifyFunc: func() {
				result := GetAllAzVolumeAttachements(fakeClientsetK8s, fakeClientsetAzDisk, "")
				expect := []AzvaResourceAll{azvaResourceAll1, azvaResourceAll2, azvaResourceAll3}

				for i := 0; i < len(result); i++ {
					require.Equal(t, result[i].PodName, expect[i].PodName)
					require.Equal(t, result[i].NodeName, expect[i].NodeName)
					require.Equal(t, result[i].ZoneName, expect[i].ZoneName)
					require.Equal(t, result[i].Namespace, expect[i].Namespace)
					require.Equal(t, result[i].Name, expect[i].Name)
					require.Equal(t, int(result[i].Age / time.Second), int(expect[i].Age / time.Second))
					require.Equal(t, result[i].RequestRole, expect[i].RequestRole)
					require.Equal(t, result[i].Role, expect[i].Role)
				}
			},
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			tt.verifyFunc()
		})
	}
}

func TestGetAzVolumeAttachementsByPod(t *testing.T) {
	fakeClientsetK8s := NewTestK8sClientset()
	fakeClientsetAzDisk := NewTestAzDiskClientset()

	tests := []struct {
		description string
		verifyFunc  func()
	}{
		{
			description: "specified pod name with more than one pv and specified namespace",
			verifyFunc: func() {
				result := GetAzVolumeAttachementsByPod(fakeClientsetK8s, fakeClientsetAzDisk, "test-pod-0", "default")
				expect := []AzvaResource{azvaResource_pod1, azvaResource_pod2}

				for i := 0; i < len(result); i++ {
					require.Equal(t, result[i].ResourceType, expect[i].ResourceType)
					require.Equal(t, result[i].Namespace, expect[i].Namespace)
					require.Equal(t, result[i].Name, expect[i].Name)
					require.Equal(t, int(result[i].Age / time.Second), int(expect[i].Age / time.Second))
					require.Equal(t, result[i].RequestRole, expect[i].RequestRole)
					require.Equal(t, result[i].Role, expect[i].Role)
				}
			},
		},
		{
			description: "specified pod name with more than one pv and empty namespace",
			verifyFunc: func() {
				result := GetAzVolumeAttachementsByPod(fakeClientsetK8s, fakeClientsetAzDisk, "test-pod-0", "")
				expect := []AzvaResource{azvaResource_pod1, azvaResource_pod2}

				for i := 0; i < len(result); i++ {
					require.Equal(t, result[i].ResourceType, expect[i].ResourceType)
					require.Equal(t, result[i].Namespace, expect[i].Namespace)
					require.Equal(t, result[i].Name, expect[i].Name)
					require.Equal(t, int(result[i].Age / time.Second), int(expect[i].Age / time.Second))
					require.Equal(t, result[i].RequestRole, expect[i].RequestRole)
					require.Equal(t, result[i].Role, expect[i].Role)
				}
			},
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			tt.verifyFunc()
		})
	}
}

func TestGetAzVolumeAttachementsByNode(t *testing.T) {
	fakeClientsetAzDisk := NewTestAzDiskClientset()

	tests := []struct {
		description string
		verifyFunc  func()
	}{
		{
			description: "specified node name attached one pvc",
			verifyFunc: func() {
				result := GetAzVolumeAttachementsByNode(fakeClientsetAzDisk, "test-node-0")
				expect := []AzvaResource{azvaResource_node1}

				for i := 0; i < len(result); i++ {
					require.Equal(t, result[i].ResourceType, expect[i].ResourceType)
					require.Equal(t, result[i].Namespace, expect[i].Namespace)
					require.Equal(t, result[i].Name, expect[i].Name)
					require.Equal(t, int(result[i].Age / time.Second), int(expect[i].Age / time.Second))
					require.Equal(t, result[i].RequestRole, expect[i].RequestRole)
					require.Equal(t, result[i].Role, expect[i].Role)
				}
			},
		},
		{
			description: "specified node name attached more than one pvc",
			verifyFunc: func() {
				result := GetAzVolumeAttachementsByNode(fakeClientsetAzDisk, "test-node-1")
				expect := []AzvaResource{azvaResource_node2, azvaResource_node3}

				for i := 0; i < len(result); i++ {
					require.Equal(t, result[i].ResourceType, expect[i].ResourceType)
					require.Equal(t, result[i].Namespace, expect[i].Namespace)
					require.Equal(t, result[i].Name, expect[i].Name)
					require.Equal(t, int(result[i].Age / time.Second), int(expect[i].Age / time.Second))
					require.Equal(t, result[i].RequestRole, expect[i].RequestRole)
					require.Equal(t, result[i].Role, expect[i].Role)
				}
			},
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			tt.verifyFunc()
		})
	}
}

func TestGetAzVolumeAttachementsByZone(t *testing.T) {
	fakeClientsetK8s := NewTestK8sClientset()
	fakeClientsetAzDisk := NewTestAzDiskClientset()

	tests := []struct {
		description string
		verifyFunc  func()
	}{
		{
			description: "specified zone name with one node",
			verifyFunc: func() {
				result := GetAzVolumeAttachementsByZone(fakeClientsetK8s, fakeClientsetAzDisk, "eastus-0")
				expect := []AzvaResource{azvaResource_zone1}

				for i := 0; i < len(result); i++ {
					require.Equal(t, result[i].ResourceType, expect[i].ResourceType)
					require.Equal(t, result[i].Namespace, expect[i].Namespace)
					require.Equal(t, result[i].Name, expect[i].Name)
					require.Equal(t, int(result[i].Age / time.Second), int(expect[i].Age / time.Second))
					require.Equal(t, result[i].RequestRole, expect[i].RequestRole)
					require.Equal(t, result[i].Role, expect[i].Role)
				}
			},
		},
		{
			description: "specified zone name with more than one node",
			verifyFunc: func() {
				result := GetAzVolumeAttachementsByZone(fakeClientsetK8s, fakeClientsetAzDisk, "eastus-1")
				expect := []AzvaResource{azvaResource_zone2, azvaResource_zone3}

				for i := 0; i < len(result); i++ {
					require.Equal(t, result[i].ResourceType, expect[i].ResourceType)
					require.Equal(t, result[i].Namespace, expect[i].Namespace)
					require.Equal(t, result[i].Name, expect[i].Name)
					require.Equal(t, int(result[i].Age / time.Second), int(expect[i].Age / time.Second))
					require.Equal(t, result[i].RequestRole, expect[i].RequestRole)
					require.Equal(t, result[i].Role, expect[i].Role)
				}
			},
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			tt.verifyFunc()
		})
	}
}
