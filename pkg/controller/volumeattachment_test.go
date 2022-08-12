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
	"k8s.io/apimachinery/pkg/runtime"
	fakev1 "k8s.io/client-go/kubernetes/fake"
	"k8s.io/klog/v2/klogr"
	azdiskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller/mockclient"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func NewTestVolumeAttachmentController(controller *gomock.Controller, namespace string, objects ...runtime.Object) *ReconcileVolumeAttachment {
	azDiskObjs, kubeObjs := splitObjects(objects...)
	controllerSharedState := initState(mockclient.NewMockClient(controller), azdiskfakes.NewSimpleClientset(azDiskObjs...), fakev1.NewSimpleClientset(kubeObjs...), objects...)

	return &ReconcileVolumeAttachment{
		controllerRetryInfo: newRetryInfo(),
		SharedState:         controllerSharedState,
		logger:              klogr.New(),
	}
}

func TestVolumeAttachmentReconcile(t *testing.T) {
	tests := []struct {
		description string
		request     reconcile.Request
		setupFunc   func(*testing.T, *gomock.Controller) *ReconcileVolumeAttachment
		verifyFunc  func(*testing.T, *ReconcileVolumeAttachment, reconcile.Result, error)
	}{
		{
			description: "[Success] Should update AzVolumeAttachment with VolumeAttachment name.",
			request:     testVolumeAttachmentRequest,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileVolumeAttachment {
				newVolumeAttachment := testVolumeAttachment.DeepCopy()
				newVolumeAttachment.Spec.Source.PersistentVolumeName = &testPersistentVolume0Name

				newAzVolumeAttachment := testPrimaryAzVolumeAttachment0.DeepCopy()

				controller := NewTestVolumeAttachmentController(
					mockCtl,
					testNamespace,
					&testAzVolume0,
					&testPersistentVolume0,
					newVolumeAttachment,
					newAzVolumeAttachment)

				mockClients(controller.cachedClient.(*mockclient.MockClient), controller.azClient, controller.kubeClient)

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileVolumeAttachment, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				val, ok := controller.azVolumeAttachmentToVaMap.Load(testPrimaryAzVolumeAttachment0Name)
				require.True(t, ok)
				vaName := val.(string)
				require.Equal(t, vaName, testVolumeAttachmentName)
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
