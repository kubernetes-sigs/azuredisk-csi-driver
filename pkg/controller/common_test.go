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
	"strings"
	"time"

	"github.com/golang/mock/gomock"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	diskscheme "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/scheme"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller/mockclient"
	util "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	verifyCRITimeout  = time.Duration(5) * time.Minute
	verifyCRIInterval = time.Duration(1) * time.Second
)

var (
	computeDiskURIFormat = "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/disks/%s"

	testSubscription  = "12345678-90ab-cedf-1234-567890abcdef"
	testResourceGroup = "test-rg"

	testManagedDiskURI0 = getTestDiskURI(testPersistentVolume0Name)
	testManagedDiskURI1 = getTestDiskURI(testPersistentVolume1Name)

	testNamespace = consts.DefaultAzureDiskCrdNamespace

	testNode0Name = "node-0"
	testNode1Name = "node-1"
	testNode2Name = "node-2"

	testSchedulableNodeName = "node-schedulable-1"

	testNode1NotFoundError      = k8serrors.NewNotFound(v1.Resource("nodes"), testNode1Name)
	testNode1ServerTimeoutError = k8serrors.NewServerTimeout(v1.Resource("nodes"), testNode1Name, 1)

	testNode0 = v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: testNode0Name,
		},
		Status: v1.NodeStatus{
			Allocatable: v1.ResourceList(map[v1.ResourceName]resource.Quantity{
				consts.AttachableVolumesField: resource.MustParse("8"),
			}),
		},
	}

	testNode1 = v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: testNode1Name,
		},
		Status: v1.NodeStatus{
			Allocatable: v1.ResourceList(map[v1.ResourceName]resource.Quantity{
				consts.AttachableVolumesField: resource.MustParse("8"),
			}),
		},
	}

	testNode2 = v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: testNode2Name,
		},
		Status: v1.NodeStatus{
			Allocatable: v1.ResourceList(map[v1.ResourceName]resource.Quantity{
				consts.AttachableVolumesField: resource.MustParse("8"),
			}),
		},
	}

	testSchedulableNode1 = v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: testSchedulableNodeName,
		},
		Status: v1.NodeStatus{
			Allocatable: v1.ResourceList(map[v1.ResourceName]resource.Quantity{
				consts.AttachableVolumesField: resource.MustParse("8"),
			}),
		},
		Spec: v1.NodeSpec{
			Unschedulable: false,
		},
	}

	testAzDriverNode0 = azdiskv1beta2.AzDriverNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testNode0Name,
			Namespace: testNamespace,
		},
	}

	testAzDriverNode1 = azdiskv1beta2.AzDriverNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testNode1Name,
			Namespace: testNamespace,
		},
	}

	testNode1Request = createReconcileRequest(testNamespace, testNode1Name)

	testAzDriverNode1NotFoundError = k8serrors.NewNotFound(azdiskv1beta2.Resource("azdrivernodes"), testNode1Name)

	testPersistentVolume0Name = "test-volume-0"
	testPersistentVolume1Name = "test-volume-1"

	testPersistentVolumeClaim0Name = "test-pvc-0"
	testPersistentVolumeClaim1Name = "test-pvc-1"

	testPod0Name = "test-pod-0"
	testPod1Name = "test-pod-1"

	testAzVolume0 = createTestAzVolume(testPersistentVolume0Name, 1)
	testAzVolume1 = createTestAzVolume(testPersistentVolume1Name, 1)

	testAzVolume0Request = createReconcileRequest(testNamespace, testPersistentVolume0Name)
	testAzVolume1Request = createReconcileRequest(testNamespace, testPersistentVolume1Name)

	testPrimaryAzVolumeAttachment0Name = azureutils.GetAzVolumeAttachmentName(testPersistentVolume0Name, testNode0Name)
	testPrimaryAzVolumeAttachment1Name = azureutils.GetAzVolumeAttachmentName(testPersistentVolume1Name, testNode0Name)

	testPrimaryAzVolumeAttachment0 = createTestAzVolumeAttachment(testPersistentVolume0Name, testNode0Name, azdiskv1beta2.PrimaryRole)

	testPrimaryAzVolumeAttachment0Request = createReconcileRequest(testNamespace, testPrimaryAzVolumeAttachment0Name)

	testPrimaryAzVolumeAttachment1 = createTestAzVolumeAttachment(testPersistentVolume1Name, testNode0Name, azdiskv1beta2.PrimaryRole)

	testPrimaryAzVolumeAttachment1Request = createReconcileRequest(testNamespace, testPrimaryAzVolumeAttachment1Name)

	testReplicaAzVolumeAttachmentName = azureutils.GetAzVolumeAttachmentName(testPersistentVolume0Name, testNode1Name)

	testReplicaAzVolumeAttachment = createTestAzVolumeAttachment(testPersistentVolume0Name, testNode1Name, azdiskv1beta2.ReplicaRole)

	testReplicaAzVolumeAttachmentRequest = createReconcileRequest(testNamespace, testReplicaAzVolumeAttachmentName)

	testVolumeAttachmentRequest = createReconcileRequest("", testVolumeAttachmentName)

	testSchedulableNodeRequest = createReconcileRequest(testNamespace, testSchedulableNodeName)

	testStorageClassName = "test-storage-class"

	testStorageClass = storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: testStorageClassName,
		},
		Provisioner: consts.DefaultDriverName,
		Parameters: map[string]string{
			consts.MaxSharesField:            "2",
			consts.MaxMountReplicaCountField: "1",
		},
	}

	testVolumeAttachmentName = "test-attachment"

	testVolumeAttachment = storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name: testVolumeAttachmentName,
		},
		Spec: storagev1.VolumeAttachmentSpec{
			Attacher: consts.DefaultDriverName,
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
					Driver:       consts.DefaultDriverName,
					VolumeHandle: fmt.Sprintf(computeDiskURIFormat, testSubscription, testResourceGroup, testPersistentVolume0Name),
				},
			},
			Capacity: v1.ResourceList{
				v1.ResourceStorage: *resource.NewQuantity(util.GiBToBytes(10), resource.DecimalSI),
			},
			ClaimRef: &v1.ObjectReference{
				Namespace: testNamespace,
				Name:      testPersistentVolumeClaim0Name,
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
					Driver:       consts.DefaultDriverName,
					VolumeHandle: fmt.Sprintf(computeDiskURIFormat, testSubscription, testResourceGroup, testPersistentVolume1Name),
				},
			},
			Capacity: v1.ResourceList{
				v1.ResourceStorage: *resource.NewQuantity(util.GiBToBytes(10), resource.DecimalSI),
			},
			ClaimRef: &v1.ObjectReference{
				Namespace: testNamespace,
				Name:      testPersistentVolumeClaim1Name,
			},
			StorageClassName: testStorageClassName,
		},
	}

	testPod0 = createPod(testNamespace, testPod0Name, []string{testPersistentVolumeClaim0Name})

	testPod0Request = createReconcileRequest(testNamespace, testPod0Name)

	testPod1 = createPod(testNamespace, testPod1Name, []string{testPersistentVolumeClaim0Name, testPersistentVolumeClaim1Name})

	testPod1Request = createReconcileRequest(testNamespace, testPod1Name)

	// dead code that could potentially be used in future unit tests
	_ = testAzVolume1Request
	_ = testPrimaryAzVolumeAttachment1Request
)

