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

// vmss.go — cache-free Interface for the VMSS-uniform topology.
//
// The attach/detach body mirrors provider/azure_controller_vmss.go, but the node
// is resolved WITHOUT any TimedCache: instead of listing all VMSS VMs and caching
// them, we read the node's spec.providerID (authoritative, cheap) and parse the
// scale-set name + instance ID out of it, then issue a per-instance GET.
//
// NodeProviderIDResolver is the seam for that lookup; in production it reads the
// Kubernetes Node object's spec.providerID via the kube client.

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

// NodeProviderIDResolver returns the ARM provider ID for a node. Production
// implementations read the Kubernetes Node's spec.providerID (no ARM listing,
// no cache).
type NodeProviderIDResolver func(ctx context.Context, nodeName string) (providerID string, err error)

// Parsers for a VMSS provider ID of the form:
//
//	azure:///subscriptions/<sub>/resourceGroups/<rg>/providers/Microsoft.Compute/virtualMachineScaleSets/<vmss>/virtualMachines/<instanceID>
var (
	vmssResourceGroupRE = regexp.MustCompile(`(?i)/resourceGroups/([^/]+)/providers/Microsoft\.Compute/virtualMachineScaleSets/`)
	vmssNameRE          = regexp.MustCompile(`(?i)/virtualMachineScaleSets/([^/]+)/virtualMachines/`)
	vmssInstanceIDRE    = regexp.MustCompile(`(?i)/virtualMachines/([^/]+)$`)
)

// vmssVMRef identifies a single VMSS instance.
type vmssVMRef struct {
	resourceGroup string
	scaleSet      string
	instanceID    string
}

func parseVMSSProviderID(providerID string) (vmssVMRef, error) {
	rg := vmssResourceGroupRE.FindStringSubmatch(providerID)
	name := vmssNameRE.FindStringSubmatch(providerID)
	id := vmssInstanceIDRE.FindStringSubmatch(providerID)
	if len(rg) != 2 || len(name) != 2 || len(id) != 2 {
		return vmssVMRef{}, fmt.Errorf("could not parse VMSS provider ID %q", providerID)
	}
	return vmssVMRef{resourceGroup: rg[1], scaleSet: name[1], instanceID: id[1]}, nil
}

// azclientVMSSDiskVMSet is a cache-free DiskVMSet for VMSS-uniform nodes.
type azclientVMSSDiskVMSet struct {
	clientFactory     azclient.ClientFactory
	resolveProviderID NodeProviderIDResolver
	isAzureStackCloud bool
}

// NewVMSS builds a cache-free DiskVMSet for the VMSS topology.
func NewVMSS(clientFactory azclient.ClientFactory, resolver NodeProviderIDResolver, isAzureStackCloud bool) Interface {
	return &azclientVMSSDiskVMSet{
		clientFactory:     clientFactory,
		resolveProviderID: resolver,
		isAzureStackCloud: isAzureStackCloud,
	}
}

// refForNode resolves a node name to its VMSS instance reference (no cache).
func (s *azclientVMSSDiskVMSet) refForNode(ctx context.Context, nodeName types.NodeName) (vmssVMRef, error) {
	providerID, err := s.resolveProviderID(ctx, string(nodeName))
	if err != nil {
		return vmssVMRef{}, fmt.Errorf("resolve providerID for node %q: %w", nodeName, err)
	}
	return parseVMSSProviderID(providerID)
}

// getVmssVM performs a direct, uncached GET of a VMSS instance.
func (s *azclientVMSSDiskVMSet) getVmssVM(ctx context.Context, ref vmssVMRef) (*armcompute.VirtualMachineScaleSetVM, error) {
	vm, err := s.clientFactory.GetVirtualMachineScaleSetVMClient().Get(ctx, ref.resourceGroup, ref.scaleSet, ref.instanceID)
	if err != nil {
		return nil, err
	}
	if vm == nil || vm.Properties == nil {
		return nil, fmt.Errorf("VMSS VM %s/%s instance %s has no properties", ref.resourceGroup, ref.scaleSet, ref.instanceID)
	}
	return vm, nil
}

