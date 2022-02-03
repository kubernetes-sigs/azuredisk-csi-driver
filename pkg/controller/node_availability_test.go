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
	diskv1alpha2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha2"
	diskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller/mockclient"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func NewTestNodeAvailabilityController(controller *gomock.Controller, namespace string, objects ...runtime.Object) *ReconcileNodeAvailability {
	diskv1alpha1Objs, kubeObjs := splitObjects(objects...)
	controllerSharedState := initState(objects...)

	return &ReconcileNodeAvailability{
		client:                mockclient.NewMockClient(controller),
		azVolumeClient:        diskfakes.NewSimpleClientset(diskv1alpha1Objs...),
		controllerSharedState: controllerSharedState,
		kubeClient:            fakev1.NewSimpleClientset(kubeObjs...),
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
				newAttachment.Status.State = diskv1alpha2.Attached

				newVolume := testAzVolume0.DeepCopy()
				newVolume.Status.Detail = &diskv1alpha2.AzVolumeStatusDetail{
					ResponseObject: &diskv1alpha2.AzVolumeStatusParams{
						VolumeID: testManagedDiskURI0,
					},
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

				mockClients(controller.client.(*mockclient.MockClient), controller.azVolumeClient, controller.kubeClient)
				controller.controllerSharedState.priorityReplicaRequestsQueue.Push(&ReplicaRequest{VolumeName: testPersistentVolume0Name, Priority: 1})

				return controller
			},
			verifyFunc: func(t *testing.T, controller *ReconcileNodeAvailability, result reconcile.Result, err error) {
				require.NoError(t, err)
				require.False(t, result.Requeue)

				roleReq, _ := azureutils.CreateLabelRequirements(consts.RoleLabel, selection.Equals, string(diskv1alpha2.ReplicaRole))
				labelSelector := labels.NewSelector().Add(*roleReq)

				conditionFunc := func() (bool, error) {
					replicas, localError := controller.azVolumeClient.DiskV1alpha2().AzVolumeAttachments(testPrimaryAzVolumeAttachment0.Namespace).List(context.TODO(), metav1.ListOptions{LabelSelector: labelSelector.String()})
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
