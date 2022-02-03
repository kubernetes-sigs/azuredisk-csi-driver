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

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	fakev1 "k8s.io/client-go/kubernetes/fake"
	diskv1alpha2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha2"
	diskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller/mockclient"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type testReconcilePV struct {
	ReconcilePV
	kubeClient kubernetes.Interface
}

func newTestPVController(controller *gomock.Controller, namespace string, objects ...runtime.Object) *testReconcilePV {
	azDiskObjs, kubeObjs := splitObjects(objects...)
	controllerSharedState := initState(objects...)

	return &testReconcilePV{
		ReconcilePV: ReconcilePV{
			client:                mockclient.NewMockClient(controller),
			azVolumeClient:        diskfakes.NewSimpleClientset(azDiskObjs...),
			controllerRetryInfo:   newRetryInfo(),
			controllerSharedState: controllerSharedState,
		},
		kubeClient: fakev1.NewSimpleClientset(kubeObjs...),
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
				azVolume.Status.Phase = diskv1alpha2.VolumeBound

				pv := testPersistentVolume0.DeepCopy()
				pv.Status.Phase = corev1.VolumeReleased

				controller := newTestPVController(
					mockCtl,
					testNamespace,
					azVolume,
					pv)

				mockClients(controller.client.(*mockclient.MockClient), controller.azVolumeClient, controller.kubeClient)

				return &controller.ReconcilePV
			},
			verifyFunc: func(t *testing.T, controller *ReconcilePV, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				azVolume, localError := controller.azVolumeClient.DiskV1alpha2().AzVolumes(testNamespace).Get(context.TODO(), testPersistentVolume0Name, metav1.GetOptions{})
				require.NoError(t, localError)
				require.Equal(t, diskv1alpha2.VolumeReleased, azVolume.Status.Phase)
			},
		},
		{
			description: "[Success] Should requeue when PV is released but AzVolume does not exist.",
			request:     testPersistentVolume0Request,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcilePV {
				pv := testPersistentVolume0.DeepCopy()
				pv.Status.Phase = corev1.VolumeReleased

				controller := newTestPVController(
					mockCtl,
					testNamespace,
					pv)

				mockClients(controller.client.(*mockclient.MockClient), controller.azVolumeClient, controller.kubeClient)

				return &controller.ReconcilePV
			},
			verifyFunc: func(t *testing.T, controller *ReconcilePV, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				azVolume, localError := controller.azVolumeClient.DiskV1alpha2().AzVolumes(testNamespace).Get(context.TODO(), testPersistentVolume0Name, metav1.GetOptions{})
				require.NoError(t, localError)
				require.Equal(t, diskv1alpha2.VolumeReleased, azVolume.Status.Phase)
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
