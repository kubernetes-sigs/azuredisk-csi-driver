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

// dispatcher.go — the cache-free replacement for provider.GetNodeVMSet.
//
// A cluster may contain a mix of Standard (availability-set / standalone) VMs and
// VMSS-uniform instances. dispatchingDiskVMSet routes each call to the right
// per-topology Interface based on a NodeClassifier. The classifier is cache-free:
// it decides from the node's spec.providerID (VMSS provider IDs contain
// "/virtualMachineScaleSets/"), which is the same authoritative signal the VMSS
// implementation already uses.

import (
	"context"
	"fmt"
	"strings"

	armcompute "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v7"

	"k8s.io/apimachinery/pkg/types"
	cloudprovider "k8s.io/cloud-provider"

	azcache "sigs.k8s.io/cloud-provider-azure/pkg/cache"
)

// nodeTopology identifies the Azure compute topology backing a node.
type nodeTopology int

const (
	// topologyStandard covers individual VM resources: Standard (availability
	// set / standalone) VMs and VMSSFlex members. Both use the VM client and are
	// served by the same VM-based implementation (NewVM).
	topologyStandard nodeTopology = iota
	// topologyVMSS covers VMSS-uniform instances (VMSS VM client).
	topologyVMSS
)

// NodeClassifier decides which topology a node belongs to. A cache-free
// implementation reads the node's spec.providerID.
type NodeClassifier func(ctx context.Context, nodeName string) (nodeTopology, error)

// NewProviderIDClassifier returns a classifier that inspects a node's
// provider ID (resolved cache-free from the Kubernetes Node object).
func NewProviderIDClassifier(resolver NodeProviderIDResolver) NodeClassifier {
	return func(ctx context.Context, nodeName string) (nodeTopology, error) {
		providerID, err := resolver(ctx, nodeName)
		if err != nil {
			return topologyStandard, err
		}
		if strings.Contains(strings.ToLower(providerID), "/virtualmachinescalesets/") {
			return topologyVMSS, nil
		}
		return topologyStandard, nil
	}
}

// dispatchingDiskVMSet routes DiskVMSet calls to the correct per-topology
// implementation. It replaces provider.GetNodeVMSet without any caching.
type dispatchingDiskVMSet struct {
	classify NodeClassifier
	standard Interface
	vmss     Interface
}

// NewDispatcher wires the per-topology implementations behind a classifier.
func NewDispatcher(classify NodeClassifier, standard, vmss Interface) Interface {
	return &dispatchingDiskVMSet{classify: classify, standard: standard, vmss: vmss}
}

// forNode selects the DiskVMSet for a node.
func (d *dispatchingDiskVMSet) forNode(ctx context.Context, nodeName string) (Interface, error) {
	topo, err := d.classify(ctx, nodeName)
	if err != nil {
		return nil, err
	}
	switch topo {
	case topologyVMSS:
		return d.vmss, nil
	case topologyStandard:
		return d.standard, nil
	default:
		return nil, fmt.Errorf("unsupported topology %d for node %q", topo, nodeName)
	}
}

func (d *dispatchingDiskVMSet) AttachDisk(ctx context.Context, nodeName types.NodeName, diskMap map[string]*AttachDiskOptions) error {
	vmset, err := d.forNode(ctx, string(nodeName))
	if err != nil {
		return err
	}
	return vmset.AttachDisk(ctx, nodeName, diskMap)
}

func (d *dispatchingDiskVMSet) DetachDisk(ctx context.Context, nodeName types.NodeName, diskMap map[string]string, forceDetach bool) error {
	vmset, err := d.forNode(ctx, string(nodeName))
	if err != nil {
		return err
	}
	return vmset.DetachDisk(ctx, nodeName, diskMap, forceDetach)
}

func (d *dispatchingDiskVMSet) UpdateVM(ctx context.Context, nodeName types.NodeName) error {
	vmset, err := d.forNode(ctx, string(nodeName))
	if err != nil {
		return err
	}
	return vmset.UpdateVM(ctx, nodeName)
}

func (d *dispatchingDiskVMSet) GetDataDisks(ctx context.Context, nodeName types.NodeName, crt azcache.AzureCacheReadType) ([]*armcompute.DataDisk, *string, error) {
	vmset, err := d.forNode(ctx, string(nodeName))
	if err != nil {
		return nil, nil, err
	}
	return vmset.GetDataDisks(ctx, nodeName, crt)
}

func (d *dispatchingDiskVMSet) GetInstanceTypeByNodeName(ctx context.Context, name string) (string, error) {
	vmset, err := d.forNode(ctx, name)
	if err != nil {
		return "", err
	}
	return vmset.GetInstanceTypeByNodeName(ctx, name)
}

func (d *dispatchingDiskVMSet) GetZoneByNodeName(ctx context.Context, name string) (cloudprovider.Zone, error) {
	vmset, err := d.forNode(ctx, name)
	if err != nil {
		return cloudprovider.Zone{}, err
	}
	return vmset.GetZoneByNodeName(ctx, name)
}

func (d *dispatchingDiskVMSet) GetNodeNameByProviderID(ctx context.Context, providerID string) (types.NodeName, error) {
	// Dispatch directly from the provider ID; no node name is available yet.
	if strings.Contains(strings.ToLower(providerID), "/virtualmachinescalesets/") {
		return d.vmss.GetNodeNameByProviderID(ctx, providerID)
	}
	return d.standard.GetNodeNameByProviderID(ctx, providerID)
}

func (d *dispatchingDiskVMSet) DeleteCacheForNode(_ context.Context, _ string) error {
	// No cache in any backing implementation.
	return nil
}

func (d *dispatchingDiskVMSet) InstanceExists(ctx context.Context, nodeName types.NodeName) (bool, error) {
	vmset, err := d.forNode(ctx, string(nodeName))
	if err != nil {
		return false, err
	}
	return vmset.InstanceExists(ctx, nodeName)
}

// Compile-time assertion.
var _ Interface = (*dispatchingDiskVMSet)(nil)
