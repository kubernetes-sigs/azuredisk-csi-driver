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
				expect := []AzvaResourceAll{
					{
						PodName:     "test-pod-0",
						NodeName:    "test-node-0",
						ZoneName:    "eastus-0",
						Namespace:   consts.DefaultAzureDiskCrdNamespace,
						Name:        "test-azVolumeAttachment-0",
						Age:         metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
						RequestRole: "Primary",
						Role:        "Primary",
						State:       "Attached",
					},
					{
						PodName:     "test-pod-0",
						NodeName:    "test-node-1",
						ZoneName:    "eastus-1",
						Namespace:   consts.DefaultAzureDiskCrdNamespace,
						Name:        "test-azVolumeAttachment-1",
						Age:         metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
						RequestRole: "Replica",
						Role:        "Replica",
						State:       "Attached",
					},
					{
						PodName:     "test-pod-1",
						NodeName:    "test-node-0",
						ZoneName:    "eastus-0",
						Namespace:   consts.DefaultAzureDiskCrdNamespace,
						Name:        "test-azVolumeAttachment-0",
						Age:         metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
						RequestRole: "Primary",
						Role:        "Primary",
						State:       "Attached",
					},
				}

				for i := 0; i < len(result); i++ {
					require.Equal(t, result[i], expect[i])
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
				expect := []AzvaResource{
					{
						ResourceType: "test-pod-0",
						Namespace:    consts.DefaultAzureDiskCrdNamespace,
						Name:         "test-azVolumeAttachment-0",
						Age:          metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
						RequestRole:  "Primary",
						Role:         "Primary",
						State:        "Attached",
					},
					{
						ResourceType: "test-pod-0",
						Namespace:    consts.DefaultAzureDiskCrdNamespace,
						Name:         "test-azVolumeAttachment-1",
						Age:          metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
						RequestRole:  "Replica",
						Role:         "Replica",
						State:        "Attached",
					},
				}

				for i := 0; i < len(result); i++ {
					require.Equal(t, result[i], expect[i])
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
				expect := []AzvaResource{
					{
						ResourceType: "test-node-0",
						Namespace:    consts.DefaultAzureDiskCrdNamespace,
						Name:         "test-azVolumeAttachment-0",
						Age:          metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
						RequestRole:  "Primary",
						Role:         "Primary",
						State:        "Attached",
					},
				}

				for i := 0; i < len(result); i++ {
					require.Equal(t, result[i], expect[i])
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
				expect := []AzvaResource{
					{
						ResourceType: "eastus-0",
						Namespace:    consts.DefaultAzureDiskCrdNamespace,
						Name:         "test-azVolumeAttachment-0",
						Age:          metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
						RequestRole:  "Primary",
						Role:         "Primary",
						State:        "Attached",
					},
				}

				for i := 0; i < len(result); i++ {
					require.Equal(t, result[i], expect[i])
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
