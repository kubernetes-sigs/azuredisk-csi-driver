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
	v1beta1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta1"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

var azvaResource_All1 AzvaResource = AzvaResource{
	PodName:     TestPod0,
	NodeName:    TestNode0,
	ZoneName:    TestZone0,
	Namespace:   consts.DefaultAzureDiskCrdNamespace,
	Name:        TestAzVolumeAttachment0,
	Age:         metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
	RequestRole: v1beta1.PrimaryRole,
	Role:        v1beta1.PrimaryRole,
	State:       v1beta1.Attached,
}

var azvaResource_All2 AzvaResource = AzvaResource{
	PodName:     TestPod1,
	NodeName:    TestNode0,
	ZoneName:    TestZone0,
	Namespace:   consts.DefaultAzureDiskCrdNamespace,
	Name:        TestAzVolumeAttachment0,
	Age:         metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
	RequestRole: v1beta1.PrimaryRole,
	Role:        v1beta1.PrimaryRole,
	State:       v1beta1.Attached,
}

var azvaResource_All3 AzvaResource = AzvaResource{
	PodName:     TestPod0,
	NodeName:    TestNode1,
	ZoneName:    TestZone1,
	Namespace:   consts.DefaultAzureDiskCrdNamespace,
	Name:        TestAzVolumeAttachment1,
	Age:         metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
	RequestRole: v1beta1.ReplicaRole,
	Role:        v1beta1.ReplicaRole,
	State:       v1beta1.Attached,
}

var azvaResource_pod1 AzvaResource = AzvaResource{
	PodName:     TestPod0,
	NodeName:    "",
	ZoneName:    "",
	Namespace:   consts.DefaultAzureDiskCrdNamespace,
	Name:        TestAzVolumeAttachment0,
	Age:         metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
	RequestRole: v1beta1.PrimaryRole,
	Role:        v1beta1.PrimaryRole,
	State:       v1beta1.Attached,
}

var azvaResource_pod2 AzvaResource = AzvaResource{
	PodName:     TestPod0,
	NodeName:    "",
	ZoneName:    "",
	Namespace:   consts.DefaultAzureDiskCrdNamespace,
	Name:        TestAzVolumeAttachment1,
	Age:         metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
	RequestRole: v1beta1.ReplicaRole,
	Role:        v1beta1.ReplicaRole,
	State:       v1beta1.Attached,
}

var azvaResource_node1 AzvaResource = AzvaResource{
	PodName:     "",
	NodeName:    TestNode0,
	ZoneName:    "",
	Namespace:   consts.DefaultAzureDiskCrdNamespace,
	Name:        TestAzVolumeAttachment0,
	Age:         metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
	RequestRole: v1beta1.PrimaryRole,
	Role:        v1beta1.PrimaryRole,
	State:       v1beta1.Attached,
}

var azvaResource_node2 AzvaResource = AzvaResource{
	PodName:     "",
	NodeName:    TestNode1,
	ZoneName:    "",
	Namespace:   consts.DefaultAzureDiskCrdNamespace,
	Name:        TestAzVolumeAttachment1,
	Age:         metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
	RequestRole: v1beta1.ReplicaRole,
	Role:        v1beta1.ReplicaRole,
	State:       v1beta1.Attached,
}

var azvaResource_node3 AzvaResource = AzvaResource{
	PodName:     "",
	NodeName:    TestNode1,
	ZoneName:    "",
	Namespace:   consts.DefaultAzureDiskCrdNamespace,
	Name:        TestAzVolumeAttachment2,
	Age:         metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
	RequestRole: v1beta1.PrimaryRole,
	Role:        v1beta1.PrimaryRole,
	State:       v1beta1.Attached,
}

var azvaResource_zone1 AzvaResource = AzvaResource{
	PodName:     "",
	NodeName:    "",
	ZoneName:    TestZone0,
	Namespace:   consts.DefaultAzureDiskCrdNamespace,
	Name:        TestAzVolumeAttachment0,
	Age:         metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
	RequestRole: v1beta1.PrimaryRole,
	Role:        v1beta1.PrimaryRole,
	State:       v1beta1.Attached,
}

var azvaResource_zone2 AzvaResource = AzvaResource{
	PodName:     "",
	NodeName:    "",
	ZoneName:    TestZone1,
	Namespace:   consts.DefaultAzureDiskCrdNamespace,
	Name:        TestAzVolumeAttachment1,
	Age:         metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
	RequestRole: v1beta1.ReplicaRole,
	Role:        v1beta1.ReplicaRole,
	State:       v1beta1.Attached,
}

