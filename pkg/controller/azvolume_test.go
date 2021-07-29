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
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	fakev1 "k8s.io/client-go/kubernetes/fake"
	diskv1alpha1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	diskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
	diskv1alpha1scheme "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/scheme"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller/mockclient"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller/mockvolumeprovisioner"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var (
	computeDiskURIFormat = "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/disks/%s"

	testSubscription  = "12345678-90ab-cedf-1234-567890abcdef"
	testResourceGroup = "test-rg"

	testManagedDiskURI = fmt.Sprintf(computeDiskURIFormat, testSubscription, testResourceGroup, testAzVolumeName)

	testAzVolumeName = "test-volume"

	testAzVolume = diskv1alpha1.AzVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testAzVolumeName,
			Namespace: testNamespace,
		},
		Spec: diskv1alpha1.AzVolumeSpec{
			UnderlyingVolume: testAzVolumeName,
			CapacityRange: &diskv1alpha1.CapacityRange{
				RequiredBytes: util.GiBToBytes(10),
			},
		},
	}

	testAzVolumeRequest = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testAzVolumeName,
			Namespace: testNamespace,
		},
	}

	testPrimaryAzVolumeAttachmentByVolume = diskv1alpha1.AzVolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "primary-attachment-by-volume",
			Namespace: testNamespace,
			Labels: map[string]string{
				azureutils.VolumeNameLabel: testAzVolumeName,
			},
		},
		Spec: diskv1alpha1.AzVolumeAttachmentSpec{
			RequestedRole: diskv1alpha1.PrimaryRole,
		},
	}

	testReplicaAzVolumeAttachmentByVolume = diskv1alpha1.AzVolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "replica-attachment-by-volume",
			Namespace: testNamespace,
			Labels: map[string]string{
				azureutils.VolumeNameLabel: testAzVolumeName,
			},
		},
		Spec: diskv1alpha1.AzVolumeAttachmentSpec{
			RequestedRole: diskv1alpha1.ReplicaRole,
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

	testPersistentVolume0Name = "test-pv-0"

	testPersistentVolume0 = v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testPersistentVolume0Name,
			Namespace: testNamespace,
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

	testPersistentVolume1Name = "test-pv-1"

	testPersistentVolume1 = v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testPersistentVolume1Name,
			Namespace: testNamespace,
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

func NewTestAzVolumeController(controller *gomock.Controller, namespace string, objects ...runtime.Object) *ReconcileAzVolume {
	diskv1alpha1Objs := make([]runtime.Object, 0)
	kubeObjs := make([]runtime.Object, 0)
	for _, obj := range objects {
		if _, _, err := diskv1alpha1scheme.Scheme.ObjectKinds(obj); err == nil {
			diskv1alpha1Objs = append(diskv1alpha1Objs, obj)
		} else {
			kubeObjs = append(kubeObjs, obj)
		}
	}
	return &ReconcileAzVolume{
		client:            mockclient.NewMockClient(controller),
		azVolumeClient:    diskfakes.NewSimpleClientset(diskv1alpha1Objs...),
		kubeClient:        fakev1.NewSimpleClientset(kubeObjs...),
		namespace:         namespace,
		volumeProvisioner: mockvolumeprovisioner.NewMockVolumeProvisioner(controller),
	}
}

func mockAzVolume(controller *ReconcileAzVolume) {
	controller.client.(*mockclient.MockClient).EXPECT().
		Get(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, key types.NamespacedName, obj runtime.Object) error {
			if target, ok := obj.(*diskv1alpha1.AzVolume); ok {
				azVolume, err := controller.azVolumeClient.DiskV1alpha1().AzVolumes(key.Namespace).Get(ctx, key.Name, metav1.GetOptions{})
				if err != nil {
					return err
				}

				azVolume.DeepCopyInto(target)

				return nil
			}

			gr := schema.GroupResource{
				Group:    obj.GetObjectKind().GroupVersionKind().Group,
				Resource: obj.GetObjectKind().GroupVersionKind().Kind,
			}
			return k8serrors.NewNotFound(gr, testNode1Name)
		}).
		AnyTimes()
	controller.client.(*mockclient.MockClient).EXPECT().
		Patch(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if _, ok := obj.(*diskv1alpha1.AzVolume); ok {
				data, err := patch.Data(obj)
				if err != nil {
					return err
				}
				options := client.PatchOptions{}
				options.ApplyOptions(opts)
				_, err = controller.azVolumeClient.DiskV1alpha1().AzVolumes(obj.GetNamespace()).Patch(ctx, obj.GetName(), patch.Type(), data, *options.AsPatchOptions())
				if err != nil {
					return err
				}

				return nil
			}

			gr := schema.GroupResource{
				Group:    obj.GetObjectKind().GroupVersionKind().Group,
				Resource: obj.GetObjectKind().GroupVersionKind().Kind,
			}
			return k8serrors.NewNotFound(gr, testNode1Name)
		}).
		AnyTimes()
	controller.client.(*mockclient.MockClient).EXPECT().
		Update(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
			if updated, ok := obj.(*diskv1alpha1.AzVolume); ok {
				options := client.UpdateOptions{}
				options.ApplyOptions(opts)
				_, err := controller.azVolumeClient.DiskV1alpha1().AzVolumes(obj.GetNamespace()).Update(ctx, updated, *options.AsUpdateOptions())
				if err != nil {
					return err
				}

				return nil
			}

			gr := schema.GroupResource{
				Group:    obj.GetObjectKind().GroupVersionKind().Group,
				Resource: obj.GetObjectKind().GroupVersionKind().Kind,
			}
			return k8serrors.NewNotFound(gr, testNode1Name)
		}).
		AnyTimes()
	controller.volumeProvisioner.(*mockvolumeprovisioner.MockVolumeProvisioner).EXPECT().
		CreateVolume(gomock.Any(), testAzVolumeName, gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			ctx context.Context,
			volumeName string,
			capacityRange *diskv1alpha1.CapacityRange,
			volumeCapabilities []diskv1alpha1.VolumeCapability,
			parameters map[string]string,
			secrets map[string]string,
			volumeContentSource *diskv1alpha1.ContentVolumeSource,
			accessibilityTopology *diskv1alpha1.TopologyRequirement) (*diskv1alpha1.AzVolumeStatusParams, error) {
			return &diskv1alpha1.AzVolumeStatusParams{
				VolumeID:      testManagedDiskURI,
				VolumeContext: parameters,
				CapacityBytes: capacityRange.RequiredBytes,
				ContentSource: volumeContentSource,
			}, nil
		}).
		MaxTimes(1)
	controller.volumeProvisioner.(*mockvolumeprovisioner.MockVolumeProvisioner).EXPECT().
		DeleteVolume(gomock.Any(), testManagedDiskURI, gomock.Any()).
		Return(nil).
		MaxTimes(1)
	controller.volumeProvisioner.(*mockvolumeprovisioner.MockVolumeProvisioner).EXPECT().
		ExpandVolume(gomock.Any(), testManagedDiskURI, gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			ctx context.Context,
			volumeID string,
			capacityRange *diskv1alpha1.CapacityRange,
			secrets map[string]string) (*diskv1alpha1.AzVolumeStatusParams, error) {
			volumeName, err := azureutils.GetDiskNameFromAzureManagedDiskURI(volumeID)
			if err != nil {
				return nil, err
			}
			azVolume, err := controller.azVolumeClient.DiskV1alpha1().AzVolumes(testNamespace).Get(context.TODO(), volumeName, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}

			azVolumeStatusParams := azVolume.Status.Detail.ResponseObject.DeepCopy()
			azVolumeStatusParams.CapacityBytes = capacityRange.RequiredBytes

			return azVolumeStatusParams, nil
		}).
		MaxTimes(1)
}