func getTestDiskURI(pvName string) string {
	return fmt.Sprintf(computeDiskURIFormat, testSubscription, testResourceGroup, pvName)
}

func createReconcileRequest(namespace, name string) reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}}
}

func createTestAzVolume(pvName string, maxMountReplicaCount int) azdiskv1beta2.AzVolume {
	azVolume := azdiskv1beta2.AzVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvName,
			Namespace: testNamespace,
		},
		Spec: azdiskv1beta2.AzVolumeSpec{
			VolumeName: pvName,
			CapacityRange: &azdiskv1beta2.CapacityRange{
				RequiredBytes: util.GiBToBytes(10),
			},
			MaxMountReplicaCount: maxMountReplicaCount,
			PersistentVolume:     pvName,
		},
	}

	return azVolume
}

func createTestAzVolumeAttachment(pvName, nodeName string, role azdiskv1beta2.Role) azdiskv1beta2.AzVolumeAttachment {
	volumeID := getTestDiskURI(pvName)
	azVolumeAttachment := azdiskv1beta2.AzVolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      azureutils.GetAzVolumeAttachmentName(pvName, nodeName),
			Namespace: testNamespace,
			Labels: map[string]string{
				consts.NodeNameLabel:   nodeName,
				consts.VolumeNameLabel: strings.ToLower(pvName),
				consts.RoleLabel:       string(role),
			},
			Annotations: map[string]string{consts.VolumeAttachRequestAnnotation: "test"},
		},
		Spec: azdiskv1beta2.AzVolumeAttachmentSpec{
			RequestedRole: role,
			VolumeName:    strings.ToLower(pvName),
			VolumeID:      volumeID,
			NodeName:      nodeName,
		},
	}
	return azVolumeAttachment
}

