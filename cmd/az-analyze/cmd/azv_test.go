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

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta1"
	diskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

func NewTestK8sClientset() *fake.Clientset {

    return fake.NewSimpleClientset(&v1.Pod {
            ObjectMeta: metav1.ObjectMeta {
                Name: "test-pod-0",
                Namespace: "default",
            },
            Spec: v1.PodSpec {
                Volumes: []v1.Volume {
                    {
                        Name: "volume-name-0",
                        VolumeSource: v1.VolumeSource {
                            PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource {
                                ClaimName: "test-pvcClaimName",
                            },
                        },
                    },
                    {
                        Name: "volume-name-1",
                        VolumeSource: v1.VolumeSource {
                            PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource {
                                ClaimName: "test-pvcClaimName-1",
                            },
                        },
                    },
                },
            },
        }, &v1.Pod {
            ObjectMeta: metav1.ObjectMeta {
                Name: "test-pod-1",
                Namespace: "default",
            },
            Spec: v1.PodSpec {
                Volumes: []v1.Volume {
                    {
                        Name: "volume-name",
                        VolumeSource: v1.VolumeSource {
                            PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource {
                                ClaimName: "test-pvcClaimName",
                            },
                        },
                    },
                },
            },
    })
}

func NewTestAzDiskClientset() *diskfakes.Clientset {
    return diskfakes.NewSimpleClientset(&v1beta1.AzVolume {
            ObjectMeta: metav1.ObjectMeta {
                Name: "test-azVolume",
                Namespace: "azure-disk-csi",
            },
            Spec: v1beta1.AzVolumeSpec{
                VolumeName: "test-azVolume",
                Parameters: map[string]string{consts.PvcNameKey: "test-pvcClaimName"},
            },
            Status: v1beta1.AzVolumeStatus{
                State: v1beta1.VolumeCreated,
            },
        }, &v1beta1.AzVolume {
            ObjectMeta: metav1.ObjectMeta {
                Name: "test-azVolume-1",
                Namespace: "azure-disk-csi",
            },
            Spec: v1beta1.AzVolumeSpec{
                VolumeName: "test-azVolume-1",
                Parameters: map[string]string{consts.PvcNameKey: "test-pvcClaimName-1"},
            },
            Status: v1beta1.AzVolumeStatus{
                State: v1beta1.VolumeCreated,
            },
    })
}

func TestGetAzVolumesByPod(t *testing.T) {
    fakeClientsetK8s := NewTestK8sClientset()
    fakeClientsetAzDisk := NewTestAzDiskClientset()

    tests := []struct {
        description string
        verifyFunc func()
    }{
        {
            description: "specified pod name with more than one pv",
            verifyFunc: func() {
                result := GetAzVolumesByPod(fakeClientsetK8s, fakeClientsetAzDisk, "test-pod-0", "default")
                expect := []AzvResource {
                    {
                        ResourceType: "test-pod-0",
                        Namespace:    consts.DefaultAzureDiskCrdNamespace,
                        Name:         "test-azVolume",
                        State:        v1beta1.VolumeCreated,
                    },
                    {
                        ResourceType: "test-pod-0",
                        Namespace:    consts.DefaultAzureDiskCrdNamespace,
                        Name:         "test-azVolume-1",
                        State:        v1beta1.VolumeCreated,
                    },
                }

                for i := 0; i < len(result); i++ {
                    require.Equal(t, result[i], expect[i])
                }
            },
        },
        // {
        //     description: "specified pod name with namespace default",
        //     verifyFunc: func() {
        //         result := GetAzVolumesByPod(fakeClientsetK8s, fakeClientsetAzDisk, "test-pod-1", "default")
        //         expect := []AzvResource {
        //             {
        //                 ResourceType: "test-pod-1",
        //                 Namespace:    consts.DefaultAzureDiskCrdNamespace,
        //                 Name:         "test-azVolume",
        //                 State:        v1beta1.VolumeCreated,
        //             },
        //         }

        //         for i := 0; i < len(result); i++ {
        //             require.Equal(t, result[i], expect[i])
        //         }
        //     },
        // },
        {
            description: "empty pod name with multiple pods have same pvc",
            verifyFunc: func() {
                result := GetAzVolumesByPod(fakeClientsetK8s, fakeClientsetAzDisk, "", "default")
                expect := []AzvResource {
                    {
                        ResourceType: "test-pod-0",
                        Namespace:    consts.DefaultAzureDiskCrdNamespace,
                        Name:         "test-azVolume",
                        State:        v1beta1.VolumeCreated,
                    },
                    {
                        ResourceType: "test-pod-1",
                        Namespace:    consts.DefaultAzureDiskCrdNamespace,
                        Name:         "test-azVolume",
                        State:        v1beta1.VolumeCreated,
                    },
                    {
                        ResourceType: "test-pod-0",
                        Namespace:    consts.DefaultAzureDiskCrdNamespace,
                        Name:         "test-azVolume-1",
                        State:        v1beta1.VolumeCreated,
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
