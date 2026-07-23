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

// vm.go — a cache-free Interface implemented directly on pkg/azclient.
//
// This demonstrates owning the attach/detach + node-resolution path WITHOUT any
// cloud-provider-azure caching:
//   - every node lookup is a direct per-node VirtualMachines.Get (no LIST, no TimedCache)
//   - DeleteCacheForNode is a no-op (there is no cache to invalidate)
//
// Scope: the Standard / single-VM (availability set) topology,
// which mirrors provider/azure_controller_standard.go. VMSS and VMSSFlex are the
// remaining work and are called out with TODOs; they follow the same shape but
// resolve the VM via the VMSS VM client instead of the VM client.
//
// The auth / throttling / retry plumbing is still provided by azclient (that is
// the layer we intentionally keep). Only the caching is removed.

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	armcompute "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v7"

	"k8s.io/apimachinery/pkg/types"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/cloud-provider-azure/pkg/azclient"
	azcache "sigs.k8s.io/cloud-provider-azure/pkg/cache"
)

// Parsers for a virtual-machines provider ID of the form:
//
//	azure:///subscriptions/<sub>/resourceGroups/<rg>/providers/Microsoft.Compute/virtualMachines/<vm>
//
// This shape is shared by Standard (availability set / standalone) VMs and by
// VMSSFlex members, which are individual VM resources. That is why a single
// VM-client implementation covers both topologies.
var (
	vmResourceGroupRE = regexp.MustCompile(`(?i)/resourceGroups/([^/]+)/providers/Microsoft\.Compute/virtualMachines/`)
	vmNameRE          = regexp.MustCompile(`(?i)/providers/Microsoft\.Compute/virtualMachines/([^/]+)$`)
)

func parseVMProviderID(providerID string) (resourceGroup, vmName string, err error) {
	rg := vmResourceGroupRE.FindStringSubmatch(providerID)
	name := vmNameRE.FindStringSubmatch(providerID)
	if len(rg) != 2 || len(name) != 2 {
		return "", "", fmt.Errorf("could not parse VM provider ID %q", providerID)
	}
	return rg[1], name[1], nil
}

// instanceViewExpand asks ARM to include the instance view so fault-domain based
// zone fallback works without a second round trip.
var instanceViewExpand = to.Ptr("instanceView")

// azclientDiskVMSet is a cache-free DiskVMSet backed by azclient's VM client. It
// serves both the Standard and VMSSFlex topologies (both are individual VM
// resources); the node is located from its spec.providerID rather than from any
// cached VM list.
type azclientDiskVMSet struct {
	clientFactory     azclient.ClientFactory
	resolveProviderID NodeProviderIDResolver
	// isAzureStackCloud toggles the Azure Stack detach path (no ToBeDetached flag).
	isAzureStackCloud bool
}

// NewVM builds a cache-free DiskVMSet for the Standard and
// VMSSFlex topologies (both use the VM client).
func NewVM(clientFactory azclient.ClientFactory, resolver NodeProviderIDResolver, isAzureStackCloud bool) Interface {
	return &azclientDiskVMSet{
		clientFactory:     clientFactory,
		resolveProviderID: resolver,
		isAzureStackCloud: isAzureStackCloud,
	}
}

// locate resolves a node name to its (resourceGroup, vmName) using the node's
// provider ID — no cache, no ARM listing.
func (s *azclientDiskVMSet) locate(ctx context.Context, nodeName types.NodeName) (resourceGroup, vmName string, err error) {
	providerID, err := s.resolveProviderID(ctx, string(nodeName))
	if err != nil {
		return "", "", fmt.Errorf("resolve providerID for node %q: %w", nodeName, err)
	}
	return parseVMProviderID(providerID)
}

// getVMByRef performs a direct, uncached GET of a VM by resource group and name.
func (s *azclientDiskVMSet) getVMByRef(ctx context.Context, resourceGroup, vmName string, expand *string) (*armcompute.VirtualMachine, error) {
	vm, err := s.clientFactory.GetVirtualMachineClient().Get(ctx, resourceGroup, vmName, expand)
	if err != nil {
		return nil, err
	}
	if vm == nil || vm.Properties == nil || vm.Properties.StorageProfile == nil {
		return nil, fmt.Errorf("VM %q in resource group %q has no storage profile", vmName, resourceGroup)
	}
	return vm, nil
}

// getVM locates and GETs the node's VM (uncached). Used by read-only methods.
func (s *azclientDiskVMSet) getVM(ctx context.Context, nodeName types.NodeName, expand *string) (*armcompute.VirtualMachine, error) {
	rg, vmName, err := s.locate(ctx, nodeName)
	if err != nil {
		return nil, err
	}
	return s.getVMByRef(ctx, rg, vmName, expand)
}

