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
	"k8s.io/apimachinery/pkg/util/wait"
	fakev1 "k8s.io/client-go/kubernetes/fake"
	diskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller/mockclient"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func newTestPVController(controller *gomock.Controller, namespace string, objects ...runtime.Object) *ReconcilePV {
	azDiskObjs, kubeObjs := splitObjects(objects...)
	controllerSharedState := initState(mockclient.NewMockClient(controller), diskfakes.NewSimpleClientset(azDiskObjs...), fakev1.NewSimpleClientset(kubeObjs...), objects...)

	return &ReconcilePV{
		controllerRetryInfo:   newRetryInfo(),
		controllerSharedState: controllerSharedState,
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
					&testReplicaAzVolumeAttachment,
					pv)

				mockClients(controller.controllerSharedState.cachedClient.(*mockclient.MockClient), controller.controllerSharedState.azClient, controller.controllerSharedState.kubeClient)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcilePV, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)
				waitErr := wait.PollImmediate(verifyCRIInterval, verifyCRITimeout, func() (bool, error) {
					azVolumeAttachments, err := controller.controllerSharedState.azClient.DiskV1beta1().AzVolumeAttachments(testNamespace).List(context.TODO(), metav1.ListOptions{})
					return len(azVolumeAttachments.Items) == 0, err
				})
				require.NoError(t, waitErr)
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