func TestAzVolumeControllerReconcile(t *testing.T) {
	tests := []struct {
		description string
		request     reconcile.Request
		setupFunc   func(*testing.T, *gomock.Controller) *ReconcileAzVolume
		verifyFunc  func(*testing.T, *ReconcileAzVolume, reconcile.Result, error)
	}{
		{
			description: "[Success] Should create a volume when a new AzVolume instance is created.",
			request:     testAzVolumeRequest,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAzVolume {
				azVolume := testAzVolume.DeepCopy()

				azVolume.Status.State = diskv1alpha1.VolumeOperationPending

				controller := NewTestAzVolumeController(
					mockCtl,
					testNamespace,
					azVolume)

				mockAzVolume(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAzVolume, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)
				azVolume, err := controller.azVolumeClient.DiskV1alpha1().AzVolumes(testNamespace).Get(context.TODO(), testAzVolumeName, metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, diskv1alpha1.VolumeCreated, azVolume.Status.State)
			},
		},
		{
			description: "[Success] Should expand a volume when a AzVolume Spec and Status report different sizes.",
			request:     testAzVolumeRequest,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAzVolume {
				azVolume := testAzVolume.DeepCopy()

				azVolume.Status.Detail = &diskv1alpha1.AzVolumeStatusDetail{
					ResponseObject: &diskv1alpha1.AzVolumeStatusParams{
						VolumeID:      testManagedDiskURI,
						CapacityBytes: azVolume.Spec.CapacityRange.RequiredBytes,
					},
				}
				azVolume.Spec.CapacityRange.RequiredBytes *= 2
				azVolume.Status.State = diskv1alpha1.VolumeCreated

				controller := NewTestAzVolumeController(
					mockCtl,
					testNamespace,
					azVolume)

				mockAzVolume(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAzVolume, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)
				azVolume, err := controller.azVolumeClient.DiskV1alpha1().AzVolumes(testNamespace).Get(context.TODO(), testAzVolumeName, metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, azVolume.Spec.CapacityRange.RequiredBytes, azVolume.Status.Detail.ResponseObject.CapacityBytes)
			},
		},
		{
			description: "[Success] Should delete a volume when a AzVolume is marked for deletion.",
			request:     testAzVolumeRequest,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAzVolume {
				azVolume := testAzVolume.DeepCopy()

				azVolume.Annotations = map[string]string{
					azureutils.VolumeDeleteRequestAnnotation: "cloud-delete-volume",
				}
				azVolume.Finalizers = []string{azureutils.AzVolumeFinalizer}
				azVolume.Status.Detail = &diskv1alpha1.AzVolumeStatusDetail{
					ResponseObject: &diskv1alpha1.AzVolumeStatusParams{
						VolumeID:      testManagedDiskURI,
						CapacityBytes: azVolume.Spec.CapacityRange.RequiredBytes,
					},
				}
				now := metav1.Time{Time: metav1.Now().Add(-1000)}
				azVolume.ObjectMeta.DeletionTimestamp = &now
				azVolume.Status.State = diskv1alpha1.VolumeCreated

				controller := NewTestAzVolumeController(
					mockCtl,
					testNamespace,
					azVolume)

				mockAzVolume(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAzVolume, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)
				azVolume, err := controller.azVolumeClient.DiskV1alpha1().AzVolumes(testNamespace).Get(context.TODO(), testAzVolumeName, metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, diskv1alpha1.VolumeDeleted, azVolume.Status.State)
			},
		},
		{
			description: "[Success] Should release replica attachments when AzVolume is released.",
			request:     testAzVolumeRequest,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAzVolume {
				azVolume := testAzVolume.DeepCopy()

				azVolume.Status.Detail = &diskv1alpha1.AzVolumeStatusDetail{
					Phase: diskv1alpha1.VolumeReleased,
					ResponseObject: &diskv1alpha1.AzVolumeStatusParams{
						VolumeID:      testManagedDiskURI,
						CapacityBytes: azVolume.Spec.CapacityRange.RequiredBytes,
					},
				}
				azVolume.Status.State = diskv1alpha1.VolumeCreated

				controller := NewTestAzVolumeController(
					mockCtl,
					testNamespace,
					azVolume,
					&testReplicaAzVolumeAttachmentByVolume)

				mockAzVolume(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAzVolume, result reconcile.Result, err error) {
				_, localErr := controller.azVolumeClient.DiskV1alpha1().AzVolumes(testNamespace).Get(context.TODO(), testAzVolumeName, metav1.GetOptions{})
				require.NoError(t, localErr)
				azVolumeAttachments, _ := controller.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(testNamespace).List(context.TODO(), metav1.ListOptions{})
				require.Len(t, azVolumeAttachments.Items, 0)
			},
		},
		{
			description: "[Failure] Should delete volume attachments and requeue when AzVolume is marked for deletion.",
			request:     testAzVolumeRequest,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAzVolume {
				azVolume := testAzVolume.DeepCopy()

				azVolume.Annotations = map[string]string{
					azureutils.VolumeDeleteRequestAnnotation: "cloud-delete-volume",
				}
				azVolume.Finalizers = []string{azureutils.AzVolumeFinalizer}
				azVolume.Status.Detail = &diskv1alpha1.AzVolumeStatusDetail{
					ResponseObject: &diskv1alpha1.AzVolumeStatusParams{
						VolumeID:      testManagedDiskURI,
						CapacityBytes: azVolume.Spec.CapacityRange.RequiredBytes,
					},
				}
				now := metav1.Time{Time: metav1.Now().Add(-1000)}
				azVolume.ObjectMeta.DeletionTimestamp = &now
				azVolume.Status.State = diskv1alpha1.VolumeCreated

				controller := NewTestAzVolumeController(
					mockCtl,
					testNamespace,
					azVolume,
					&testPrimaryAzVolumeAttachmentByVolume,
					&testReplicaAzVolumeAttachmentByVolume)

				mockAzVolume(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAzVolume, result reconcile.Result, err error) {
				require.Equal(t, status.Errorf(codes.Aborted, "volume deletion requeued until attached azVolumeAttachments are entirely detached..."), err)
				require.True(t, result.Requeue)

				azVolume, err := controller.azVolumeClient.DiskV1alpha1().AzVolumes(testNamespace).Get(context.TODO(), testAzVolumeName, metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, diskv1alpha1.VolumeCreated, azVolume.Status.State)

				azVolumeAttachments, _ := controller.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(testNamespace).List(context.TODO(), metav1.ListOptions{})
				require.Len(t, azVolumeAttachments.Items, 0)
			},
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			mockCtl := gomock.NewController(t)
			defer mockCtl.Finish()
			controller := tt.setupFunc(t, mockCtl)
			result, err := controller.Reconcile(context.TODO(), tt.request)
			tt.verifyFunc(t, controller, result, err)
		})
	}
}

func TestAzVolumeControllerRecover(t *testing.T) {
	tests := []struct {
		description string
		request     reconcile.Request
		setupFunc   func(*testing.T, *gomock.Controller) *ReconcileAzVolume
		verifyFunc  func(*testing.T, *ReconcileAzVolume, error)
	}{
		{
			description: "[Success] Should create AzVolume instances for PersistentVolumes using Azure Disk CSI Driver.",
			request:     testAzVolumeRequest,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAzVolume {
				azVolume := testAzVolume.DeepCopy()

				azVolume.Status.State = diskv1alpha1.VolumeOperationPending

				controller := NewTestAzVolumeController(
					mockCtl,
					testNamespace,
					&testStorageClass,
					&testPersistentVolume0,
					&testPersistentVolume1)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAzVolume, err error) {
				require.NoError(t, err)
				azVolumes, err := controller.azVolumeClient.DiskV1alpha1().AzVolumes(testNamespace).List(context.TODO(), metav1.ListOptions{})
				require.NoError(t, err)
				require.Len(t, azVolumes.Items, 2)
			},
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			mockCtl := gomock.NewController(t)
			defer mockCtl.Finish()
			controller := tt.setupFunc(t, mockCtl)
			err := controller.Recover(context.TODO())
			tt.verifyFunc(t, controller, err)
		})
	}
}
