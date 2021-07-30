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
	"fmt"

	"github.com/golang/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	diskv1alpha1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	azVolumeClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	diskv1alpha1scheme "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/scheme"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller/mockclient"
	util "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var (
	computeDiskURIFormat = "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/disks/%s"

	testSubscription  = "12345678-90ab-cedf-1234-567890abcdef"
	testResourceGroup = "test-rg"

	testManagedDiskURI = fmt.Sprintf(computeDiskURIFormat, testSubscription, testResourceGroup, testPersistentVolume0Name)

	testNamespace = "test-namespace"

	testNode0Name = "node-0"
	testNode1Name = "node-1"

	testNode1NotFoundError      = k8serrors.NewNotFound(v1.Resource("nodes"), testNode1Name)
	testNode1ServerTimeoutError = k8serrors.NewServerTimeout(v1.Resource("nodes"), testNode1Name, 1)

	testAzDriverNode0 = v1alpha1.AzDriverNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testNode0Name,
			Namespace: testNamespace,
		},
	}

	testAzDriverNode1 = v1alpha1.AzDriverNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testNode1Name,
			Namespace: testNamespace,
		},
	}

	testNode1Request = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testNode1Name,
			Namespace: testNamespace,
		},
	}

	testAzDriverNode1NotFoundError = k8serrors.NewNotFound(v1alpha1.Resource("azdrivernodes"), testNode1Name)

	testPersistentVolume0Name = "test-volume-0"
	testPersistentVolume1Name = "test-pv-1"

	testAzVolume0 = diskv1alpha1.AzVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testPersistentVolume0Name,
			Namespace: testNamespace,
		},
		Spec: diskv1alpha1.AzVolumeSpec{
			UnderlyingVolume: testPersistentVolume0Name,
			CapacityRange: &diskv1alpha1.CapacityRange{
				RequiredBytes: util.GiBToBytes(10),
			},
		},
	}

	testAzVolume0Request = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testPersistentVolume0Name,
			Namespace: testNamespace,
		},
	}

	testPrimaryAzVolumeAttachmentName = "primary-attachment-by-volume"

	testPrimaryAzVolumeAttachment = diskv1alpha1.AzVolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testPrimaryAzVolumeAttachmentName,
			Namespace: testNamespace,
			Labels: map[string]string{
				azureutils.NodeNameLabel:   testNode0Name,
				azureutils.VolumeNameLabel: testPersistentVolume0Name,
			},
		},
		Spec: diskv1alpha1.AzVolumeAttachmentSpec{
			RequestedRole:    diskv1alpha1.PrimaryRole,
			UnderlyingVolume: testPersistentVolume0Name,
			VolumeID:         testManagedDiskURI,
		},
	}

	testPrimaryAzVolumeAttachmentRequest = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testPrimaryAzVolumeAttachmentName,
			Namespace: testNamespace,
		},
	}

	testReplicaAzVolumeAttachmentName = "replica-attachment-by-volume"

	testReplicaAzVolumeAttachment = diskv1alpha1.AzVolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testReplicaAzVolumeAttachmentName,
			Namespace: testNamespace,
			Labels: map[string]string{
				azureutils.NodeNameLabel:   testNode1Name,
				azureutils.VolumeNameLabel: testPersistentVolume0Name,
			},
		},
		Spec: diskv1alpha1.AzVolumeAttachmentSpec{
			RequestedRole:    diskv1alpha1.ReplicaRole,
			UnderlyingVolume: testPersistentVolume0Name,
			VolumeID:         testManagedDiskURI,
		},
	}

	testStorageClassName = "test-storage-class"

	testStorageClass = storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: testStorageClassName,
		},
		Provisioner: azureutils.DriverName,
		Parameters: map[string]string{
			azureutils.MaxSharesField:            "2",
			azureutils.MaxMountReplicaCountField: "1",
		},
	}

	testVolumeAttachmentName = "test-attachment"

	testVolumeAttachment = storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name: testVolumeAttachmentName,
		},
		Spec: storagev1.VolumeAttachmentSpec{
			Attacher: azureutils.DriverName,
			NodeName: testNode0Name,
			Source: storagev1.VolumeAttachmentSource{
				PersistentVolumeName: &testPersistentVolume0.Name,
			},
		},
	}

	testPersistentVolume0 = v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: testPersistentVolume0Name,
		},
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeSource: v1.PersistentVolumeSource{
				CSI: &v1.CSIPersistentVolumeSource{
					Driver:       azureutils.DriverName,
					VolumeHandle: fmt.Sprintf(computeDiskURIFormat, testSubscription, testResourceGroup, testPersistentVolume0Name),
				},
			},
			Capacity: v1.ResourceList{
				v1.ResourceStorage: *resource.NewQuantity(util.GiBToBytes(10), resource.DecimalSI),
			},
			StorageClassName: "",
		},
	}

	testPersistentVolume0Request = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: testPersistentVolume0Name,
		},
	}

	testPersistentVolume1 = v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: testPersistentVolume1Name,
		},
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeSource: v1.PersistentVolumeSource{
				CSI: &v1.CSIPersistentVolumeSource{
					Driver:       azureutils.DriverName,
					VolumeHandle: fmt.Sprintf(computeDiskURIFormat, testSubscription, testResourceGroup, testPersistentVolume1Name),
				},
			},
			Capacity: v1.ResourceList{
				v1.ResourceStorage: *resource.NewQuantity(util.GiBToBytes(10), resource.DecimalSI),
			},
			StorageClassName: testStorageClassName,
		},
	}
)

