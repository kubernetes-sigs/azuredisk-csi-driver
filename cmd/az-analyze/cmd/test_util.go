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
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	azdiskv1beta1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta1"
	azdiskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

const (
	TestPod0                = "test-pod-0"
	TestPod1                = "test-pod-1"
	TestNode0               = "test-node-0"
	TestNode1               = "test-node-1"
	TestZone0               = "eastus-0"
	TestZone1               = "eastus-1"
	TestAzVolume0           = "test-azVolume-0"
	TestAzVolume1           = "test-azVolume-1"
	TestAzVolumeAttachment0 = "test-azVolumeAttachment-0"
	TestAzVolumeAttachment1 = "test-azVolumeAttachment-1"
	TestAzVolumeAttachment2 = "test-azVolumeAttachment-2"
	TestVolume0             = "volume-name-0"
	TestVolume1             = "volume-name-1"
	TestPvcClaimName0       = "test-pvcClaimName-0"
	TestPvcClaimName1       = "test-pvcClaimName-1"
	TestPvcClaimName2       = "test-pvcClaimName-2"
)

func NewTestK8sClientset() *fake.Clientset {
	fakePods := []v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      TestPod0,
				Namespace: metav1.NamespaceDefault,
			},
			Spec: v1.PodSpec{
				Volumes: []v1.Volume{
					{
						Name: TestVolume0,
						VolumeSource: v1.VolumeSource{
							PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
								ClaimName: TestPvcClaimName0,
							},
						},
					},
					{
						Name: TestVolume1,
						VolumeSource: v1.VolumeSource{
							PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
								ClaimName: TestPvcClaimName1,
							},
						},
					},
				},
			},
		}, {
			ObjectMeta: metav1.ObjectMeta{
				Name:      TestPod1,
				Namespace: metav1.NamespaceDefault,
			},
			Spec: v1.PodSpec{
				Volumes: []v1.Volume{
					{
						Name: TestVolume0,
						VolumeSource: v1.VolumeSource{
							PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
								ClaimName: TestPvcClaimName0,
							},
						},
					},
				},
			},
		},
	}

	fakeNodes := []v1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   TestNode0,
				Labels: map[string]string{consts.WellKnownTopologyKey: TestZone0},
			},
		}, {
			ObjectMeta: metav1.ObjectMeta{
				Name:   TestNode1,
				Labels: map[string]string{consts.WellKnownTopologyKey: TestZone1},
			},
		},
	}

	objs := make([]runtime.Object, 0)
	for _, pod := range fakePods {
		pod := pod
		objs = append(objs, &pod)
	}

	for _, node := range fakeNodes {
		node := node
		objs = append(objs, &node)
	}

	return fake.NewSimpleClientset(objs...)
}

func NewTestAzDiskClientset() *azdiskfakes.Clientset {
	fakeAzvs := []azdiskv1beta1.AzVolume{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      TestAzVolume0,
				Namespace: consts.DefaultAzureDiskCrdNamespace,
			},
			Spec: azdiskv1beta1.AzVolumeSpec{
				VolumeName: TestAzVolume0,
				Parameters: map[string]string{consts.PvcNameKey: TestPvcClaimName0},
			},
			Status: azdiskv1beta1.AzVolumeStatus{
				State: azdiskv1beta1.VolumeCreated,
			},
		}, {
			ObjectMeta: metav1.ObjectMeta{
				Name:      TestAzVolume1,
				Namespace: consts.DefaultAzureDiskCrdNamespace,
			},
			Spec: azdiskv1beta1.AzVolumeSpec{
				VolumeName: TestAzVolume1,
				Parameters: map[string]string{consts.PvcNameKey: TestPvcClaimName1},
			},
			Status: azdiskv1beta1.AzVolumeStatus{
				State: azdiskv1beta1.VolumeCreated,
			},
		},
	}

	fakeAzvas := []azdiskv1beta1.AzVolumeAttachment{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      TestAzVolumeAttachment0,
				Namespace: consts.DefaultAzureDiskCrdNamespace,
				CreationTimestamp: metav1.Time{
					Time: time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC),
				},
			},
			Spec: azdiskv1beta1.AzVolumeAttachmentSpec{
				VolumeContext: map[string]string{consts.PvcNameKey: TestPvcClaimName0},
				NodeName:      TestNode0,
				RequestedRole: azdiskv1beta1.PrimaryRole,
			},
			Status: azdiskv1beta1.AzVolumeAttachmentStatus{
				Detail: &azdiskv1beta1.AzVolumeAttachmentStatusDetail{
					Role: azdiskv1beta1.PrimaryRole,
				},
				State: azdiskv1beta1.Attached,
			},
		}, {
			ObjectMeta: metav1.ObjectMeta{
				Name:      TestAzVolumeAttachment1,
				Namespace: consts.DefaultAzureDiskCrdNamespace,
				CreationTimestamp: metav1.Time{
					Time: time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC),
				},
			},
			Spec: azdiskv1beta1.AzVolumeAttachmentSpec{
				VolumeContext: map[string]string{consts.PvcNameKey: TestPvcClaimName1},
				NodeName:      TestNode1,
				RequestedRole: azdiskv1beta1.ReplicaRole,
			},
			Status: azdiskv1beta1.AzVolumeAttachmentStatus{
				Detail: &azdiskv1beta1.AzVolumeAttachmentStatusDetail{
					Role: azdiskv1beta1.ReplicaRole,
				},
				State: azdiskv1beta1.Attached,
			},
		}, {
			ObjectMeta: metav1.ObjectMeta{
				Name:      TestAzVolumeAttachment2,
				Namespace: consts.DefaultAzureDiskCrdNamespace,
				CreationTimestamp: metav1.Time{
					Time: time.Date(2022, 4, 27, 20, 34, 58, 651387237, time.UTC),
				},
			},
			Spec: azdiskv1beta1.AzVolumeAttachmentSpec{
				VolumeContext: map[string]string{consts.PvcNameKey: TestPvcClaimName2},
				NodeName:      TestNode1,
				RequestedRole: azdiskv1beta1.PrimaryRole,
			},
			Status: azdiskv1beta1.AzVolumeAttachmentStatus{
				Detail: &azdiskv1beta1.AzVolumeAttachmentStatusDetail{
					Role: azdiskv1beta1.PrimaryRole,
				},
				State: azdiskv1beta1.Attached,
			},
		},
	}

	objs := make([]runtime.Object, 0)
	for _, azv := range fakeAzvs {
		azv := azv
		objs = append(objs, &azv)
	}

	for _, azva := range fakeAzvas {
		azva := azva
		objs = append(objs, &azva)
	}

	return azdiskfakes.NewSimpleClientset(objs...)
}
