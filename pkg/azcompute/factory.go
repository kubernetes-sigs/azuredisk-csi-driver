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

// factory.go — assembly + kube-backed node resolution.
//
// This is the single entry point that replaces provider.GetNodeVMSet: it wires
// the per-topology cache-free implementations behind the dispatcher, using a
// provider-ID resolver sourced from the Kubernetes Node object (no ARM listing,
// no cache).

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"

	"sigs.k8s.io/cloud-provider-azure/pkg/azclient"
)

// NewKubeNodeProviderIDResolver returns a cache-free NodeProviderIDResolver that
// reads spec.providerID from the Kubernetes Node object.
func NewKubeNodeProviderIDResolver(kubeClient clientset.Interface) NodeProviderIDResolver {
	return func(ctx context.Context, nodeName string) (string, error) {
		if kubeClient == nil {
			return "", fmt.Errorf("kube client is nil, cannot resolve providerID for node %q", nodeName)
		}
		node, err := kubeClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("get node %q: %w", nodeName, err)
		}
		if node.Spec.ProviderID == "" {
			return "", fmt.Errorf("node %q has no spec.providerID", nodeName)
		}
		return node.Spec.ProviderID, nil
	}
}

// NewCacheFree assembles the cache-free DiskVMSet dispatcher covering
// all Azure compute topologies (Standard, VMSSFlex, VMSS-uniform), backed by
// azclient and a kube-sourced provider-ID resolver. This is the drop-in
// replacement for provider.GetNodeVMSet.
func NewCacheFree(clientFactory azclient.ClientFactory, kubeClient clientset.Interface, isAzureStackCloud bool) Interface {
	resolver := NewKubeNodeProviderIDResolver(kubeClient)
	vmBased := NewVM(clientFactory, resolver, isAzureStackCloud)
	vmss := NewVMSS(clientFactory, resolver, isAzureStackCloud)
	classify := NewProviderIDClassifier(resolver)
	return NewDispatcher(classify, vmBased, vmss)
}