var azvaResource_zone3 AzvaResource = AzvaResource{
	PodName:     "",
	NodeName:    "",
	ZoneName:    TestZone1,
	Namespace:   consts.DefaultAzureDiskCrdNamespace,
	Name:        TestAzVolumeAttachment2,
	Age:         metav1.Now().Sub(time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC)),
	RequestRole: v1beta1.PrimaryRole,
	Role:        v1beta1.PrimaryRole,
	State:       v1beta1.Attached,
}

func TestGetAllAzVolumeAttachements(t *testing.T) {
	fakeClientsetK8s := NewTestK8sClientset()
	fakeClientsetAzDisk := NewTestAzDiskClientset()

	tests := []struct {
		description string
		verifyFunc  func()
	}{
		{
			description: "Test get all AzVolumeAttachements with specified namespace",
			verifyFunc: func() {
				result := GetAllAzVolumeAttachements(fakeClientsetK8s, fakeClientsetAzDisk, metav1.NamespaceDefault)
				expect := []AzvaResource{azvaResource_All1, azvaResource_All2, azvaResource_All3}

				verifyFields(t, result, expect)
			},
		},
		{
			description: "Test get all AzVolumeAttachements with empty namespace",
			verifyFunc: func() {
				result := GetAllAzVolumeAttachements(fakeClientsetK8s, fakeClientsetAzDisk, metav1.NamespaceNone)
				expect := []AzvaResource{azvaResource_All1, azvaResource_All2, azvaResource_All3}

				verifyFields(t, result, expect)
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
			description: "Test get AzVolumeAttachements with specified namespace and pod name which has more than one pv",
			verifyFunc: func() {
				result := GetAzVolumeAttachementsByPod(fakeClientsetK8s, fakeClientsetAzDisk, TestPod0, metav1.NamespaceDefault)
				expect := []AzvaResource{azvaResource_pod1, azvaResource_pod2}

				verifyFields(t, result, expect)
			},
		},
		{
			description: "Test get AzVolumeAttachements with empty namespace and specified pod name which has more than one pv",
			verifyFunc: func() {
				result := GetAzVolumeAttachementsByPod(fakeClientsetK8s, fakeClientsetAzDisk, TestPod0, metav1.NamespaceNone)
				expect := []AzvaResource{azvaResource_pod1, azvaResource_pod2}

				verifyFields(t, result, expect)
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
			description: "Test get AzVolumeAttachements with specified node name attached one pvc",
			verifyFunc: func() {
				result := GetAzVolumeAttachementsByNode(fakeClientsetAzDisk, TestNode0)
				expect := []AzvaResource{azvaResource_node1}

				verifyFields(t, result, expect)
			},
		},
		{
			description: "Test get AzVolumeAttachements with specified node name attached more than one pvc",
			verifyFunc: func() {
				result := GetAzVolumeAttachementsByNode(fakeClientsetAzDisk, TestNode1)
				expect := []AzvaResource{azvaResource_node2, azvaResource_node3}

				verifyFields(t, result, expect)
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
			description: "Test get AzVolumeAttachements with specified zone name which has one node",
			verifyFunc: func() {
				result := GetAzVolumeAttachementsByZone(fakeClientsetK8s, fakeClientsetAzDisk, TestZone0)
				expect := []AzvaResource{azvaResource_zone1}

				verifyFields(t, result, expect)
			},
		},
		{
			description: "Test get AzVolumeAttachements with specified zone name which has more than one node",
			verifyFunc: func() {
				result := GetAzVolumeAttachementsByZone(fakeClientsetK8s, fakeClientsetAzDisk, TestZone1)
				expect := []AzvaResource{azvaResource_zone2, azvaResource_zone3}

				verifyFields(t, result, expect)
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

func verifyFields(t *testing.T, result []AzvaResource, expect []AzvaResource) {
	require.Equal(t, len(result), len(expect))

	for i := 0; i < len(result); i++ {
		require.Equal(t, result[i].PodName, expect[i].PodName)
		require.Equal(t, result[i].NodeName, expect[i].NodeName)
		require.Equal(t, result[i].ZoneName, expect[i].ZoneName)
		require.Equal(t, result[i].Namespace, expect[i].Namespace)
		require.Equal(t, result[i].Name, expect[i].Name)
		require.Equal(t, int(result[i].Age/time.Second), int(expect[i].Age/time.Second))
		require.Equal(t, result[i].RequestRole, expect[i].RequestRole)
		require.Equal(t, result[i].Role, expect[i].Role)
	}
}
