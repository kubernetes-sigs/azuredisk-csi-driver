/*
Copyright 2024 The Kubernetes Authors.

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

package hooks

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk/mockkubeclient"
)

func TestIsNodeBeingDrained(t *testing.T) {
	testCases := []struct {
		name         string
		nodeTaintKey string
		want         bool
	}{
		{"Should recognize common eviction taint", v1.TaintNodeUnschedulable, true},
		{"Should recognize cluster autoscaler taint", clusterAutoscalerTaint, true},
		{"Should recognize Karpenter v1beta1 taint", v1beta1KarpenterTaint, true},
		{"Should recognize Karpenter v1 taint", v1KarpenterTaint, true},
		{"Should not block on generic taint", "ebs/fake-taint", false},
		{"Should not block on no taint", "", false},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var taints []v1.Taint

			if tc.nodeTaintKey != "" {
				taint := v1.Taint{
					Key:    tc.nodeTaintKey,
					Value:  "",
					Effect: v1.TaintEffectNoSchedule,
				}

				taints = append(taints, taint)
			}

			testNode := &v1.Node{
				Spec: v1.NodeSpec{
					Taints: taints,
				},
			}

			got := isNodeBeingDrained(testNode)

			if tc.want != got {
				t.Fatalf("isNodeBeingDrained returned wrong answer when node contained taint with key: %s; got: %t, want: %t", tc.nodeTaintKey, got, tc.want)
			}
		})
	}
}

func TestPreStop(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	kubeClient := mockkubeclient.NewMockInterface(ctrl)

	testCases := []struct {
		name    string
		wantErr bool
	}{
		{"Should return error when KUBE_NODE_NAME is missing", true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := PreStop(kubeClient)

			if tc.wantErr && err == nil {
				t.Fatalf("PreStop returned nil error when KUBE_NODE_NAME is missing; got: %v, want: %v", err, tc.wantErr)
			}

			if !tc.wantErr && err != nil {
				t.Fatalf("PreStop returned error when KUBE_NODE_NAME is present; got: %v, want: %v", err, tc.wantErr)
			}
		})
	}
}

func volumeAttachmentForNode(nodeName, name string) *storagev1.VolumeAttachment {
	return &storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       storagev1.VolumeAttachmentSpec{NodeName: nodeName},
	}
}

// checkVolumeAttachments is called by the initial check of waitForVolumeAttachments
// and again for every VolumeAttachment event, so several calls can find the node
// already empty. Only the first of them may close the channel: closing it twice
// panics and takes the preStop hook down with it.
func TestCheckVolumeAttachmentsSignal(t *testing.T) {
	const nodeName = "node-1"

	testCases := []struct {
		name        string
		attachments []runtime.Object
		checks      int
		wantClosed  bool
	}{
		{
			name:        "attachment for this node does not signal",
			attachments: []runtime.Object{volumeAttachmentForNode(nodeName, "va-1")},
			checks:      1,
			wantClosed:  false,
		},
		{
			// the hook only waits for the attachments of its own node
			name:        "attachment for another node still signals",
			attachments: []runtime.Object{volumeAttachmentForNode("node-2", "va-2")},
			checks:      1,
			wantClosed:  true,
		},
		{
			name:       "no attachment for this node signals",
			checks:     1,
			wantClosed: true,
		},
		{
			name:       "second empty check does not close the channel again",
			checks:     2,
			wantClosed: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			clientset := fake.NewClientset(tc.attachments...)
			signal := &attachmentDeletionSignal{allAttachmentsDeleted: make(chan struct{})}

			for range tc.checks {
				require.NotPanics(t, func() {
					err := checkVolumeAttachments(clientset, nodeName, signal)
					require.NoError(t, err)
				})
			}

			select {
			case <-signal.allAttachmentsDeleted:
				if !tc.wantClosed {
					t.Errorf("checkVolumeAttachments closed allAttachmentsDeleted while the node still has an attachment")
				}
			default:
				if tc.wantClosed {
					t.Errorf("checkVolumeAttachments did not close allAttachmentsDeleted although the node has no attachment")
				}
			}
		})
	}
}