// AttachDisk attaches the disks in diskMap to the node's VM.
func (s *azclientDiskVMSet) AttachDisk(ctx context.Context, nodeName types.NodeName, diskMap map[string]*AttachDiskOptions) error {
	rg, vmName, err := s.locate(ctx, nodeName)
	if err != nil {
		return err
	}
	vm, err := s.getVMByRef(ctx, rg, vmName, nil)
	if err != nil {
		return err
	}

	disks := make([]*armcompute.DataDisk, len(vm.Properties.StorageProfile.DataDisks))
	copy(disks, vm.Properties.StorageProfile.DataDisks)

	for diskURI, opt := range diskMap {
		attached := false
		for _, disk := range vm.Properties.StorageProfile.DataDisks {
			if disk.ManagedDisk != nil && disk.Lun != nil && strings.EqualFold(*disk.ManagedDisk.ID, diskURI) {
				if *disk.Lun == opt.Lun {
					attached = true
					break
				}
				return fmt.Errorf("disk(%s) already attached to node(%s) on LUN(%d), but target LUN is %d", diskURI, nodeName, *disk.Lun, opt.Lun)
			}
		}
		if attached {
			klog.V(2).Infof("azureDisk - disk(%s) already attached to node(%s) on LUN(%d)", diskURI, nodeName, opt.Lun)
			continue
		}

		managedDisk := &armcompute.ManagedDiskParameters{ID: to.Ptr(diskURI)}
		desID := opt.DiskEncryptionSetID
		if desID == "" && vm.Properties.StorageProfile.OSDisk != nil &&
			vm.Properties.StorageProfile.OSDisk.ManagedDisk != nil &&
			vm.Properties.StorageProfile.OSDisk.ManagedDisk.DiskEncryptionSet != nil &&
			vm.Properties.StorageProfile.OSDisk.ManagedDisk.DiskEncryptionSet.ID != nil {
			// inherit the OS disk's disk-encryption-set by default
			desID = *vm.Properties.StorageProfile.OSDisk.ManagedDisk.DiskEncryptionSet.ID
		}
		if desID != "" {
			managedDisk.DiskEncryptionSet = &armcompute.DiskEncryptionSetParameters{ID: to.Ptr(desID)}
		}

		lun := opt.Lun
		disks = append(disks, &armcompute.DataDisk{
			Name:                    to.Ptr(opt.DiskName),
			Lun:                     &lun,
			Caching:                 to.Ptr(opt.CachingMode),
			CreateOption:            to.Ptr(armcompute.DiskCreateOptionTypesAttach),
			ManagedDisk:             managedDisk,
			WriteAcceleratorEnabled: ptr.To(opt.WriteAcceleratorEnabled),
		})
	}

	return s.updateDataDisks(ctx, rg, vmName, vm.Location, disks)
}

// DetachDisk detaches the disks in diskMap from the node's VM.
func (s *azclientDiskVMSet) DetachDisk(ctx context.Context, nodeName types.NodeName, diskMap map[string]string, forceDetach bool) error {
	rg, vmName, err := s.locate(ctx, nodeName)
	if err != nil {
		// if the node can't be located there is nothing to detach
		klog.Warningf("azureDisk - cannot locate node %s, skip detaching disk list(%v): %v", nodeName, diskMap, err)
		return nil
	}
	vm, err := s.getVMByRef(ctx, rg, vmName, nil)
	if err != nil {
		// if the VM doesn't exist there is nothing to detach
		klog.Warningf("azureDisk - cannot find node %s, skip detaching disk list(%v): %v", nodeName, diskMap, err)
		return nil
	}

	disks := make([]*armcompute.DataDisk, len(vm.Properties.StorageProfile.DataDisks))
	copy(disks, vm.Properties.StorageProfile.DataDisks)

	found := false
	for i, disk := range disks {
		for diskURI, diskName := range diskMap {
			if (disk.Name != nil && diskName != "" && strings.EqualFold(*disk.Name, diskName)) ||
				(disk.ManagedDisk != nil && disk.ManagedDisk.ID != nil && diskURI != "" && strings.EqualFold(*disk.ManagedDisk.ID, diskURI)) {
				klog.V(2).Infof("azureDisk - detach disk: name(%q) uri(%q) from node(%s)", diskName, diskURI, nodeName)
				disks[i].ToBeDetached = ptr.To(true)
				if forceDetach {
					disks[i].DetachOption = to.Ptr(armcompute.DiskDetachOptionTypesForceDetach)
				}
				found = true
			}
		}
	}

	if !found {
		klog.Warningf("detach azure disk on node(%s): disk list(%v) not found", nodeName, diskMap)
	} else if s.isAzureStackCloud {
		// Azure Stack does not support the ToBeDetached flag; drop the disks instead.
		kept := disks[:0]
		for _, disk := range disks {
			if !ptr.Deref(disk.ToBeDetached, false) {
				kept = append(kept, disk)
			}
		}
		disks = kept
	}

	return s.updateDataDisks(ctx, rg, vmName, vm.Location, disks)
}

// UpdateVM re-issues a VM update to reconcile pending attach/detach operations.
func (s *azclientDiskVMSet) UpdateVM(ctx context.Context, nodeName types.NodeName) error {
	rg, vmName, err := s.locate(ctx, nodeName)
	if err != nil {
		return err
	}
	vm, err := s.getVMByRef(ctx, rg, vmName, nil)
	if err != nil {
		return err
	}
	return s.updateDataDisks(ctx, rg, vmName, vm.Location, vm.Properties.StorageProfile.DataDisks)
}