func createPod(podNamespace, podName string, pvcs []string) v1.Pod {
	volumes := []v1.Volume{}
	for _, pvc := range pvcs {
		volumes = append(volumes, v1.Volume{
			VolumeSource: v1.VolumeSource{
				CSI: &v1.CSIVolumeSource{
					Driver: consts.DefaultDriverName,
				},
				PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvc,
				},
			},
		})
	}

	testPod := v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: podNamespace,
			Name:      podName,
		},
		Spec: v1.PodSpec{
			Volumes: volumes,
		},
		Status: v1.PodStatus{
			Phase: v1.PodPending,
		},
	}

	return testPod
}

func initState(client client.Client, azClient azdisk.Interface, kubeClient kubernetes.Interface, objs ...runtime.Object) (c *SharedState) {
	c = NewSharedState(consts.DefaultDriverName, consts.DefaultAzureDiskCrdNamespace, consts.WellKnownTopologyKey, &record.FakeRecorder{}, client, azClient, kubeClient, nil)
	c.MarkRecoveryComplete()

	for _, obj := range objs {
		switch target := obj.(type) {
		case *v1.Pod:
			claims := []string{}
			podKey := getQualifiedName(target.Namespace, target.Name)
			for _, volume := range target.Spec.Volumes {
				if volume.CSI == nil || volume.CSI.Driver != consts.DefaultDriverName {
					continue
				}
				namespacedClaimName := getQualifiedName(target.Namespace, volume.PersistentVolumeClaim.ClaimName)
				claims = append(claims, namespacedClaimName)
				v, _ := c.claimToPodsMap.LoadOrStore(namespacedClaimName, newLockableEntry(set{}))

				lockable := v.(*lockableEntry)
				lockable.Lock()
				pods := lockable.entry.(set)
				if !pods.has(podKey) {
					pods.add(podKey)
				}
				// No need to restore the amended set to claimToPodsMap because set is a reference type
				lockable.Unlock()
			}
			c.podToClaimsMap.Store(podKey, claims)
		case *v1.PersistentVolume:
			diskName, _ := azureutils.GetDiskName(target.Spec.CSI.VolumeHandle)
			azVolumeName := strings.ToLower(diskName)
			claimName := getQualifiedName(target.Spec.ClaimRef.Namespace, target.Spec.ClaimRef.Name)
			c.volumeToClaimMap.Store(azVolumeName, claimName)
			c.claimToVolumeMap.Store(claimName, azVolumeName)
			c.pvToVolumeMap.Store(target.Name, azVolumeName)
			c.createOperationQueue(azVolumeName)
		default:
			continue
		}
	}
	return
}

func splitObjects(objs ...runtime.Object) (azDiskObjs, kubeObjs []runtime.Object) {
	azDiskObjs = make([]runtime.Object, 0)
	kubeObjs = make([]runtime.Object, 0)
	for _, obj := range objs {
		if _, _, err := diskscheme.Scheme.ObjectKinds(obj); err == nil {
			azDiskObjs = append(azDiskObjs, obj)
		} else {
			kubeObjs = append(kubeObjs, obj)
		}
	}

	return
}

type mockStatusClient struct {
	azVolumeClient azdisk.Interface
}

