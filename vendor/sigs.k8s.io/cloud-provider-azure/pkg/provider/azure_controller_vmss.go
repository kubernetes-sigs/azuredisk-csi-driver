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
	"net/http"
	"strings"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2020-12-01/compute"
	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/Azure/go-autorest/autorest/to"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	azcache "sigs.k8s.io/cloud-provider-azure/pkg/cache"
	"sigs.k8s.io/cloud-provider-azure/pkg/consts"
)

// AttachDisk attaches a disk to vm
func (ss *ScaleSet) AttachDisk(nodeName types.NodeName, diskMap map[string]*AttachDiskOptions) (*azure.Future, error) {
	vmName := mapNodeNameToVMName(nodeName)
	ssName, instanceID, vm, err := ss.getVmssVM(vmName, azcache.CacheReadTypeDefault)
	if err != nil {
		return nil, err
	}

	nodeResourceGroup, err := ss.GetNodeResourceGroup(vmName)
	if err != nil {
		return nil, err
	}

	disks := []compute.DataDisk{}
	if vm.StorageProfile != nil && vm.StorageProfile.DataDisks != nil {
		disks = make([]compute.DataDisk, len(*vm.StorageProfile.DataDisks))
		copy(disks, *vm.StorageProfile.DataDisks)
	}

	for k, v := range diskMap {
		diskURI := k
		opt := v
		if opt.isManagedDisk {
			attached := false
			for _, disk := range *vm.StorageProfile.DataDisks {
				if disk.ManagedDisk != nil && strings.EqualFold(*disk.ManagedDisk.ID, diskURI) {
					attached = true
					break
				}
			}
			if attached {
				klog.V(2).Infof("azureDisk - disk(%s) already attached to node(%s)", diskURI, nodeName)
				continue
			}

			managedDisk := &compute.ManagedDiskParameters{ID: &diskURI}
			if opt.diskEncryptionSetID == "" {
				if vm.StorageProfile.OsDisk != nil &&
					vm.StorageProfile.OsDisk.ManagedDisk != nil &&
					vm.StorageProfile.OsDisk.ManagedDisk.DiskEncryptionSet != nil &&
					vm.StorageProfile.OsDisk.ManagedDisk.DiskEncryptionSet.ID != nil {
					// set diskEncryptionSet as value of os disk by default
					opt.diskEncryptionSetID = *vm.StorageProfile.OsDisk.ManagedDisk.DiskEncryptionSet.ID
				}
			}
			if opt.diskEncryptionSetID != "" {
				managedDisk.DiskEncryptionSet = &compute.DiskEncryptionSetParameters{ID: &opt.diskEncryptionSetID}
			}
			disks = append(disks,
				compute.DataDisk{
					Name:                    &opt.diskName,
					Lun:                     &opt.lun,
					Caching:                 opt.cachingMode,
					CreateOption:            "attach",
					ManagedDisk:             managedDisk,
					WriteAcceleratorEnabled: to.BoolPtr(opt.writeAcceleratorEnabled),
				})
		} else {
			disks = append(disks,
				compute.DataDisk{
					Name: &opt.diskName,
					Vhd: &compute.VirtualHardDisk{
						URI: &diskURI,
					},
					Lun:          &opt.lun,
					Caching:      opt.cachingMode,
					CreateOption: "attach",
				})
		}
	}

	newVM := compute.VirtualMachineScaleSetVM{
		VirtualMachineScaleSetVMProperties: &compute.VirtualMachineScaleSetVMProperties{
			StorageProfile: &compute.StorageProfile{
				DataDisks: &disks,
			},
		},
	}

	ctx, cancel := getContextWithCancel()
	defer cancel()

	// Invalidate the cache right after updating
	defer func() {
		_ = ss.deleteCacheForNode(vmName)
	}()

	klog.V(2).Infof("azureDisk - update(%s): vm(%s) - attach disk list(%s)", nodeResourceGroup, nodeName, diskMap)
	future, rerr := ss.VirtualMachineScaleSetVMsClient.UpdateAsync(ctx, nodeResourceGroup, ssName, instanceID, newVM, "attach_disk")
	if rerr != nil {
		klog.Errorf("azureDisk - attach disk list(%s) on rg(%s) vm(%s) failed, err: %v", diskMap, nodeResourceGroup, nodeName, rerr)
		if rerr.HTTPStatusCode == http.StatusNotFound {
			klog.Errorf("azureDisk - begin to filterNonExistingDisks(%v) on rg(%s) vm(%s)", diskMap, nodeResourceGroup, nodeName)
			disks := ss.filterNonExistingDisks(ctx, *newVM.VirtualMachineScaleSetVMProperties.StorageProfile.DataDisks)
			newVM.VirtualMachineScaleSetVMProperties.StorageProfile.DataDisks = &disks
			future, rerr = ss.VirtualMachineScaleSetVMsClient.UpdateAsync(ctx, nodeResourceGroup, ssName, instanceID, newVM, "attach_disk")
		}
	}

	klog.V(2).Infof("azureDisk - update(%s): vm(%s) - attach disk list(%s, %s) returned with %v", nodeResourceGroup, nodeName, diskMap, rerr)
	if rerr != nil {
		return future, rerr.Error()
	}
	return future, nil
}

// WaitForUpdateResult waits for the response of the update request
func (ss *ScaleSet) WaitForUpdateResult(ctx context.Context, future *azure.Future, resourceGroupName, source string) error {
	if rerr := ss.VirtualMachineScaleSetVMsClient.WaitForUpdateResult(ctx, future, resourceGroupName, source); rerr != nil {
		return rerr.Error()
	}
	return nil
}

