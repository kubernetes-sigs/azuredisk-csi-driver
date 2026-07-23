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

// Package azcompute implements the azuredisk driver's own, cache-free Azure
// compute operations: resolving the VM/VMSS instance backing a Kubernetes node
// (via spec.providerID) and performing data-disk attach/detach on it directly
// through pkg/azclient — without the caching that comes with
// sigs.k8s.io/cloud-provider-azure/pkg/provider.
package azcompute

import (
	"context"

	armcompute "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v7"
	"k8s.io/apimachinery/pkg/types"
	cloudprovider "k8s.io/cloud-provider"

	azcache "sigs.k8s.io/cloud-provider-azure/pkg/cache"
)

// AttachDiskOptions holds the parameters for attaching a single data disk.
// azcompute owns this type so the compute path no longer depends on
// cloud-provider-azure/pkg/provider; it mirrors the fields the driver sets.
type AttachDiskOptions struct {
	CachingMode             armcompute.CachingTypes
	DiskName                string
	DiskEncryptionSetID     string
	WriteAcceleratorEnabled bool
	Lun                     int32
}

// Interface is the slim compute surface the azuredisk driver needs: resolving
// the VM/VMSS backing a node and attaching/detaching its data disks. It mirrors
// the subset of the cloud-provider VMSet the driver used to call, but is
// implemented cache-free on top of pkg/azclient.
type Interface interface {
	// AttachDisk attaches the disks described by diskMap to the node's VM.
	AttachDisk(ctx context.Context, nodeName types.NodeName, diskMap map[string]*AttachDiskOptions) error
	// DetachDisk detaches the disks described by diskMap from the node's VM.
	DetachDisk(ctx context.Context, nodeName types.NodeName, diskMap map[string]string, forceDetach bool) error
	// UpdateVM triggers an update of the node's VM (used to recover from failed attach/detach).
	UpdateVM(ctx context.Context, nodeName types.NodeName) error
	// GetDataDisks returns the data disks currently attached to the node.
	GetDataDisks(ctx context.Context, nodeName types.NodeName, crt azcache.AzureCacheReadType) ([]*armcompute.DataDisk, *string, error)
	// GetNodeNameByProviderID resolves a node name from an ARM provider ID (disk.ManagedBy).
	GetNodeNameByProviderID(ctx context.Context, providerID string) (types.NodeName, error)
	// GetInstanceTypeByNodeName returns the VM size for a node.
	GetInstanceTypeByNodeName(ctx context.Context, name string) (string, error)
	// GetZoneByNodeName returns the availability zone for a node.
	GetZoneByNodeName(ctx context.Context, name string) (cloudprovider.Zone, error)
	// InstanceExists reports whether the VM backing the node still exists. It is
	// used to short-circuit detach when a node/VM is gone, without the expensive
	// cache refresh that provider.Cloud.InstanceID triggers.
	InstanceExists(ctx context.Context, nodeName types.NodeName) (bool, error)
	// DeleteCacheForNode invalidates the cached VM state for a node.
	DeleteCacheForNode(ctx context.Context, nodeName string) error
}