func (s *mockStatusClient) updateStatus(ctx context.Context, obj client.Object) error {
	switch target := obj.(type) {
	case *azdiskv1beta2.AzVolume:
		_, err := s.azVolumeClient.DiskV1beta2().AzVolumes(obj.GetNamespace()).UpdateStatus(ctx, target, metav1.UpdateOptions{})
		if err != nil {
			return err
		}

	case *azdiskv1beta2.AzVolumeAttachment:
		_, err := s.azVolumeClient.DiskV1beta2().AzVolumeAttachments(obj.GetNamespace()).UpdateStatus(ctx, target, metav1.UpdateOptions{})
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
}

func (s *mockStatusClient) Patch(ctx context.Context, obj client.Object, _ client.Patch, _ ...client.PatchOption) error {
	return s.updateStatus(ctx, obj)
}

func (s *mockStatusClient) Update(ctx context.Context, obj client.Object, _ ...client.UpdateOption) error {
	return s.updateStatus(ctx, obj)
}

func mockClients(mockClient *mockclient.MockClient, azVolumeClient azdisk.Interface, kubeClient kubernetes.Interface) {
	statusClient := mockStatusClient{azVolumeClient: azVolumeClient}
	mockClient.EXPECT().Status().Return(&statusClient).AnyTimes()
	mockClient.EXPECT().
		Get(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, key types.NamespacedName, obj runtime.Object) error {
			switch target := obj.(type) {
			case *azdiskv1beta2.AzVolume:
				azVolume, err := azVolumeClient.DiskV1beta2().AzVolumes(key.Namespace).Get(ctx, key.Name, metav1.GetOptions{})
				if err != nil {
					return err
				}

				azVolume.DeepCopyInto(target)

			case *azdiskv1beta2.AzVolumeAttachment:
				azVolumeAttachment, err := azVolumeClient.DiskV1beta2().AzVolumeAttachments(key.Namespace).Get(ctx, key.Name, metav1.GetOptions{})
				if err != nil {
					return err
				}

				azVolumeAttachment.DeepCopyInto(target)

			case *v1.PersistentVolume:
				pv, err := kubeClient.CoreV1().PersistentVolumes().Get(ctx, key.Name, metav1.GetOptions{})
				if err != nil {
					return err
				}

				pv.DeepCopyInto(target)

			case *storagev1.VolumeAttachment:
				volumeAttachment, err := kubeClient.StorageV1().VolumeAttachments().Get(ctx, key.Name, metav1.GetOptions{})
				if err != nil {
					return err
				}

				volumeAttachment.DeepCopyInto(target)

			case *v1.Pod:
				pod, err := kubeClient.CoreV1().Pods(key.Namespace).Get(ctx, key.Name, metav1.GetOptions{})
				if err != nil {
					return err
				}

				pod.DeepCopyInto(target)

			case *v1.Node:
				node, err := kubeClient.CoreV1().Nodes().Get(ctx, key.Name, metav1.GetOptions{})
				if err != nil {
					return err
				}

				node.DeepCopyInto(target)

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
			case *azdiskv1beta2.AzVolume:
				_, err := azVolumeClient.DiskV1beta2().AzVolumes(obj.GetNamespace()).Patch(ctx, obj.GetName(), patch.Type(), data, metav1.PatchOptions{})
				if err != nil {
					return err
				}

			case *azdiskv1beta2.AzVolumeAttachment:
				_, err := azVolumeClient.DiskV1beta2().AzVolumeAttachments(obj.GetNamespace()).Patch(ctx, obj.GetName(), patch.Type(), data, metav1.PatchOptions{})
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
			case *azdiskv1beta2.AzVolume:
				_, err := azVolumeClient.DiskV1beta2().AzVolumes(obj.GetNamespace()).Update(ctx, target, metav1.UpdateOptions{})
				if err != nil {
					return err
				}

			case *azdiskv1beta2.AzVolumeAttachment:
				_, err := azVolumeClient.DiskV1beta2().AzVolumeAttachments(obj.GetNamespace()).Update(ctx, target, metav1.UpdateOptions{})
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
			case *azdiskv1beta2.AzVolumeAttachmentList:
				azVolumeAttachments, err := azVolumeClient.DiskV1beta2().AzVolumeAttachments(testNamespace).List(ctx, *options.AsListOptions())
				if err != nil {
					return err
				}

				azVolumeAttachments.DeepCopyInto(target)
			case *azdiskv1beta2.AzDriverNodeList:
				azDriverNodes, err := azVolumeClient.DiskV1beta2().AzDriverNodes(testNamespace).List(ctx, *options.AsListOptions())
				if err != nil {
					return err
				}

				azDriverNodes.DeepCopyInto(target)

			case *v1.PodList:
				pods, err := kubeClient.CoreV1().Pods("").List(ctx, *options.AsListOptions())
				if err != nil {
					return err
				}

				pods.DeepCopyInto(target)

			case *v1.NodeList:
				nodes, err := kubeClient.CoreV1().Nodes().List(ctx, *options.AsListOptions())
				if err != nil {
					return err
				}

				nodes.DeepCopyInto(target)
			}

			return nil
		}).
		AnyTimes()
	mockClient.EXPECT().
		Delete(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, obj client.Object, opts ...client.ListOption) error {
			switch target := obj.(type) {
			case *azdiskv1beta2.AzVolume:
				err := azVolumeClient.DiskV1beta2().AzVolumes(obj.GetNamespace()).Delete(ctx, target.Name, metav1.DeleteOptions{})
				if err != nil {
					return err
				}

			case *azdiskv1beta2.AzVolumeAttachment:
				err := azVolumeClient.DiskV1beta2().AzVolumeAttachments(obj.GetNamespace()).Delete(ctx, target.Name, metav1.DeleteOptions{})
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
}
