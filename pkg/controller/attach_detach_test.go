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

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2022-03-01/compute"
	"github.com/golang/mock/gomock"
	"github.com/pborman/uuid"
	"github.com/stretchr/testify/require"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	fakev1 "k8s.io/client-go/kubernetes/fake"
	"k8s.io/klog/v2/klogr"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdiskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils/mockclient"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller/mockattachmentprovisioner"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/provisioner"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func NewTestAttachDetachController(controller *gomock.Controller, namespace string, objects ...runtime.Object) *ReconcileAttachDetach {
	azDiskObjs, kubeObjs := splitObjects(objects...)
	controllerSharedState := initState(mockclient.NewMockClient(controller), azdiskfakes.NewSimpleClientset(azDiskObjs...), fakev1.NewSimpleClientset(kubeObjs...), objects...)

	return &ReconcileAttachDetach{
		cloudDiskAttacher: mockattachmentprovisioner.NewMockCloudDiskAttachDetacher(controller),
		stateLock:         &sync.Map{},
		retryInfo:         newRetryInfo(),
		SharedState:       controllerSharedState,
		logger:            klogr.New(),
		volumeInfos:       make(map[string]*volumeInfo),
	}
}

func mockClientsAndAttachmentProvisioner(controller *ReconcileAttachDetach) {
	mockClients(controller.cachedClient.(*mockclient.MockClient), controller.azClient, controller.kubeClient)

	controller.cloudDiskAttacher.(*mockattachmentprovisioner.MockCloudDiskAttachDetacher).EXPECT().
		PublishVolume(gomock.Any(), testManagedDiskURI0, gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, volumeID, nodeID string, volumeContext map[string]string) provisioner.CloudAttachResult {
			attachResult := provisioner.NewCloudAttachResult()
			attachResult.SetPublishContext(map[string]string{consts.LUN: "1"})
			attachResult.ResultChannel() <- nil
			close(attachResult.ResultChannel())
			return attachResult
		}).
		MaxTimes(1)

	controller.cloudDiskAttacher.(*mockattachmentprovisioner.MockCloudDiskAttachDetacher).EXPECT().
		UnpublishVolume(gomock.Any(), testManagedDiskURI0, gomock.Any()).
		DoAndReturn(func(ctx context.Context, volumeID, nodeID string) error {
			return nil
		}).
		MaxTimes(1)

	controller.cloudDiskAttacher.(*mockattachmentprovisioner.MockCloudDiskAttachDetacher).EXPECT().
		GetNodeDataDisks(gomock.Any()).
		DoAndReturn(func(nodeName types.NodeName) ([]compute.DataDisk, *string, error) {
			var disk0 = compute.DataDisk{Name: &testPersistentVolume0.Name}
			var disks = []compute.DataDisk{0: disk0}
			return disks, nil, nil
		}).AnyTimes()

	controller.cloudDiskAttacher.(*mockattachmentprovisioner.MockCloudDiskAttachDetacher).EXPECT().
		GetDiskLun(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(diskName, diskURI string, nodeName types.NodeName) (int32, *string, error) {
			return int32(1), nil, nil
		}).AnyTimes()

	controller.cloudDiskAttacher.(*mockattachmentprovisioner.MockCloudDiskAttachDetacher).EXPECT().
		CheckDiskExists(gomock.Any(), getTestDiskURI(testPersistentVolume2Name)).
		DoAndReturn(func(ctx context.Context, diskURI string) (*compute.Disk, error) {
			return nil, nil
		}).MaxTimes(1)

	controller.cloudDiskAttacher.(*mockattachmentprovisioner.MockCloudDiskAttachDetacher).EXPECT().
		CheckDiskExists(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, diskURI string) (*compute.Disk, error) {
			return &compute.Disk{
				ID:                &testPersistentVolume0Name,
				ManagedByExtended: &[]string{testNode0Name, testNode1Name},
			}, nil
		}).AnyTimes()

	controller.cloudDiskAttacher.(*mockattachmentprovisioner.MockCloudDiskAttachDetacher).EXPECT().
		GetNodeNameByProviderID(testNode0Name).
		DoAndReturn(func(providerID string) (types.NodeName, error) {
			return types.NodeName(testNode0Name), nil
		}).AnyTimes()

	controller.cloudDiskAttacher.(*mockattachmentprovisioner.MockCloudDiskAttachDetacher).EXPECT().
		GetNodeNameByProviderID(testNode1Name).
		DoAndReturn(func(providerID string) (types.NodeName, error) {
			return types.NodeName(testNode1Name), nil
		}).AnyTimes()
}

