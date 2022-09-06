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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	fakev1 "k8s.io/client-go/kubernetes/fake"
	"k8s.io/klog/v2/klogr"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdiskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller/mockclient"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller/mockvolumeprovisioner"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func NewTestAzVolumeController(controller *gomock.Controller, namespace string, objects ...runtime.Object) *ReconcileAzVolume {
	azDiskObjs, kubeObjs := splitObjects(objects...)
	controllerSharedState := initState(mockclient.NewMockClient(controller), azdiskfakes.NewSimpleClientset(azDiskObjs...), fakev1.NewSimpleClientset(kubeObjs...), objects...)

	return &ReconcileAzVolume{
		volumeProvisioner: mockvolumeprovisioner.NewMockVolumeProvisioner(controller),
		stateLock:         &sync.Map{},
		retryInfo:         newRetryInfo(),
		SharedState:       controllerSharedState,
		logger:            klogr.New(),
	}
}

func mockClientsAndVolumeProvisioner(controller *ReconcileAzVolume) {
	mockClients(controller.cachedClient.(*mockclient.MockClient), controller.azClient, controller.kubeClient)

	controller.volumeProvisioner.(*mockvolumeprovisioner.MockVolumeProvisioner).EXPECT().
		CreateVolume(gomock.Any(), testPersistentVolume0Name, gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			ctx context.Context,
			volumeName string,
			capacityRange *azdiskv1beta2.CapacityRange,
			volumeCapabilities []azdiskv1beta2.VolumeCapability,
			parameters map[string]string,
			secrets map[string]string,
			volumeContentSource *azdiskv1beta2.ContentVolumeSource,
			accessibilityTopology *azdiskv1beta2.TopologyRequirement) (*azdiskv1beta2.AzVolumeStatusDetail, error) {
			return &azdiskv1beta2.AzVolumeStatusDetail{
				VolumeID:      testManagedDiskURI0,
				VolumeContext: parameters,
				CapacityBytes: capacityRange.RequiredBytes,
				ContentSource: volumeContentSource,
			}, nil
		}).
		MaxTimes(1)
	controller.volumeProvisioner.(*mockvolumeprovisioner.MockVolumeProvisioner).EXPECT().
		DeleteVolume(gomock.Any(), testManagedDiskURI0, gomock.Any()).
		Return(nil).
		MaxTimes(1)
	controller.volumeProvisioner.(*mockvolumeprovisioner.MockVolumeProvisioner).EXPECT().
		ExpandVolume(gomock.Any(), testManagedDiskURI0, gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			ctx context.Context,
			volumeID string,
			capacityRange *azdiskv1beta2.CapacityRange,
			secrets map[string]string) (*azdiskv1beta2.AzVolumeStatusDetail, error) {
			volumeName, err := azureutils.GetDiskName(volumeID)
			if err != nil {
				return nil, err
			}
			azVolume, err := controller.azClient.DiskV1beta2().AzVolumes(testNamespace).Get(ctx, volumeName, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}

			azVolumeStatusParams := azVolume.Status.Detail.DeepCopy()
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
			description: "[Success] Should delete AzVolume from operation queue if it's not found.",
			request:     testAzVolume0Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAzVolume {
				controller := NewTestAzVolumeController(
					mockCtl,
					testNamespace)

				mockClientsAndVolumeProvisioner(controller)
				controller.volumeOperationQueues.Store(testPersistentVolume0Name, newLockableEntry(newOperationQueue()))

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAzVolume, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				_, ok := controller.volumeOperationQueues.Load(testPersistentVolume0Name)
				require.False(t, ok)
			},
		},
		{
			description: "[Success] Should create a volume when a new AzVolume instance is created.",
			request:     testAzVolume0Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAzVolume {
				azVolume := testAzVolume0.DeepCopy()

				azVolume.Status.State = azdiskv1beta2.VolumeOperationPending

				controller := NewTestAzVolumeController(
					mockCtl,
					testNamespace,
					azVolume)

				mockClientsAndVolumeProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAzVolume, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				conditionFunc := func() (bool, error) {
					azVolume, localError := controller.azClient.DiskV1beta2().AzVolumes(testAzVolume0.Namespace).Get(context.TODO(), testAzVolume0.Name, metav1.GetOptions{})
					if localError != nil {
						return false, nil
					}
					return azVolume.Status.State == azdiskv1beta2.VolumeCreated, nil
				}
				conditionError := wait.PollImmediate(verifyCRIInterval, verifyCRITimeout, conditionFunc)
				require.NoError(t, conditionError)
			},
		},
		{
			description: "[Success] Should expand a volume when a AzVolume Spec and Status report different sizes.",
			request:     testAzVolume0Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAzVolume {
				azVolume := testAzVolume0.DeepCopy()

				azVolume.Status.Detail = &azdiskv1beta2.AzVolumeStatusDetail{
					VolumeID:      testManagedDiskURI0,
					CapacityBytes: azVolume.Spec.CapacityRange.RequiredBytes,
				}
				azVolume.Spec.CapacityRange.RequiredBytes *= 2
				azVolume.Status.State = azdiskv1beta2.VolumeCreated

				controller := NewTestAzVolumeController(
					mockCtl,
					testNamespace,
					azVolume)

				mockClientsAndVolumeProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAzVolume, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)
				conditionFunc := func() (bool, error) {
					azVolume, localError := controller.azClient.DiskV1beta2().AzVolumes(testAzVolume0.Namespace).Get(context.TODO(), testAzVolume0.Name, metav1.GetOptions{})
					if localError != nil {
						return false, nil
					}
					return azVolume.Status.Detail.CapacityBytes == azVolume.Spec.CapacityRange.RequiredBytes, nil
				}
				conditionError := wait.PollImmediate(verifyCRIInterval, verifyCRITimeout, conditionFunc)
				require.NoError(t, conditionError)
			},
		},
		{
			description: "[Success] Should delete a volume when a AzVolume is marked for deletion.",
			request:     testAzVolume0Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAzVolume {
				azVolume := testAzVolume0.DeepCopy()

				azVolume.Status.Annotations = map[string]string{
					consts.VolumeDeleteRequestAnnotation: "cloud-delete-volume",
				}
				azVolume.Finalizers = []string{consts.AzVolumeFinalizer}
				azVolume.Status.Detail = &azdiskv1beta2.AzVolumeStatusDetail{
					VolumeID:      testManagedDiskURI0,
					CapacityBytes: azVolume.Spec.CapacityRange.RequiredBytes,
				}
				now := metav1.Time{Time: metav1.Now().Add(-1000)}
				azVolume.ObjectMeta.DeletionTimestamp = &now
				azVolume.Status.State = azdiskv1beta2.VolumeCreated

				controller := NewTestAzVolumeController(
					mockCtl,
					testNamespace,
					azVolume)

				mockClientsAndVolumeProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAzVolume, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)
				conditionFunc := func() (bool, error) {
					azVolume, localError := controller.azClient.DiskV1beta2().AzVolumes(testAzVolume0.Namespace).Get(context.TODO(), testAzVolume0.Name, metav1.GetOptions{})
					return len(azVolume.Finalizers) == 0, localError
				}
				conditionError := wait.PollImmediate(verifyCRIInterval, verifyCRITimeout, conditionFunc)
				require.NoError(t, conditionError)
			},
		},
		{
			description: "[Success] Should delete replica volume attachments and delete AzVolume respectively",
			request:     testAzVolume0Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAzVolume {
				azVolume := testAzVolume0.DeepCopy()

				azVolume.Status.Annotations = map[string]string{
					consts.VolumeDeleteRequestAnnotation: "cloud-delete-volume",
				}
				azVolume.Finalizers = []string{consts.AzVolumeFinalizer}
				azVolume.Status.Detail = &azdiskv1beta2.AzVolumeStatusDetail{
					VolumeID:      testManagedDiskURI0,
					CapacityBytes: azVolume.Spec.CapacityRange.RequiredBytes,
				}
				now := metav1.Time{Time: metav1.Now().Add(-1000)}
				azVolume.ObjectMeta.DeletionTimestamp = &now
				azVolume.Status.State = azdiskv1beta2.VolumeCreated

				controller := NewTestAzVolumeController(
					mockCtl,
					testNamespace,
					azVolume,
					&testReplicaAzVolumeAttachment)

				mockClientsAndVolumeProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAzVolume, result reconcile.Result, err error) {
				require.NoError(t, err)
				req, err := azureutils.CreateLabelRequirements(consts.VolumeNameLabel, selection.Equals, testPersistentVolume0Name)
				require.NoError(t, err)
				labelSelector := labels.NewSelector().Add(*req)
				checkAzVolumeAttachmentDeletion := func() (bool, error) {
					var attachments azdiskv1beta2.AzVolumeAttachmentList
					err := controller.cachedClient.List(context.Background(), &attachments, &client.ListOptions{LabelSelector: labelSelector})
					return len(attachments.Items) == 0, err
				}
				err = wait.PollImmediate(verifyCRIInterval, verifyCRITimeout, checkAzVolumeAttachmentDeletion)
				require.NoError(t, err)
				checkAzVolumeDeletion := func() (bool, error) {
					var azVolume azdiskv1beta2.AzVolume
					err := controller.cachedClient.Get(context.Background(), types.NamespacedName{Namespace: controller.objectNamespace, Name: testPersistentVolume0Name}, &azVolume)
					return len(azVolume.Finalizers) == 0, err
				}
				err = wait.PollImmediate(verifyCRIInterval, verifyCRITimeout, checkAzVolumeDeletion)
				require.NoError(t, err)
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
		setupFunc   func(*testing.T, *gomock.Controller) *ReconcileAzVolume
		verifyFunc  func(*testing.T, *ReconcileAzVolume, error)
	}{
		{
			description: "[Success] Should create AzVolume instances for PersistentVolumes using Azure Disk CSI Driver.",
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAzVolume {
				azVolume := testAzVolume0.DeepCopy()

				azVolume.Status.State = azdiskv1beta2.VolumeOperationPending

				controller := NewTestAzVolumeController(
					mockCtl,
					testNamespace,
					&testStorageClass,
					&testPersistentVolume0,
					&testPersistentVolume1)

				mockClientsAndVolumeProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAzVolume, err error) {
				require.NoError(t, err)
				azVolumes, err := controller.azClient.DiskV1beta2().AzVolumes(testNamespace).List(context.TODO(), metav1.ListOptions{})
				require.NoError(t, err)
				require.Len(t, azVolumes.Items, 2)
			},
		},
		{
			description: "[Success] Should update AzVolume CRIs to right state",
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAzVolume {
				newAzVolume0 := testAzVolume0.DeepCopy()
				newAzVolume0.Status.State = azdiskv1beta2.VolumeCreating

				newAzVolume1 := testAzVolume1.DeepCopy()
				newAzVolume1.Status.State = azdiskv1beta2.VolumeDeleting

				controller := NewTestAzVolumeController(
					mockCtl,
					testNamespace,
					newAzVolume0,
					newAzVolume1)

				mockClientsAndVolumeProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAzVolume, err error) {
				require.NoError(t, err)

				azVolume, localErr := controller.azClient.DiskV1beta2().AzVolumes(testNamespace).Get(context.TODO(), testPersistentVolume0Name, metav1.GetOptions{})
				require.NoError(t, localErr)
				require.Equal(t, azVolume.Status.State, azdiskv1beta2.VolumeOperationPending)
				require.Contains(t, azVolume.Status.Annotations, consts.RecoverAnnotation)

				azVolume, localErr = controller.azClient.DiskV1beta2().AzVolumes(testNamespace).Get(context.TODO(), testPersistentVolume1Name, metav1.GetOptions{})
				require.NoError(t, localErr)
				require.Equal(t, azVolume.Status.State, azdiskv1beta2.VolumeCreated)
				require.Contains(t, azVolume.Status.Annotations, consts.RecoverAnnotation)
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
