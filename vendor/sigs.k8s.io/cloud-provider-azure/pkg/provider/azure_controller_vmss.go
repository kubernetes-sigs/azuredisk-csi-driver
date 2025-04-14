/*
Copyright 2020 The Kubernetes Authors.

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

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	azcache "sigs.k8s.io/cloud-provider-azure/pkg/cache"
)

// AttachDisk attaches a disk to vm
func (ss *ScaleSet) AttachDisk(ctx context.Context, nodeName types.NodeName, diskMap map[string]*AttachDiskOptions) error {
	vmName := mapNodeNameToVMName(nodeName)
	vm, err := ss.getVmssVM(ctx, vmName, azcache.CacheReadTypeDefault)
	if err != nil {
		return err
	}

	nodeResourceGroup, err := ss.GetNodeResourceGroup(vmName)
	if err != nil {
		return err
	}

	var dataDisksToAttach []*armcompute.DataDisksToAttach
	storageProfile := vm.AsVirtualMachineScaleSetVM().Properties.StorageProfile

	for k, v := range diskMap {
		diSKURI := k
		opt := v
		attached := false
		for _, disk := range storageProfile.DataDisks {
			if disk.ManagedDisk != nil && strings.EqualFold(*disk.ManagedDisk.ID, diSKURI) && disk.Lun != nil {
				if *disk.Lun == opt.Lun {
					attached = true
					break
				}
				return fmt.Errorf("disk(%s) already attached to node(%s) on LUN(%d), but target LUN is %d", diSKURI, nodeName, *disk.Lun, opt.Lun)

			}
		}
		if attached {
			klog.V(2).Infof("azureDisk - disk(%s) already attached to node(%s) on LUN(%d)", diSKURI, nodeName, opt.Lun)
			continue
		}

		cachingMode := armcompute.CachingTypes(opt.CachingMode)
		dataDisk := armcompute.DataDisksToAttach{
			DiskID:                  &diSKURI,
			Lun:                     &opt.Lun,
			Caching:                 &cachingMode,
			WriteAcceleratorEnabled: ptr.To(opt.WriteAcceleratorEnabled),
		}
		if opt.DiskEncryptionSetID == "" {
			if storageProfile.OSDisk != nil &&
				storageProfile.OSDisk.ManagedDisk != nil &&
				storageProfile.OSDisk.ManagedDisk.DiskEncryptionSet != nil &&
				storageProfile.OSDisk.ManagedDisk.DiskEncryptionSet.ID != nil {
				// set diskEncryptionSet as value of os disk by default
				opt.DiskEncryptionSetID = *storageProfile.OSDisk.ManagedDisk.DiskEncryptionSet.ID
			}
		}
		if opt.DiskEncryptionSetID != "" {
			dataDisk.DiskEncryptionSet = &armcompute.DiskEncryptionSetParameters{ID: &opt.DiskEncryptionSetID}
		}
		dataDisksToAttach = append(dataDisksToAttach, &dataDisk)
	}

	attachDataDisksRequest := armcompute.AttachDetachDataDisksRequest{
		DataDisksToAttach: dataDisksToAttach,
	}

	klog.V(2).Infof("azureDisk - update: rg(%s) vm(%s) - attach disk list(%+v)", nodeResourceGroup, nodeName, diskMap)
	resp, err := ss.ComputeClientFactory.GetVirtualMachineScaleSetVMClient().AttachDetachDataDisks(ctx, nodeResourceGroup, vm.VMSSName, vm.InstanceID, attachDataDisksRequest)
	klog.V(2).Infof("azureDisk - update: rg(%s) vm(%s) - attach disk list(%+v) returned with %v", nodeResourceGroup, nodeName, diskMap, err)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil || resp == nil {
			_ = ss.DeleteCacheForNode(ctx, vmName)
		}
	}()

	if resp != nil {
		vm, err := ss.getVmssVM(ctx, vmName, azcache.CacheReadTypeDefault)
		if err == nil && vm != nil && vm.VirtualMachineScaleSetVMProperties != nil {
			vm.VirtualMachineScaleSetVMProperties.StorageProfile = &resp.StorageProfile
			klog.V(2).Infof("StorageProfile: %v, datadisk num: %d", resp.StorageProfile, len(resp.StorageProfile.DataDisks))
			if err := ss.updateCache(ctx, vmName, nodeResourceGroup, vm.VMSSName, vm.InstanceID, vm.AsVirtualMachineScaleSetVM()); err != nil {
				klog.Errorf("updateCache(%s, %s, %s, %s) failed with error: %v", vmName, nodeResourceGroup, vm.VMSSName, vm.InstanceID, err)
			}
		} else {
			klog.Errorf("getVmssVM failed with error(%v) or nil pointer", err)
		}
	}
	return err
}

// DetachDisk detaches a disk from VM
func (ss *ScaleSet) DetachDisk(ctx context.Context, nodeName types.NodeName, diskMap map[string]string, forceDetach bool) error {
	vmName := mapNodeNameToVMName(nodeName)
	vm, err := ss.getVmssVM(ctx, vmName, azcache.CacheReadTypeDefault)
	if err != nil {
		return err
	}
	if vm == nil || vm.VirtualMachineScaleSetVMProperties == nil {
		return fmt.Errorf("vm(%s) is nil or vm properties is nil", vmName)
	}

	nodeResourceGroup, err := ss.GetNodeResourceGroup(vmName)
	if err != nil {
		return err
	}

	var dataDisksToDetach []*armcompute.DataDisksToDetach

	storageProfile := vm.VirtualMachineScaleSetVMProperties.StorageProfile
	bFoundDisk := false
	for _, disk := range storageProfile.DataDisks {
		for diSKURI, diskName := range diskMap {
			if disk.Lun != nil && (disk.Name != nil && diskName != "" && strings.EqualFold(*disk.Name, diskName)) ||
				(disk.Vhd != nil && disk.Vhd.URI != nil && diSKURI != "" && strings.EqualFold(*disk.Vhd.URI, diSKURI)) ||
				(disk.ManagedDisk != nil && diSKURI != "" && strings.EqualFold(*disk.ManagedDisk.ID, diSKURI)) {
				// found the disk
				klog.V(2).Infof("azureDisk - detach disk: name %s uri %s", diskName, diSKURI)
				uri := diSKURI
				dataDisksToDetach = append(dataDisksToDetach, &armcompute.DataDisksToDetach{
					DiskID: &uri,
				})
				bFoundDisk = true
			}
		}
	}

	if !bFoundDisk {
		klog.Warningf("detach azure disk on node(%s): disk list(%s) not found", nodeName, diskMap)
		return nil
	}

	detachDataDisksRequest := armcompute.AttachDetachDataDisksRequest{
		DataDisksToDetach: dataDisksToDetach,
	}

	klog.V(2).Infof("azureDisk - update(%s): vm(%s) - detach disk list(%s), length of detach list: %d", nodeResourceGroup, nodeName, diskMap, len(dataDisksToDetach))
	resp, err := ss.ComputeClientFactory.GetVirtualMachineScaleSetVMClient().AttachDetachDataDisks(ctx, nodeResourceGroup, vm.VMSSName, vm.InstanceID, detachDataDisksRequest)
	klog.V(2).Infof("azureDisk - update(%s): vm(%s) - detach disk list(%s) returned with %v", nodeResourceGroup, nodeName, diskMap, err)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil || resp == nil {
			_ = ss.DeleteCacheForNode(ctx, vmName)
		}
	}()

	if resp != nil {
		vm, err := ss.getVmssVM(ctx, vmName, azcache.CacheReadTypeDefault)
		if err == nil && vm != nil && vm.VirtualMachineScaleSetVMProperties != nil {
			vm.VirtualMachineScaleSetVMProperties.StorageProfile = &resp.StorageProfile
			if err := ss.updateCache(ctx, vmName, nodeResourceGroup, vm.VMSSName, vm.InstanceID, vm.AsVirtualMachineScaleSetVM()); err != nil {
				klog.Errorf("updateCache(%s, %s, %s, %s) failed with error: %v", vmName, nodeResourceGroup, vm.VMSSName, vm.InstanceID, err)
			}
		} else {
			klog.Errorf("getVmssVM failed with error(%v) or nil pointer", err)
		}
	}
	return err
}

// UpdateVM updates a vm
func (ss *ScaleSet) UpdateVM(ctx context.Context, nodeName types.NodeName) error {
	vmName := mapNodeNameToVMName(nodeName)
	vm, err := ss.getVmssVM(ctx, vmName, azcache.CacheReadTypeDefault)
	if err != nil {
		return err
	}

	nodeResourceGroup, err := ss.GetNodeResourceGroup(vmName)
	if err != nil {
		return err
	}

	_, rerr := ss.ComputeClientFactory.GetVirtualMachineScaleSetVMClient().Update(ctx, nodeResourceGroup, vm.VMSSName, vm.InstanceID, armcompute.VirtualMachineScaleSetVM{})
	if rerr != nil {
		return rerr
	}
	return nil
}

// GetDataDisks gets a list of data disks attached to the node.
func (ss *ScaleSet) GetDataDisks(ctx context.Context, nodeName types.NodeName, crt azcache.AzureCacheReadType) ([]*armcompute.DataDisk, *string, error) {
	vm, err := ss.getVmssVM(ctx, string(nodeName), crt)
	if err != nil {
		return nil, nil, err
	}

	if vm != nil && vm.AsVirtualMachineScaleSetVM() != nil && vm.AsVirtualMachineScaleSetVM().Properties != nil {
		storageProfile := vm.AsVirtualMachineScaleSetVM().Properties.StorageProfile

		if storageProfile == nil || storageProfile.DataDisks == nil {
			return nil, nil, nil
		}
		return storageProfile.DataDisks, vm.AsVirtualMachineScaleSetVM().Properties.ProvisioningState, nil
	}

	return nil, nil, nil
}
