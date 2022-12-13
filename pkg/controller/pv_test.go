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
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	fakev1 "k8s.io/client-go/kubernetes/fake"
	"k8s.io/klog/v2/klogr"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdiskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils/mockclient"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func newTestPVController(controller *gomock.Controller, namespace string, objects ...runtime.Object) *ReconcilePV {
	azDiskObjs, kubeObjs := splitObjects(objects...)
	controllerSharedState := initState(mockclient.NewMockClient(controller), azdiskfakes.NewSimpleClientset(azDiskObjs...), fakev1.NewSimpleClientset(kubeObjs...), objects...)

	return &ReconcilePV{
		controllerRetryInfo: newRetryInfo(),
		SharedState:         controllerSharedState,
		logger:              klogr.New(),
	}
}

func TestPVControllerReconcile(t *testing.T) {
	tests := []struct {
		description string
		request     reconcile.Request
		setupFunc   func(*testing.T, *gomock.Controller) *ReconcilePV
		verifyFunc  func(*testing.T, *ReconcilePV, reconcile.Result, error)
	}{
		{
			description: "[Success] Should release AzVolume when PV is released.",
			request:     testPersistentVolume0Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcilePV {
				azVolume := testAzVolume0.DeepCopy()

				pv := testPersistentVolume0.DeepCopy()
				pv.Status.Phase = corev1.VolumeReleased

				controller := newTestPVController(
					mockCtl,
					testNamespace,
					azVolume,
					pv)

				mockClients(controller.cachedClient.(*mockclient.MockClient), controller.azClient, controller.kubeClient)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcilePV, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)
				waitErr := wait.PollImmediate(verifyCRIInterval, verifyCRITimeout, func() (bool, error) {
					azVolumeAttachments, err := controller.azClient.DiskV1beta2().AzVolumeAttachments(testNamespace).List(context.TODO(), metav1.ListOptions{})
					return len(azVolumeAttachments.Items) == 0, err
				})
				require.NoError(t, waitErr)
			},
		},
		{
			description: "[Success] Should not requeue when PV doesn't exist.",
			request:     testPersistentVolume0Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcilePV {

				controller := newTestPVController(
					mockCtl,
					testNamespace)

				mockClients(controller.cachedClient.(*mockclient.MockClient), controller.azClient, controller.kubeClient)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcilePV, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)
			},
		},
		{
			description: "[Success] Should not requeue when PV is marked for deletion and AzVolume doesn't exist.",
			request:     testPersistentVolume0Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcilePV {
				pv := testPersistentVolume0.DeepCopy()
				pv.Status.Phase = corev1.VolumeReleased
				pv.DeletionTimestamp = &metav1.Time{Time: time.Now().Add(time.Minute * -1)}

				controller := newTestPVController(
					mockCtl,
					testNamespace,
					pv)

				mockClients(controller.cachedClient.(*mockclient.MockClient), controller.azClient, controller.kubeClient)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcilePV, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)
			},
		},
		{
			description: "[Success] Should delete AzVolume when PV is deleted but AzVolume exists.",
			request:     testPersistentVolume0Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcilePV {
				azVolume := testAzVolume0.DeepCopy()

				pv := testPersistentVolume0.DeepCopy()
				azVolume.Status = azdiskv1beta2.AzVolumeStatus{
					Annotations: azureutils.AddToMap(map[string]string{}, consts.PreProvisionedVolumeAnnotation, "pre-provisioned-volume"),
				}
				controller := newTestPVController(
					mockCtl,
					testNamespace,
					azVolume,
					pv)

				mockClients(controller.cachedClient.(*mockclient.MockClient), controller.azClient, controller.kubeClient)

				err := controller.kubeClient.CoreV1().PersistentVolumes().Delete(context.TODO(), pv.Name, metav1.DeleteOptions{})
				require.NoError(t, err)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcilePV, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				waitErr := wait.PollImmediate(verifyCRIInterval, verifyCRITimeout, func() (bool, error) {
					_, err = controller.azClient.DiskV1beta2().AzVolumes(controller.config.ObjectNamespace).Get(context.Background(), testPersistentVolume0Name, metav1.GetOptions{})
					return errors.IsNotFound(err), nil
				})
				require.NoError(t, waitErr)
			},
		},
		{
			description: "[Success] Should create AzVolume when PV exists but AzVolume isn't found (for pre-provisioned volume).",
			request:     testPersistentVolume0Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcilePV {
				pv := testPersistentVolume0.DeepCopy()
				pv.Status.Phase = corev1.VolumeBound

				controller := newTestPVController(
					mockCtl,
					testNamespace,
					pv)

				mockClients(controller.cachedClient.(*mockclient.MockClient), controller.azClient, controller.kubeClient)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcilePV, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)
				azv, err := controller.azClient.DiskV1beta2().AzVolumes(controller.config.ObjectNamespace).Get(context.Background(), testPersistentVolume0Name, metav1.GetOptions{})
				require.NoError(t, err)
				require.Contains(t, azv.Status.Annotations, consts.PreProvisionedVolumeAnnotation)
			},
		},
		{
			description: "[Success] Should ignore non-csi volumes",
			request:     testPersistentVolume0Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcilePV {
				pv := testPersistentVolume0.DeepCopy()
				pv.Status.Phase = corev1.VolumeBound
				pv.Spec.CSI = nil

				controller := newTestPVController(
					mockCtl,
					testNamespace,
					pv)

				mockClients(controller.cachedClient.(*mockclient.MockClient), controller.azClient, controller.kubeClient)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcilePV, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)
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

func TestPVControllerRecover(t *testing.T) {
	tests := []struct {
		description string
		setupFunc   func(*testing.T, *gomock.Controller) *ReconcilePV
		verifyFunc  func(*testing.T, *ReconcilePV, error)
	}{
		{
			description: "[Success] Should map azVolumeName, pvName, pvClaimName.",
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcilePV {
				pv := testPersistentVolume0.DeepCopy()

				controller := newTestPVController(
					mockCtl,
					testNamespace,
					pv)

				mockClients(controller.cachedClient.(*mockclient.MockClient), controller.azClient, controller.kubeClient)

				controller.SharedState.pvToVolumeMap.Delete(testPersistentVolume0Name)
				controller.deleteVolumeAndClaim(testPersistentVolume0Name)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcilePV, err error) {
				require.NoError(t, err)

				pvClaimName := getQualifiedName(testNamespace, testPersistentVolumeClaim0Name)
				azvName, ok := controller.pvToVolumeMap.Load(testPersistentVolume0Name)
				require.True(t, ok)
				require.Equal(t, testPersistentVolume0Name, azvName)

				pvc, ok := controller.volumeToClaimMap.Load(testPersistentVolume0Name)
				require.True(t, ok)
				require.Equal(t, pvClaimName, pvc)

				azvName, ok = controller.claimToVolumeMap.Load(pvClaimName)
				require.True(t, ok)
				require.Equal(t, testPersistentVolume0Name, azvName)
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
