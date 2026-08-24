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

package azuredisk

import (
	"context"
	"errors"

	armcompute "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v7"
	"k8s.io/apimachinery/pkg/types"
	cloudprovider "k8s.io/cloud-provider"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/azcompute"
	azcache "sigs.k8s.io/cloud-provider-azure/pkg/cache"
	"sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

// providerDiskVMSet implements azcompute.Interface by delegating to
// provider.Cloud's cached vmSet layer. It is the flag-off (kill-switch) path
// that preserves the original cloud-provider-azure compute behavior while the
// cache-free implementation is validated on all VM topologies.
//
// It is the single place the compute path references provider.Cloud: all
// compute call sites go through azcompute.Interface, so switching the default to
// the cache-free implementation and deleting this file drops cloud-provider-azure
// from the compute path entirely (auth/config still use provider.Cloud).
type providerDiskVMSet struct {
	cloud *provider.Cloud
}

func newProviderDiskVMSet(cloud *provider.Cloud) azcompute.Interface {
	return &providerDiskVMSet{cloud: cloud}
}

func (p *providerDiskVMSet) AttachDisk(ctx context.Context, nodeName types.NodeName, diskMap map[string]*azcompute.AttachDiskOptions) error {
	vmset, err := p.cloud.GetNodeVMSet(ctx, nodeName, azcache.CacheReadTypeUnsafe)
	if err != nil {
		return err
	}
	providerMap := make(map[string]*provider.AttachDiskOptions, len(diskMap))
	for diskURI, opt := range diskMap {
		if opt == nil {
			providerMap[diskURI] = nil
			continue
		}
		providerMap[diskURI] = &provider.AttachDiskOptions{
			CachingMode:             opt.CachingMode,
			DiskName:                opt.DiskName,
			DiskEncryptionSetID:     opt.DiskEncryptionSetID,
			WriteAcceleratorEnabled: opt.WriteAcceleratorEnabled,
			Lun:                     opt.Lun,
		}
	}
	return vmset.AttachDisk(ctx, nodeName, providerMap)
}

func (p *providerDiskVMSet) DetachDisk(ctx context.Context, nodeName types.NodeName, diskMap map[string]string, forceDetach bool) error {
	vmset, err := p.cloud.GetNodeVMSet(ctx, nodeName, azcache.CacheReadTypeUnsafe)
	if err != nil {
		return err
	}
	return vmset.DetachDisk(ctx, nodeName, diskMap, forceDetach)
}

func (p *providerDiskVMSet) UpdateVM(ctx context.Context, nodeName types.NodeName) error {
	vmset, err := p.cloud.GetNodeVMSet(ctx, nodeName, azcache.CacheReadTypeUnsafe)
	if err != nil {
		return err
	}
	return vmset.UpdateVM(ctx, nodeName)
}

func (p *providerDiskVMSet) GetDataDisks(ctx context.Context, nodeName types.NodeName, crt azcache.AzureCacheReadType) ([]*armcompute.DataDisk, *string, error) {
	vmset, err := p.cloud.GetNodeVMSet(ctx, nodeName, crt)
	if err != nil {
		return nil, nil, err
	}
	return vmset.GetDataDisks(ctx, nodeName, crt)
}

func (p *providerDiskVMSet) GetNodeNameByProviderID(ctx context.Context, providerID string) (types.NodeName, error) {
	return p.cloud.VMSet.GetNodeNameByProviderID(ctx, providerID)
}

func (p *providerDiskVMSet) GetInstanceTypeByNodeName(ctx context.Context, name string) (string, error) {
	vmset, err := p.cloud.GetNodeVMSet(ctx, types.NodeName(name), azcache.CacheReadTypeUnsafe)
	if err != nil {
		return "", err
	}
	return vmset.GetInstanceTypeByNodeName(ctx, name)
}

func (p *providerDiskVMSet) GetZoneByNodeName(ctx context.Context, name string) (cloudprovider.Zone, error) {
	vmset, err := p.cloud.GetNodeVMSet(ctx, types.NodeName(name), azcache.CacheReadTypeUnsafe)
	if err != nil {
		return cloudprovider.Zone{}, err
	}
	return vmset.GetZoneByNodeName(ctx, name)
}

func (p *providerDiskVMSet) InstanceExists(ctx context.Context, nodeName types.NodeName) (bool, error) {
	if _, err := p.cloud.InstanceID(ctx, nodeName); err != nil {
		if errors.Is(err, cloudprovider.InstanceNotFound) || isInstanceNotFoundError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (p *providerDiskVMSet) DeleteCacheForNode(ctx context.Context, nodeName string) error {
	vmset, err := p.cloud.GetNodeVMSet(ctx, types.NodeName(nodeName), azcache.CacheReadTypeDefault)
	if err != nil {
		return err
	}
	return vmset.DeleteCacheForNode(ctx, nodeName)
}

var _ azcompute.Interface = (*providerDiskVMSet)(nil)