func TestAttachDetachReconcile(t *testing.T) {
	tests := []struct {
		description string
		request     reconcile.Request
		setupFunc   func(*testing.T, *gomock.Controller) *ReconcileAttachDetach
		verifyFunc  func(*testing.T, *ReconcileAttachDetach, reconcile.Result, error)
	}{
		{
			description: "[Success] Should attach volume when new primary AzVolumeAttachment is created with attach request annotation.",
			request:     testPrimaryAzVolumeAttachment0Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAttachDetach {
				newAttachment := testPrimaryAzVolumeAttachment0.DeepCopy()
				newAttachment.Status.State = azdiskv1beta2.AttachmentPending
				newAttachment.Status.Annotations = azureutils.AddToMap(newAttachment.Status.Annotations, consts.VolumeAttachmentKey, testVolumeAttachmentName)

				newVolumeAttachment := testVolumeAttachment.DeepCopy()

				controller := NewTestAttachDetachController(
					mockCtl,
					testNamespace,
					&testAzVolume0,
					newVolumeAttachment,
					newAttachment)

				controller.azVolumeAttachmentToVaMap.Store(newAttachment.Name, newVolumeAttachment.Name)
				addTestNodeInAvailableAttachmentsMap(controller.SharedState, newAttachment.Spec.NodeName, testNodeAvailableAttachmentCount)

				mockClientsAndAttachmentProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAttachDetach, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				conditionFunc := func() (bool, error) {
					azVolumeAttachment, localError := controller.azClient.DiskV1beta2().AzVolumeAttachments(testPrimaryAzVolumeAttachment0.Namespace).Get(context.TODO(), testPrimaryAzVolumeAttachment0.Name, metav1.GetOptions{})
					if localError != nil {
						return false, nil
					}
					return azVolumeAttachment.Status.State == azdiskv1beta2.Attached, nil
				}

				conditionError := wait.PollImmediate(verifyCRIInterval, verifyCRITimeout, conditionFunc)
				require.NoError(t, conditionError)
			},
		},

		{
			description: "[Failure] Should not attach volume when new primary AzVolumeAttachment is created without attach request annotation.",
			request:     testPrimaryAzVolumeAttachment0Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAttachDetach {
				newAttachment := testPrimaryAzVolumeAttachment0.DeepCopy()
				newAttachment.Annotations = azureutils.RemoveFromMap(newAttachment.Annotations, consts.VolumeAttachRequestAnnotation)
				newAttachment.Status.Annotations = azureutils.AddToMap(newAttachment.Status.Annotations, consts.VolumeAttachmentKey, testVolumeAttachmentName)

				newVolumeAttachment := testVolumeAttachment.DeepCopy()

				controller := NewTestAttachDetachController(
					mockCtl,
					testNamespace,
					&testAzVolume0,
					newVolumeAttachment,
					newAttachment)

				addTestNodeInAvailableAttachmentsMap(controller.SharedState, newAttachment.Spec.NodeName, testNodeAvailableAttachmentCount)
				mockClientsAndAttachmentProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAttachDetach, result reconcile.Result, err error) {
				require.False(t, result.Requeue)

				conditionFunc := func() (bool, error) {
					azVolumeAttachment, localError := controller.azClient.DiskV1beta2().AzVolumeAttachments(testPrimaryAzVolumeAttachment0.Namespace).Get(context.TODO(), testPrimaryAzVolumeAttachment0.Name, metav1.GetOptions{})
					return azVolumeAttachment.Status.State != azdiskv1beta2.Attached && azVolumeAttachment.Status.State != azdiskv1beta2.Attaching, localError
				}

				conditionError := wait.PollImmediate(verifyCRIInterval, verifyCRITimeout, conditionFunc)
				require.NoError(t, conditionError)
			},
		},
		{
			description: "[Success] Should detach volume when new primary AzVolumeAttachment is marked with detach request annotation.",
			request:     testPrimaryAzVolumeAttachment0Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAttachDetach {
				newAttachment := testPrimaryAzVolumeAttachment0.DeepCopy()
				newAttachment.Status.State = azdiskv1beta2.Attached
				newAttachment.Status.Annotations = map[string]string{consts.VolumeDetachRequestAnnotation: "crdProvisioner"}
				newAttachment.Finalizers = []string{consts.AzVolumeAttachmentFinalizer}

				controller := NewTestAttachDetachController(
					mockCtl,
					testNamespace,
					&testAzVolume0,
					newAttachment)

				addTestNodeInAvailableAttachmentsMap(controller.SharedState, newAttachment.Spec.NodeName, testNodeAvailableAttachmentCount)
				mockClientsAndAttachmentProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAttachDetach, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				conditionFunc := func() (bool, error) {
					_, localError := controller.azClient.DiskV1beta2().AzVolumeAttachments(testPrimaryAzVolumeAttachment0.Namespace).Get(context.TODO(), testPrimaryAzVolumeAttachment0.Name, metav1.GetOptions{})
					return apiErrors.IsNotFound(localError), nil
				}

				conditionError := wait.PollImmediate(verifyCRIInterval, verifyCRITimeout, conditionFunc)
				require.NoError(t, conditionError)
			},
		},
		{
			description: "[Success] Should remove finalizer from AzVolumeAttachment when it is marked with deletion timestamp.",
			request:     testPrimaryAzVolumeAttachment0Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAttachDetach {
				newAttachment := testPrimaryAzVolumeAttachment0.DeepCopy()
				newAttachment.Finalizers = []string{consts.AzVolumeAttachmentFinalizer}
				now := metav1.Time{Time: metav1.Now().Add(-1000)}
				newAttachment.DeletionTimestamp = &now

				controller := NewTestAttachDetachController(
					mockCtl,
					testNamespace,
					&testAzVolume0,
					newAttachment)

				mockClientsAndAttachmentProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAttachDetach, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				conditionFunc := func() (bool, error) {
					azVolumeAttachment, localError := controller.azClient.DiskV1beta2().AzVolumeAttachments(testPrimaryAzVolumeAttachment0.Namespace).Get(context.TODO(), testPrimaryAzVolumeAttachment0.Name, metav1.GetOptions{})
					return len(azVolumeAttachment.Finalizers) == 0, localError
				}

				conditionError := wait.PollImmediate(verifyCRIInterval, verifyCRITimeout, conditionFunc)
				require.NoError(t, conditionError)
			},
		},
		{
			description: "[Success] Should promote AzVolumeAttachment upon request.",
			request:     testReplicaAzVolumeAttachmentRequest,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAttachDetach {
				newAttachment := testReplicaAzVolumeAttachment.DeepCopy()
				newAttachment.Status.Detail = &azdiskv1beta2.AzVolumeAttachmentStatusDetail{
					PublishContext: map[string]string{},
					Role:           azdiskv1beta2.ReplicaRole,
				}
				newAttachment.Labels = map[string]string{consts.RoleLabel: string(azdiskv1beta2.PrimaryRole)}
				newAttachment.Spec.RequestedRole = azdiskv1beta2.PrimaryRole
				newAttachment.Status.State = azdiskv1beta2.Attached
				newAttachment.Status.Annotations = azureutils.AddToMap(newAttachment.Status.Annotations, consts.VolumeAttachmentKey, testVolumeAttachmentName)

				newVolumeAttachment := testVolumeAttachment.DeepCopy()

				controller := NewTestAttachDetachController(
					mockCtl,
					testNamespace,
					&testAzVolume0,
					newVolumeAttachment,
					newAttachment)

				controller.azVolumeAttachmentToVaMap.Store(newAttachment.Name, newVolumeAttachment.Name)

				mockClientsAndAttachmentProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAttachDetach, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				azVolumeAttachment, localError := controller.azClient.DiskV1beta2().AzVolumeAttachments(testReplicaAzVolumeAttachment.Namespace).Get(context.TODO(), testReplicaAzVolumeAttachment.Name, metav1.GetOptions{})
				require.NoError(t, localError)
				require.NotNil(t, azVolumeAttachment)
				require.NotNil(t, azVolumeAttachment.Status.Detail)
				require.Equal(t, azVolumeAttachment.Status.Detail.Role, azdiskv1beta2.PrimaryRole)
			},
		},
		{
			description: "[Success] Should demote AzVolumeAttachment upon request.",
			request:     testPrimaryAzVolumeAttachment0Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAttachDetach {
				newAttachment := testPrimaryAzVolumeAttachment0.DeepCopy()
				newAttachment.Status.Detail = &azdiskv1beta2.AzVolumeAttachmentStatusDetail{
					PublishContext: map[string]string{},
					Role:           azdiskv1beta2.PrimaryRole,
				}
				newAttachment.Labels = map[string]string{consts.RoleLabel: string(azdiskv1beta2.ReplicaRole)}
				newAttachment.Spec.RequestedRole = azdiskv1beta2.ReplicaRole
				newAttachment.Status.State = azdiskv1beta2.Attached
				newAttachment.Status.Annotations = azureutils.AddToMap(newAttachment.Status.Annotations, consts.VolumeAttachmentKey, testVolumeAttachmentName)

				newVolumeAttachment := testVolumeAttachment.DeepCopy()

				controller := NewTestAttachDetachController(
					mockCtl,
					testNamespace,
					&testAzVolume0,
					newVolumeAttachment,
					newAttachment)

				controller.azVolumeAttachmentToVaMap.Store(newAttachment.Name, newVolumeAttachment.Name)

				mockClientsAndAttachmentProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAttachDetach, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				azVolumeAttachment, localError := controller.azClient.DiskV1beta2().AzVolumeAttachments(testPrimaryAzVolumeAttachment0.Namespace).Get(context.TODO(), testPrimaryAzVolumeAttachment0.Name, metav1.GetOptions{})
				require.NoError(t, localError)
				require.NotNil(t, azVolumeAttachment)
				require.NotNil(t, azVolumeAttachment.Status.Detail)
				require.Equal(t, azVolumeAttachment.Status.Detail.Role, azdiskv1beta2.ReplicaRole)
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

func TestAttachDetachRecover(t *testing.T) {
	tests := []struct {
		description string
		setupFunc   func(*testing.T, *gomock.Controller) *ReconcileAttachDetach
		verifyFunc  func(*testing.T, *ReconcileAttachDetach, error)
	}{
		{
			description: "[Success] Should create AzVolumeAttachment instances for VolumeAttachment of PV using Azure Disk CSI Driver.",
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAttachDetach {

				controller := NewTestAttachDetachController(
					mockCtl,
					testNamespace,
					&testVolumeAttachment,
					&testNode1,
					&testPersistentVolume0)

				mockClientsAndAttachmentProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAttachDetach, err error) {
				require.NoError(t, err)

				azVolumeAttachments, localErr := controller.azClient.DiskV1beta2().AzVolumeAttachments(testNamespace).List(context.TODO(), metav1.ListOptions{})
				require.NoError(t, localErr)
				require.Len(t, azVolumeAttachments.Items, 2)

				azVolumeAttachment := azVolumeAttachments.Items[1]
				require.NotNil(t, azVolumeAttachment.Status.Detail)
				require.NotNil(t, azVolumeAttachment.Status.Detail.PublishContext)
				require.Equal(t, azVolumeAttachment.Status.Detail.PublishContext[azureconstants.LUN], "1")
				require.NotNil(t, azVolumeAttachment.Status.Detail.Role)
				require.Equal(t, azVolumeAttachment.Status.Detail.Role, v1beta2.Role("Replica"))
				require.NotNil(t, azVolumeAttachment.Status.State)
				require.Equal(t, azVolumeAttachment.Status.State, v1beta2.AzVolumeAttachmentAttachmentState("Attached"))
			},
		},
		{
			description: "[Success] Should create AzVolumeAttachment instances with Replica role using Azure Disk CSI Driver.",
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAttachDetach {

				controller := NewTestAttachDetachController(
					mockCtl,
					testNamespace,
					&testVolumeAttachment,
					&testNode1,
					&testPersistentVolume0)

				mockClientsAndAttachmentProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAttachDetach, err error) {
				require.NoError(t, err)

				azVolumeAttachments, localErr := controller.azClient.DiskV1beta2().AzVolumeAttachments(testNamespace).List(context.TODO(), metav1.ListOptions{})
				require.NoError(t, localErr)
				require.Len(t, azVolumeAttachments.Items, 2)

				azVolumeAttachment := azVolumeAttachments.Items[1]
				require.NotNil(t, azVolumeAttachment.Status.Detail)
				require.NotNil(t, azVolumeAttachment.Status.Detail.PublishContext)
				require.Equal(t, azVolumeAttachment.Status.Detail.PublishContext[azureconstants.LUN], "1")
				require.NotNil(t, azVolumeAttachment.Status.Detail.Role)
				require.Equal(t, azVolumeAttachment.Status.Detail.Role, v1beta2.Role("Replica"))
				require.NotNil(t, azVolumeAttachment.Status.State)
				require.Equal(t, azVolumeAttachment.Status.State, v1beta2.AzVolumeAttachmentAttachmentState("Attached"))
			},
		},
		{
			description: "[Success] Should convert existing AzVolumeAttachment instances if using older api version.",
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAttachDetach {
				azVolumeAttachment := testPrimaryAzVolumeAttachment0.DeepCopy()
				convertToV1Beta1(azVolumeAttachment)
				azVolumeAttachment.Annotations = azureutils.AddToMap(azVolumeAttachment.Annotations, "testKey", "testValue")
				testMap := map[string]string{"testKey": "testValue"}
				azVolumeAttachment.Spec.VolumeContext = testMap
				azVolumeAttachment.Status.Detail = &azdiskv1beta2.AzVolumeAttachmentStatusDetail{
					PublishContext: testMap,
				}

				controller := NewTestAttachDetachController(
					mockCtl,
					testNamespace,
					&testVolumeAttachment,
					azVolumeAttachment,
					&testPersistentVolume0)

				mockClientsAndAttachmentProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAttachDetach, err error) {
				require.NoError(t, err)

				azVolumeAttachments, localErr := controller.azClient.DiskV1beta2().AzVolumeAttachments(testNamespace).List(context.TODO(), metav1.ListOptions{})
				require.NoError(t, localErr)
				require.Len(t, azVolumeAttachments.Items, 1)

				azVolumeAttachment := azVolumeAttachments.Items[0]
				annotations := azVolumeAttachment.Annotations
				require.Equal(t, annotations[consts.APIVersion], azdiskv1beta2.APIVersion)

				annotations = azVolumeAttachment.Status.Annotations
				require.Equal(t, annotations["testKey"], "testValue")

				require.NotNil(t, azVolumeAttachment.Spec.VolumeContext)
				require.Equal(t, azVolumeAttachment.Spec.VolumeContext["testKey"], "testValue")

				require.NotNil(t, azVolumeAttachment.Status.Detail)
				require.NotNil(t, azVolumeAttachment.Status.Detail.PublishContext)
				require.Equal(t, azVolumeAttachment.Status.Detail.PublishContext[azureconstants.LUN], "1")
			},
		},
		{
			description: "[Success] Should update AzVolumeAttachment CRIs to right state",
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileAttachDetach {
				newAzVolumeAttachment2 := testPrimaryAzVolumeAttachment2.DeepCopy()
				newAzVolumeAttachment2.Status.State = azdiskv1beta2.Attaching
				newAzVolumeAttachment2.Status.Detail = &azdiskv1beta2.AzVolumeAttachmentStatusDetail{}
				newAzVolumeAttachment2.Status.Error = &azdiskv1beta2.AzError{}

				newAzVolumeAttachment1 := testPrimaryAzVolumeAttachment1.DeepCopy()
				newAzVolumeAttachment1.Status.State = azdiskv1beta2.Detaching

				controller := NewTestAttachDetachController(
					mockCtl,
					testNamespace,
					newAzVolumeAttachment2,
					newAzVolumeAttachment1)

				mockClientsAndAttachmentProvisioner(controller)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileAttachDetach, err error) {
				require.NoError(t, err)

				azVolumeAttachment, localErr := controller.azClient.DiskV1beta2().AzVolumeAttachments(testNamespace).Get(context.TODO(), testPrimaryAzVolumeAttachment2Name, metav1.GetOptions{})
				require.NoError(t, localErr)
				require.Equal(t, azVolumeAttachment.Status.State, azdiskv1beta2.AttachmentPending)
				require.Nil(t, azVolumeAttachment.Status.Detail)
				require.Nil(t, azVolumeAttachment.Status.Error)
				require.Contains(t, azVolumeAttachment.Status.Annotations, consts.RecoverAnnotation)

				azVolumeAttachment, localErr = controller.azClient.DiskV1beta2().AzVolumeAttachments(testNamespace).Get(context.TODO(), testPrimaryAzVolumeAttachment1Name, metav1.GetOptions{})
				require.NoError(t, localErr)
				require.Equal(t, azVolumeAttachment.Status.State, azdiskv1beta2.Attached)
				require.Contains(t, azVolumeAttachment.Status.Annotations, consts.RecoverAnnotation)
			},
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			mockCtl := gomock.NewController(t)
			defer mockCtl.Finish()
			controller := tt.setupFunc(t, mockCtl)
			err := controller.Recover(context.TODO(), uuid.NewUUID().String())
			tt.verifyFunc(t, controller, err)
		})
	}
}
