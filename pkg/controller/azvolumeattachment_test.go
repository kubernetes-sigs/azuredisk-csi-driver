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
	"sync"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
	storagev1 "k8s.io/api/storage/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	fakev1 "k8s.io/client-go/kubernetes/fake"
	diskv1alpha1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	diskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
	diskv1alpha1scheme "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/scheme"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller/mockattachmentprovisioner"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller/mockclient"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var (
	testPrimaryAzVolumeAttachmentRequest = reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testPrimaryAzVolumeAttachmentByVolume.Name,
			Namespace: testPrimaryAzVolumeAttachmentByVolume.Namespace,
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
)

func NewTestAzVolumeAttachmentController(controller *gomock.Controller, namespace string, objects ...runtime.Object) *ReconcileAzVolumeAttachment {
	diskv1alpha1Objs := make([]runtime.Object, 0)
	kubeObjs := make([]runtime.Object, 0)
	for _, obj := range objects {
		if _, _, err := diskv1alpha1scheme.Scheme.ObjectKinds(obj); err == nil {
			diskv1alpha1Objs = append(diskv1alpha1Objs, obj)
		} else {
			kubeObjs = append(kubeObjs, obj)
		}
	}
	return &ReconcileAzVolumeAttachment{
		client:                mockclient.NewMockClient(controller),
		azVolumeClient:        diskfakes.NewSimpleClientset(diskv1alpha1Objs...),
		kubeClient:            fakev1.NewSimpleClientset(kubeObjs...),
		syncMutex:             sync.RWMutex{},
		namespace:             namespace,
		mutexMap:              make(map[string]*sync.Mutex),
		mutexMapMutex:         sync.RWMutex{},
		volumeMap:             make(map[string]string),
		volumeMapMutex:        sync.RWMutex{},
		cleanUpMap:            make(map[string]context.CancelFunc),
		cleanUpMapMutex:       sync.RWMutex{},
		attachmentProvisioner: mockattachmentprovisioner.NewMockAttachmentProvisioner(controller),
	}
}