// AttachDisk attaches the disks in diskMap to the VMSS instance.
func (s *azclientVMSSDiskVMSet) AttachDisk(ctx context.Context, nodeName types.NodeName, diskMap map[string]*AttachDiskOptions) error {
	ref, err := s.refForNode(ctx, nodeName)
	if err != nil {
		return err
	}
	vm, err := s.getVmssVM(ctx, ref)
	if err != nil {
		return err
	}

	var disks []*armcompute.DataDisk
	sp := vm.Properties.StorageProfile
	if sp != nil && sp.DataDisks != nil {
		disks = make([]*armcompute.DataDisk, len(sp.DataDisks))
		copy(disks, sp.DataDisks)
	}

	for diskURI, opt := range diskMap {
		attached := false
		if sp != nil {
			for _, disk := range sp.DataDisks {
				if disk.ManagedDisk != nil && disk.Lun != nil && strings.EqualFold(*disk.ManagedDisk.ID, diskURI) {
					if *disk.Lun == opt.Lun {
						attached = true
						break
					}
					return fmt.Errorf("disk(%s) already attached to node(%s) on LUN(%d), but target LUN is %d", diskURI, nodeName, *disk.Lun, opt.Lun)
				}
			}
		}
		if attached {
			klog.V(2).Infof("azureDisk - disk(%s) already attached to node(%s) on LUN(%d)", diskURI, nodeName, opt.Lun)
			continue
		}

		managedDisk := &armcompute.ManagedDiskParameters{ID: to.Ptr(diskURI)}
		desID := opt.DiskEncryptionSetID
		if desID == "" && sp != nil && sp.OSDisk != nil && sp.OSDisk.ManagedDisk != nil &&
			sp.OSDisk.ManagedDisk.DiskEncryptionSet != nil && sp.OSDisk.ManagedDisk.DiskEncryptionSet.ID != nil {
			desID = *sp.OSDisk.ManagedDisk.DiskEncryptionSet.ID
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

	return s.updateDataDisks(ctx, ref, disks)
}

// DetachDisk detaches the disks in diskMap from the VMSS instance.
func (s *azclientVMSSDiskVMSet) DetachDisk(ctx context.Context, nodeName types.NodeName, diskMap map[string]string, forceDetach bool) error {
	ref, err := s.refForNode(ctx, nodeName)
	if err != nil {
		return err
	}
	vm, err := s.getVmssVM(ctx, ref)
	if err != nil {
		klog.Warningf("azureDisk - cannot find node %s, skip detaching disk list(%v): %v", nodeName, diskMap, err)
		return nil
	}

	var disks []*armcompute.DataDisk
	if vm.Properties.StorageProfile != nil && vm.Properties.StorageProfile.DataDisks != nil {
		disks = make([]*armcompute.DataDisk, len(vm.Properties.StorageProfile.DataDisks))
		copy(disks, vm.Properties.StorageProfile.DataDisks)
	}

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
		// Skip the VM update entirely (mirrors provider): a generic update can
		// stay pending during zone outages and mask a successful detach.
		klog.Warningf("azureDisk - detach disk: VM update skipped, no disks to detach on node(%s) with diskMap(%v)", nodeName, diskMap)
		return nil
	}

	if s.isAzureStackCloud {
		kept := disks[:0]
		for _, disk := range disks {
			if !ptr.Deref(disk.ToBeDetached, false) {
				kept = append(kept, disk)
			}
		}
		disks = kept
	}

	return s.updateDataDisks(ctx, ref, disks)
}

// UpdateVM re-issues a VMSS instance update to reconcile pending operations.
func (s *azclientVMSSDiskVMSet) UpdateVM(ctx context.Context, nodeName types.NodeName) error {
	ref, err := s.refForNode(ctx, nodeName)
	if err != nil {
		return err
	}
	vm, err := s.getVmssVM(ctx, ref)
	if err != nil {
		return err
	}
	var disks []*armcompute.DataDisk
	if vm.Properties.StorageProfile != nil {
		disks = vm.Properties.StorageProfile.DataDisks
	}
	return s.updateDataDisks(ctx, ref, disks)
}

// updateDataDisks PUTs the given data-disk list to the VMSS instance. No cache is touched.
func (s *azclientVMSSDiskVMSet) updateDataDisks(ctx context.Context, ref vmssVMRef, disks []*armcompute.DataDisk) error {
	newVM := armcompute.VirtualMachineScaleSetVM{
		Properties: &armcompute.VirtualMachineScaleSetVMProperties{
			StorageProfile: &armcompute.StorageProfile{DataDisks: disks},
		},
	}
	_, err := s.clientFactory.GetVirtualMachineScaleSetVMClient().Update(ctx, ref.resourceGroup, ref.scaleSet, ref.instanceID, newVM)
	if err != nil && isNotFound(err) {
		// A referenced managed disk may have been deleted out-of-band; drop the
		// missing disks and retry once.
		newVM.Properties.StorageProfile.DataDisks = filterNonExistingDisks(ctx, s.clientFactory, disks)
		_, err = s.clientFactory.GetVirtualMachineScaleSetVMClient().Update(ctx, ref.resourceGroup, ref.scaleSet, ref.instanceID, newVM)
	}
	return err
}

// GetDataDisks returns the data disks currently attached to the VMSS instance (uncached).
func (s *azclientVMSSDiskVMSet) GetDataDisks(ctx context.Context, nodeName types.NodeName, _ azcache.AzureCacheReadType) ([]*armcompute.DataDisk, *string, error) {
	ref, err := s.refForNode(ctx, nodeName)
	if err != nil {
		return nil, nil, err
	}
	vm, err := s.getVmssVM(ctx, ref)
	if err != nil {
		return nil, nil, err
	}
	if vm.Properties.StorageProfile == nil {
		return nil, nil, nil
	}
	// VMSS provisioning state lives in InstanceView; the driver only uses this
	// pointer opportunistically, so nil is acceptable here.
	return vm.Properties.StorageProfile.DataDisks, nil, nil
}

// GetInstanceTypeByNodeName returns the VM size (SKU) for the VMSS instance (uncached).
func (s *azclientVMSSDiskVMSet) GetInstanceTypeByNodeName(ctx context.Context, name string) (string, error) {
	ref, err := s.refForNode(ctx, types.NodeName(name))
	if err != nil {
		return "", err
	}
	vm, err := s.getVmssVM(ctx, ref)
	if err != nil {
		return "", err
	}
	if vm.SKU == nil || vm.SKU.Name == nil {
		return "", fmt.Errorf("SKU of VMSS node(%s) is nil", name)
	}
	return *vm.SKU.Name, nil
}

// GetZoneByNodeName returns the availability zone (or fault domain) for the VMSS instance (uncached).
func (s *azclientVMSSDiskVMSet) GetZoneByNodeName(ctx context.Context, name string) (cloudprovider.Zone, error) {
	ref, err := s.refForNode(ctx, types.NodeName(name))
	if err != nil {
		return cloudprovider.Zone{}, err
	}
	vm, err := s.getVmssVM(ctx, ref)
	if err != nil {
		return cloudprovider.Zone{}, err
	}

	region := strings.ToLower(ptr.Deref(vm.Location, ""))
	var failureDomain string
	switch {
	case len(vm.Zones) > 0:
		zoneID, err := strconv.Atoi(ptr.Deref(vm.Zones[0], ""))
		if err != nil {
			return cloudprovider.Zone{}, fmt.Errorf("failed to parse zone %q: %w", ptr.Deref(vm.Zones[0], ""), err)
		}
		failureDomain = fmt.Sprintf("%s-%d", region, zoneID)
	case vm.Properties.InstanceView != nil && vm.Properties.InstanceView.PlatformFaultDomain != nil:
		failureDomain = strconv.Itoa(int(*vm.Properties.InstanceView.PlatformFaultDomain))
	default:
		failureDomain = "0"
	}

	return cloudprovider.Zone{
		FailureDomain: strings.ToLower(failureDomain),
		Region:        region,
	}, nil
}

// GetNodeNameByProviderID resolves the node (computer) name for a VMSS provider ID (uncached).
func (s *azclientVMSSDiskVMSet) GetNodeNameByProviderID(ctx context.Context, providerID string) (types.NodeName, error) {
	ref, err := parseVMSSProviderID(providerID)
	if err != nil {
		return "", err
	}
	vm, err := s.getVmssVM(ctx, ref)
	if err != nil {
		return "", err
	}
	if vm.Properties.OSProfile == nil || vm.Properties.OSProfile.ComputerName == nil {
		return "", fmt.Errorf("computer name of VMSS instance %q is nil", providerID)
	}
	return types.NodeName(strings.ToLower(*vm.Properties.OSProfile.ComputerName)), nil
}

// DeleteCacheForNode is a no-op: this implementation holds no cache.
func (s *azclientVMSSDiskVMSet) DeleteCacheForNode(_ context.Context, _ string) error { return nil }

// InstanceExists reports whether the VMSS instance backing the node still exists (uncached).
func (s *azclientVMSSDiskVMSet) InstanceExists(ctx context.Context, nodeName types.NodeName) (bool, error) {
	ref, err := s.refForNode(ctx, nodeName)
	if err != nil {
		return false, err
	}
	if _, err := s.getVmssVM(ctx, ref); err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Compile-time assertion.
var _ Interface = (*azclientVMSSDiskVMSet)(nil)