// updateDataDisks PUTs the given data-disk list to the VM. No cache is touched.
func (s *azclientDiskVMSet) updateDataDisks(ctx context.Context, resourceGroup, vmName string, location *string, disks []*armcompute.DataDisk) error {
	newVM := armcompute.VirtualMachine{
		Location: location,
		Properties: &armcompute.VirtualMachineProperties{
			StorageProfile: &armcompute.StorageProfile{DataDisks: disks},
		},
	}
	_, err := s.clientFactory.GetVirtualMachineClient().CreateOrUpdate(ctx, resourceGroup, vmName, newVM)
	if err != nil && isNotFound(err) {
		// A referenced managed disk may have been deleted out-of-band; drop the
		// missing disks and retry once.
		newVM.Properties.StorageProfile.DataDisks = filterNonExistingDisks(ctx, s.clientFactory, disks)
		_, err = s.clientFactory.GetVirtualMachineClient().CreateOrUpdate(ctx, resourceGroup, vmName, newVM)
	}
	return err
}

// GetDataDisks returns the data disks currently attached to the node (uncached).
func (s *azclientDiskVMSet) GetDataDisks(ctx context.Context, nodeName types.NodeName, _ azcache.AzureCacheReadType) ([]*armcompute.DataDisk, *string, error) {
	vm, err := s.getVM(ctx, nodeName, nil)
	if err != nil {
		return nil, nil, err
	}
	return vm.Properties.StorageProfile.DataDisks, vm.Properties.ProvisioningState, nil
}

// GetInstanceTypeByNodeName returns the VM size for the node (uncached).
func (s *azclientDiskVMSet) GetInstanceTypeByNodeName(ctx context.Context, name string) (string, error) {
	vm, err := s.getVM(ctx, types.NodeName(name), nil)
	if err != nil {
		return "", err
	}
	if vm.Properties.HardwareProfile == nil || vm.Properties.HardwareProfile.VMSize == nil {
		return "", fmt.Errorf("HardwareProfile of node(%s) is nil", name)
	}
	return string(*vm.Properties.HardwareProfile.VMSize), nil
}

// GetZoneByNodeName returns the availability zone (or fault domain) for the node (uncached).
func (s *azclientDiskVMSet) GetZoneByNodeName(ctx context.Context, name string) (cloudprovider.Zone, error) {
	vm, err := s.getVM(ctx, types.NodeName(name), instanceViewExpand)
	if err != nil {
		return cloudprovider.Zone{}, err
	}

	region := strings.ToLower(ptr.Deref(vm.Location, ""))
	var failureDomain string
	if len(vm.Zones) > 0 {
		zoneID, err := strconv.Atoi(ptr.Deref(vm.Zones[0], ""))
		if err != nil {
			return cloudprovider.Zone{}, fmt.Errorf("failed to parse zone %q: %w", ptr.Deref(vm.Zones[0], ""), err)
		}
		failureDomain = fmt.Sprintf("%s-%d", region, zoneID)
	} else if vm.Properties.InstanceView != nil {
		failureDomain = strconv.Itoa(int(ptr.Deref(vm.Properties.InstanceView.PlatformFaultDomain, 0)))
	} else {
		failureDomain = "0"
	}

	return cloudprovider.Zone{
		FailureDomain: strings.ToLower(failureDomain),
		Region:        region,
	}, nil
}

// GetNodeNameByProviderID resolves the node (computer) name for a VM provider ID.
// It reads OSProfile.ComputerName so it is correct for both Standard and VMSSFlex
// (where the VM/node names may differ). Uncached.
func (s *azclientDiskVMSet) GetNodeNameByProviderID(ctx context.Context, providerID string) (types.NodeName, error) {
	rg, vmName, err := parseVMProviderID(providerID)
	if err != nil {
		return "", err
	}
	vm, err := s.getVMByRef(ctx, rg, vmName, nil)
	if err != nil {
		return "", err
	}
	if vm.Properties.OSProfile != nil && vm.Properties.OSProfile.ComputerName != nil {
		return types.NodeName(strings.ToLower(*vm.Properties.OSProfile.ComputerName)), nil
	}
	// Fall back to the ARM VM name when the computer name is unavailable.
	return types.NodeName(strings.ToLower(vmName)), nil
}

// DeleteCacheForNode is a no-op: this implementation holds no cache. This is the
// whole point of dropping the provider layer — there is nothing to invalidate,
// so error paths that used to force an expensive VM-list refresh disappear.
func (s *azclientDiskVMSet) DeleteCacheForNode(_ context.Context, _ string) error { return nil }

// InstanceExists reports whether the node's VM still exists (uncached).
func (s *azclientDiskVMSet) InstanceExists(ctx context.Context, nodeName types.NodeName) (bool, error) {
	rg, vmName, err := s.locate(ctx, nodeName)
	if err != nil {
		return false, err
	}
	if _, err := s.getVMByRef(ctx, rg, vmName, nil); err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Compile-time assertion that the implementation satisfies the driver-owned interface.
var _ Interface = (*azclientDiskVMSet)(nil)
