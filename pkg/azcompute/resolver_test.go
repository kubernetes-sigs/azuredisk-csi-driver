/*
Copyright 2026 The Kubernetes Authors.

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

package azcompute

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const (
	testStandardProviderID = "azure:///subscriptions/sub1/resourceGroups/rg1/providers/Microsoft.Compute/virtualMachines/aks-nodepool1-000000"
	testVMSSProviderID     = "azure:///subscriptions/sub1/resourceGroups/rg1/providers/Microsoft.Compute/virtualMachineScaleSets/aks-nodepool1-vmss/virtualMachines/3"
)

func TestParseVMProviderID(t *testing.T) {
	tests := []struct {
		name       string
		providerID string
		wantRG     string
		wantVM     string
		wantErr    bool
	}{
		{
			name:       "standard / flex VM",
			providerID: testStandardProviderID,
			wantRG:     "rg1",
			wantVM:     "aks-nodepool1-000000",
		},
		{
			name:       "VMSS uniform provider ID is not a VM resource",
			providerID: testVMSSProviderID,
			wantErr:    true,
		},
		{
			name:       "garbage",
			providerID: "not-a-provider-id",
			wantErr:    true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg, vm, err := parseVMProviderID(tc.providerID)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.wantRG, rg)
			assert.Equal(t, tc.wantVM, vm)
		})
	}
}

func TestParseVMSSProviderID(t *testing.T) {
	tests := []struct {
		name       string
		providerID string
		want       vmssVMRef
		wantErr    bool
	}{
		{
			name:       "VMSS uniform instance",
			providerID: testVMSSProviderID,
			want:       vmssVMRef{resourceGroup: "rg1", scaleSet: "aks-nodepool1-vmss", instanceID: "3"},
		},
		{
			name:       "standard VM provider ID is not a VMSS instance",
			providerID: testStandardProviderID,
			wantErr:    true,
		},
		{
			name:       "garbage",
			providerID: "not-a-provider-id",
			wantErr:    true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := parseVMSSProviderID(tc.providerID)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.want, ref)
		})
	}
}

func TestProviderIDNodeClassifier(t *testing.T) {
	tests := []struct {
		name       string
		providerID string
		want       nodeTopology
	}{
		{name: "standard", providerID: testStandardProviderID, want: topologyStandard},
		{name: "vmss", providerID: testVMSSProviderID, want: topologyVMSS},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resolver := func(context.Context, string) (string, error) { return tc.providerID, nil }
			classify := NewProviderIDClassifier(resolver)
			got, err := classify(context.Background(), "node")
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestKubeNodeProviderIDResolver(t *testing.T) {
	t.Run("resolves provider ID from node spec", func(t *testing.T) {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node1"},
			Spec:       corev1.NodeSpec{ProviderID: testStandardProviderID},
		}
		client := fake.NewSimpleClientset(node)
		resolver := NewKubeNodeProviderIDResolver(client)

		got, err := resolver(context.Background(), "node1")
		assert.NoError(t, err)
		assert.Equal(t, testStandardProviderID, got)
	})

	t.Run("errors when provider ID is empty", func(t *testing.T) {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}}
		client := fake.NewSimpleClientset(node)
		resolver := NewKubeNodeProviderIDResolver(client)

		_, err := resolver(context.Background(), "node1")
		assert.Error(t, err)
	})

	t.Run("errors when node is missing", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		resolver := NewKubeNodeProviderIDResolver(client)

		_, err := resolver(context.Background(), "does-not-exist")
		assert.Error(t, err)
	})

	t.Run("errors when kube client is nil", func(t *testing.T) {
		resolver := NewKubeNodeProviderIDResolver(nil)
		_, err := resolver(context.Background(), "node1")
		assert.Error(t, err)
	})
}