// DetachDisk detaches a disk from VM
func (ss *ScaleSet) DetachDisk(nodeName types.NodeName, diskMap map[string]string) (*azure.Future, error) {
	vmName := mapNodeNameToVMName(nodeName)
	ssName, instanceID, vm, err := ss.getVmssVM(vmName, azcache.CacheReadTypeDefault)
	if err != nil {
		return nil, err
	}

	nodeResourceGroup, err := ss.GetNodeResourceGroup(vmName)
	if err != nil {
		return nil, err
	}

	disks := []compute.DataDisk{}
	if vm.StorageProfile != nil && vm.StorageProfile.DataDisks != nil {
		disks = make([]compute.DataDisk, len(*vm.StorageProfile.DataDisks))
		copy(disks, *vm.StorageProfile.DataDisks)
	}
	bFoundDisk := false
	for i, disk := range disks {
		for diskURI, diskName := range diskMap {
			if disk.Lun != nil && (disk.Name != nil && diskName != "" && strings.EqualFold(*disk.Name, diskName)) ||
				(disk.Vhd != nil && disk.Vhd.URI != nil && diskURI != "" && strings.EqualFold(*disk.Vhd.URI, diskURI)) ||
				(disk.ManagedDisk != nil && diskURI != "" && strings.EqualFold(*disk.ManagedDisk.ID, diskURI)) {
				// found the disk
				klog.V(2).Infof("azureDisk - detach disk: name %q uri %q", diskName, diskURI)
				disks[i].ToBeDetached = to.BoolPtr(true)
				bFoundDisk = true
			}
		}
	}

	if !bFoundDisk {
		// only log here, next action is to update VM status with original meta data
		klog.Errorf("detach azure disk on node(%s): disk list(%s) not found", nodeName, diskMap)
	} else {
		if strings.EqualFold(ss.cloud.Environment.Name, consts.AzureStackCloudName) && !ss.Config.DisableAzureStackCloud {
			// Azure stack does not support ToBeDetached flag, use original way to detach disk
			newDisks := []compute.DataDisk{}
			for _, disk := range disks {
				if !to.Bool(disk.ToBeDetached) {
					newDisks = append(newDisks, disk)
				}
			}
			disks = newDisks
		}
	}

	newVM := compute.VirtualMachineScaleSetVM{
		VirtualMachineScaleSetVMProperties: &compute.VirtualMachineScaleSetVMProperties{
			StorageProfile: &compute.StorageProfile{
				DataDisks: &disks,
			},
		},
	}

	ctx, cancel := getContextWithCancel()
	defer cancel()

	// Invalidate the cache right after updating
	defer func() {
		_ = ss.deleteCacheForNode(vmName)
	}()

	klog.V(2).Infof("azureDisk - update(%s): vm(%s) - detach disk list(%s)", nodeResourceGroup, nodeName, diskMap)
	future, rerr := ss.VirtualMachineScaleSetVMsClient.UpdateAsync(ctx, nodeResourceGroup, ssName, instanceID, newVM, "detach_disk")
	if rerr != nil {
		klog.Errorf("azureDisk - detach disk list(%s) on rg(%s) vm(%s) failed, err: %v", diskMap, nodeResourceGroup, nodeName, rerr)
		if rerr.HTTPStatusCode == http.StatusNotFound {
			klog.Errorf("azureDisk - begin to filterNonExistingDisks(%v) on rg(%s) vm(%s)", diskMap, nodeResourceGroup, nodeName)
			disks := ss.filterNonExistingDisks(ctx, *newVM.VirtualMachineScaleSetVMProperties.StorageProfile.DataDisks)
			newVM.VirtualMachineScaleSetVMProperties.StorageProfile.DataDisks = &disks
			future, rerr = ss.VirtualMachineScaleSetVMsClient.UpdateAsync(ctx, nodeResourceGroup, ssName, instanceID, newVM, "detach_disk")
		}
	}

	klog.V(2).Infof("azureDisk - update(%s): vm(%s) - detach disk(%v) returned with %v", nodeResourceGroup, nodeName, diskMap, rerr)
	if rerr != nil {
		return future, rerr.Error()
	}
	return future, nil
}

// UpdateVM updates a vm
func (ss *ScaleSet) UpdateVM(nodeName types.NodeName) error {
	vmName := mapNodeNameToVMName(nodeName)
	ssName, instanceID, _, err := ss.getVmssVM(vmName, azcache.CacheReadTypeDefault)
	if err != nil {
		return err
	}

	nodeResourceGroup, err := ss.GetNodeResourceGroup(vmName)
	if err != nil {
		return err
	}

	ctx, cancel := getContextWithCancel()
	defer cancel()

	// Invalidate the cache right after updating
	defer func() {
		_ = ss.deleteCacheForNode(vmName)
	}()

	klog.V(2).Infof("azureDisk - update(%s): vm(%s)", nodeResourceGroup, nodeName)
	rerr := ss.VirtualMachineScaleSetVMsClient.Update(ctx, nodeResourceGroup, ssName, instanceID, compute.VirtualMachineScaleSetVM{}, "update_vmss_instance")

	klog.V(2).Infof("azureDisk - update(%s): vm(%s) - returned with %v", nodeResourceGroup, nodeName, rerr)
	if rerr != nil {
		return rerr.Error()
	}
	return nil
}

// GetDataDisks gets a list of data disks attached to the node.
func (ss *ScaleSet) GetDataDisks(nodeName types.NodeName, crt azcache.AzureCacheReadType) ([]compute.DataDisk, *string, error) {
	_, _, vm, err := ss.getVmssVM(string(nodeName), crt)
	if err != nil {
		return nil, nil, err
	}

	if vm.StorageProfile == nil || vm.StorageProfile.DataDisks == nil {
		return nil, nil, nil
	}

	return *vm.StorageProfile.DataDisks, vm.ProvisioningState, nil
}
