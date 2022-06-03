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
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/wait"
	fakev1 "k8s.io/client-go/kubernetes/fake"
	"k8s.io/klog/v2/klogr"
	azdiskv1beta1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta1"
	azdiskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller/mockclient"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func NewTestNodeAvailabilityController(controller *gomock.Controller, namespace string, objects ...runtime.Object) *ReconcileNodeAvailability {
	diskv1alpha1Objs, kubeObjs := splitObjects(objects...)
	controllerSharedState := initState(mockclient.NewMockClient(controller), azdiskfakes.NewSimpleClientset(diskv1alpha1Objs...), fakev1.NewSimpleClientset(kubeObjs...), objects...)

	return &ReconcileNodeAvailability{
		controllerSharedState: controllerSharedState,
		logger:                klogr.New(),
	}
}

func TestNodeAvailabilityController(t *testing.T) {
	tests := []struct {
		description string
		request     reconcile.Request
		setupFunc   func(*testing.T, *gomock.Controller) *ReconcileNodeAvailability
		verifyFunc  func(*testing.T, *ReconcileNodeAvailability, reconcile.Result, error)
	}{
		{
			description: "[Success] Should create new AzVolumeReplica when new node becomes available",
			request:     testSchedulableNodeRequest,
			setupFunc: func(t *testing.T, mockCtl *gomock.Controller) *ReconcileNodeAvailability {
				newAttachment := testPrimaryAzVolumeAttachment0.DeepCopy()
				newAttachment.Status.State = azdiskv1beta1.Attached

				newVolume := testAzVolume0.DeepCopy()
				newVolume.Status.Detail = &azdiskv1beta1.AzVolumeStatusDetail{
					VolumeID: testManagedDiskURI0,
				}

				newPod := testPod1.DeepCopy()
				newPod.Status.Phase = v1.PodRunning

				controller := NewTestNodeAvailabilityController(
					mockCtl,
					testNamespace,
					newVolume,
					newAttachment,
					&testPersistentVolume0,
					&testNode0,
					&testSchedulableNode1,
					newPod,
				)

				mockClients(controller.controllerSharedState.cachedClient.(*mockclient.MockClient), controller.controllerSharedState.azClient, controller.controllerSharedState.kubeClient)
				controller.controllerSharedState.priorityReplicaRequestsQueue.Push(context.TODO(), &ReplicaRequest{VolumeName: testPersistentVolume0Name, Priority: 1})

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileNodeAvailability, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				roleReq, _ := azureutils.CreateLabelRequirements(consts.RoleLabel, selection.Equals, string(azdiskv1beta1.ReplicaRole))
				labelSelector := labels.NewSelector().Add(*roleReq)

				conditionFunc := func() (bool, error) {
					replicas, localError := controller.controllerSharedState.azClient.DiskV1beta1().AzVolumeAttachments(testPrimaryAzVolumeAttachment0.Namespace).List(context.TODO(), metav1.ListOptions{LabelSelector: labelSelector.String()})
					require.NoError(t, localError)
					require.NotNil(t, replicas)
					return len(replicas.Items) == 1, nil
				}
				err = wait.PollImmediate(verifyCRIInterval, verifyCRITimeout, conditionFunc)

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