func splitObjects(objs ...runtime.Object) (diskv1alpha1Objs, kubeObjs []runtime.Object) {
	diskv1alpha1Objs = make([]runtime.Object, 0)
	kubeObjs = make([]runtime.Object, 0)
	for _, obj := range objs {
		if _, _, err := diskv1alpha1scheme.Scheme.ObjectKinds(obj); err == nil {
			diskv1alpha1Objs = append(diskv1alpha1Objs, obj)
		} else {
			kubeObjs = append(kubeObjs, obj)
		}
	}

	return
}

func mockClients(mockClient *mockclient.MockClient, azVolumeClient azVolumeClientSet.Interface, kubeClient kubernetes.Interface) {
	mockClient.EXPECT().
		Get(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, key types.NamespacedName, obj runtime.Object) error {
			switch target := obj.(type) {
			case *diskv1alpha1.AzVolume:
				azVolume, err := azVolumeClient.DiskV1alpha1().AzVolumes(key.Namespace).Get(ctx, key.Name, metav1.GetOptions{})
				if err != nil {
					return err
				}

				azVolume.DeepCopyInto(target)

			case *diskv1alpha1.AzVolumeAttachment:
				azVolumeAttachment, err := azVolumeClient.DiskV1alpha1().AzVolumeAttachments(key.Namespace).Get(ctx, key.Name, metav1.GetOptions{})
				if err != nil {
					return err
				}

				azVolumeAttachment.DeepCopyInto(target)

			case *corev1.PersistentVolume:
				pv, err := kubeClient.CoreV1().PersistentVolumes().Get(ctx, key.Name, metav1.GetOptions{})
				if err != nil {
					return err
				}

				pv.DeepCopyInto(target)

			default:
				gr := schema.GroupResource{
					Group:    target.GetObjectKind().GroupVersionKind().Group,
					Resource: target.GetObjectKind().GroupVersionKind().Kind,
				}
				return k8serrors.NewNotFound(gr, key.Name)
			}

			return nil
		}).
		AnyTimes()
	mockClient.EXPECT().
		Patch(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOptions) error {
			data, err := patch.Data(obj)
			if err != nil {
				return err
			}

			switch target := obj.(type) {
			case *diskv1alpha1.AzVolume:
				_, err := azVolumeClient.DiskV1alpha1().AzVolumes(obj.GetNamespace()).Patch(ctx, obj.GetName(), patch.Type(), data, metav1.PatchOptions{})
				if err != nil {
					return err
				}

			case *diskv1alpha1.AzVolumeAttachment:
				_, err := azVolumeClient.DiskV1alpha1().AzVolumeAttachments(obj.GetNamespace()).Patch(ctx, obj.GetName(), patch.Type(), data, metav1.PatchOptions{})
				if err != nil {
					return err
				}

			default:
				gr := schema.GroupResource{
					Group:    target.GetObjectKind().GroupVersionKind().Group,
					Resource: target.GetObjectKind().GroupVersionKind().Kind,
				}
				return k8serrors.NewNotFound(gr, obj.GetName())
			}

			return nil
		}).
		AnyTimes()
	mockClient.EXPECT().
		Update(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
			switch target := obj.(type) {
			case *diskv1alpha1.AzVolume:
				_, err := azVolumeClient.DiskV1alpha1().AzVolumes(obj.GetNamespace()).Update(ctx, target, metav1.UpdateOptions{})
				if err != nil {
					return err
				}

			case *diskv1alpha1.AzVolumeAttachment:
				_, err := azVolumeClient.DiskV1alpha1().AzVolumeAttachments(obj.GetNamespace()).Update(ctx, target, metav1.UpdateOptions{})
				if err != nil {
					return err
				}

			default:
				gr := schema.GroupResource{
					Group:    target.GetObjectKind().GroupVersionKind().Group,
					Resource: target.GetObjectKind().GroupVersionKind().Kind,
				}
				return k8serrors.NewNotFound(gr, obj.GetName())
			}

			return nil
		}).
		AnyTimes()
	mockClient.EXPECT().
		List(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
			options := client.ListOptions{}
			options.ApplyOptions(opts)

			switch target := list.(type) {
			case *diskv1alpha1.AzVolumeAttachmentList:
				azVolumeAttachments, err := azVolumeClient.DiskV1alpha1().AzVolumeAttachments(testNamespace).List(ctx, *options.AsListOptions())
				if err != nil {
					return err
				}

				azVolumeAttachments.DeepCopyInto(target)
			}

			return nil
		}).
		AnyTimes()
}