func mockClientsAndAttachmentProvisioner(controller *ReconcileAzVolumeAttachment) {
	controller.client.(*mockclient.MockClient).EXPECT().
		Get(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, key types.NamespacedName, obj runtime.Object) error {
			switch target := obj.(type) {
			case *diskv1alpha1.AzVolume:
				azVolume, err := controller.azVolumeClient.DiskV1alpha1().AzVolumes(key.Namespace).Get(ctx, key.Name, metav1.GetOptions{})
				if err != nil {
					return err
				}

				azVolume.DeepCopyInto(target)

			case *diskv1alpha1.AzVolumeAttachment:
				azVolumeAttachment, err := controller.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(key.Namespace).Get(ctx, key.Name, metav1.GetOptions{})
				if err != nil {
					return err
				}

				azVolumeAttachment.DeepCopyInto(target)

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
	controller.client.(*mockclient.MockClient).EXPECT().
		Patch(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOptions) error {
			data, err := patch.Data(obj)
			if err != nil {
				return err
			}

			switch target := obj.(type) {
			case *diskv1alpha1.AzVolume:
				_, err := controller.azVolumeClient.DiskV1alpha1().AzVolumes(obj.GetNamespace()).Patch(ctx, obj.GetName(), patch.Type(), data, metav1.PatchOptions{})
				if err != nil {
					return err
				}

			case *diskv1alpha1.AzVolumeAttachment:
				_, err := controller.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(obj.GetNamespace()).Patch(ctx, obj.GetName(), patch.Type(), data, metav1.PatchOptions{})
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
	controller.client.(*mockclient.MockClient).EXPECT().
		Update(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
			switch target := obj.(type) {
			case *diskv1alpha1.AzVolume:
				_, err := controller.azVolumeClient.DiskV1alpha1().AzVolumes(obj.GetNamespace()).Update(ctx, target, metav1.UpdateOptions{})
				if err != nil {
					return err
				}

			case *diskv1alpha1.AzVolumeAttachment:
				_, err := controller.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(obj.GetNamespace()).Update(ctx, target, metav1.UpdateOptions{})
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
	controller.client.(*mockclient.MockClient).EXPECT().
		List(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
			options := client.ListOptions{}
			options.ApplyOptions(opts)

			switch target := list.(type) {
			case *diskv1alpha1.AzVolumeAttachmentList:
				azVolumeAttachments, err := controller.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(testNamespace).List(ctx, *options.AsListOptions())
				if err != nil {
					return err
				}

				azVolumeAttachments.DeepCopyInto(target)
			}

			return nil
		})

	controller.attachmentProvisioner.(*mockattachmentprovisioner.MockAttachmentProvisioner).EXPECT().
		PublishVolume(gomock.Any(), testManagedDiskURI, gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, volumeID, nodeID string, volumeContext map[string]string) (map[string]string, error) {
			return volumeContext, nil
		}).
		MaxTimes(1)
	controller.attachmentProvisioner.(*mockattachmentprovisioner.MockAttachmentProvisioner).EXPECT().
		UnpublishVolume(gomock.Any(), testManagedDiskURI, gomock.Any()).
		DoAndReturn(func(ctx context.Context, volumeID, nodeID string) error {
			return nil
		}).
		MaxTimes(1)
}

func TestAzVolumeAttachmentControllerReconcile(t *testing.T) {
	tests := []struct {
		description string
		request     reconcile.Request
		setupFunc   func(*testing.T, *gomock.Controller) *ReconcileAzVolumeAttachment
		verifyFunc  func(*testing.T, *ReconcileAzVolumeAttachment, reconcile.Result, error)
	}{
		{
			description: "[Success] Should attach volume when new primary AzVolumeAttachment is created.",
			request:     testPrimaryAzVolumeAttachmentRequest,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAzVolumeAttachment {
				newAttachment := testPrimaryAzVolumeAttachmentByVolume.DeepCopy()
				newAttachment.Status.State = diskv1alpha1.AttachmentPending

				controller := NewTestAzVolumeAttachmentController(
					mockCtl,
					testNamespace,
					&testAzVolume,
					newAttachment)

				mockClientsAndAttachmentProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAzVolumeAttachment, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				azVolumeAttachment, localError := controller.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(testPrimaryAzVolumeAttachmentByVolume.Namespace).Get(context.TODO(), testPrimaryAzVolumeAttachmentByVolume.Name, metav1.GetOptions{})
				require.NoError(t, localError)
				require.Equal(t, diskv1alpha1.Attached, azVolumeAttachment.Status.State)
			},
		},
		{
			description: "[Success] Should detach volume when new primary AzVolumeAttachment is deleted.",
			request:     testPrimaryAzVolumeAttachmentRequest,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAzVolumeAttachment {
				newAttachment := testPrimaryAzVolumeAttachmentByVolume.DeepCopy()
				newAttachment.Status.State = diskv1alpha1.Attached
				now := metav1.Time{Time: metav1.Now().Add(-1000)}
				newAttachment.DeletionTimestamp = &now

				controller := NewTestAzVolumeAttachmentController(
					mockCtl,
					testNamespace,
					&testAzVolume,
					newAttachment)

				mockClientsAndAttachmentProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAzVolumeAttachment, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				azVolumeAttachment, localError := controller.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(testPrimaryAzVolumeAttachmentByVolume.Namespace).Get(context.TODO(), testPrimaryAzVolumeAttachmentByVolume.Name, metav1.GetOptions{})
				require.NoError(t, localError)
				require.Equal(t, diskv1alpha1.Detached, azVolumeAttachment.Status.State)
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

func TestAzVolumeAttachmentControllerRecover(t *testing.T) {
	tests := []struct {
		description string
		setupFunc   func(*testing.T, *gomock.Controller) *ReconcileAzVolumeAttachment
		verifyFunc  func(*testing.T, *ReconcileAzVolumeAttachment, error)
	}{
		{
			description: "[Success] Should create AzVolumeAttachment instances for VolumeAttachment of PV using Azure Disk CSI Driver.",
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAzVolumeAttachment {

				controller := NewTestAzVolumeAttachmentController(
					mockCtl,
					testNamespace,
					&testVolumeAttachment,
					&testPersistentVolume0)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAzVolumeAttachment, err error) {
				require.NoError(t, err)

				azVolumeAttachments, localErr := controller.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(testNamespace).List(context.TODO(), metav1.ListOptions{})
				require.NoError(t, localErr)
				require.Len(t, azVolumeAttachments.Items, 1)
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
