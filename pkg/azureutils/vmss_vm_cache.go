package azureutils

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	armcompute "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	"k8s.io/klog/v2"
)

type VMSSCacheEntry struct {
	VMCache       *sync.Map
	Name          *string
	ResourceGroup *string
}

type VMCacheEntry struct {
	VMSSName      *string
	Name          *string
	InstanceID    *string
	ResourceGroup *string
	VM            *armcompute.VirtualMachineScaleSetVM
}

type VMSSVMCache struct {
	VMSSCache *sync.Map
}

func NewCache() *VMSSVMCache {
	return &VMSSVMCache{
		VMSSCache: &sync.Map{},
	}
}

func (v *VMSSVMCache) VmssGetter(vmssName string) (*VMSSCacheEntry, bool) {
	entry, found := v.VMSSCache.Load(vmssName)

	if !found {
		return nil, false
	}

	vmssEntry := entry.(*VMSSCacheEntry)

	return vmssEntry, true
}

// Get the vm object of the specified VM from cache.
// If not in cache, get it from VMSSVMClient
func (c *Cloud) GetVMSSVM(ctx context.Context, vmName string) (*VMCacheEntry, error) {
	scaleSetName, instanceID, err := GetInstanceIDAndScaleSetNameFromNodeName(vmName)
	if err != nil {
		return nil, fmt.Errorf("failed to get vm: %v", err)
	}

	fullScaleSetName, vmss, found := c.getVMSSFromNodeName(vmName)

	if !found {
		vm, err := c.getVMFromClient(ctx, scaleSetName, instanceID)
		if err != nil {
			return nil, err
		}

		// set the vmss and vm in the cache, if the storage profile is not nil
		if vm != nil {
			result := c.VMSSVMCache.SetVMSSAndVM(fullScaleSetName, vmName, instanceID, c.ResourceGroup, vm)
			return result, nil
		} else {
			return nil, fmt.Errorf("vm not found")
		}
	} else {
		// vmss is in cache, try to get vm from cache
		vm, found := vmss.VmGetter(vmName)
		if !found {
			// vm is not in cache, get vm from client
			vm, err := c.getVMFromClient(ctx, scaleSetName, instanceID)
			if err != nil {
				return nil, fmt.Errorf("failed to get vm: %v", err)
			}
			
			// set the vm in the cache, if storage profile is not nil
			if vm != nil {
				result := vmss.SetVM(fullScaleSetName, vmName, instanceID, c.ResourceGroup, vm)
				return result, nil
			} else {
				return nil, fmt.Errorf("vm not found")
			}
		} else {
			// vm is found in cache, return the storage profile
			return vm, nil
		}
	}
}

func (v *VMSSCacheEntry) VmGetter(vmName string) (*VMCacheEntry, bool) {
	entry, found := v.VMCache.Load(vmName)

	if !found {
		return nil, false
	}

	vmEntry := entry.(*VMCacheEntry)

	return vmEntry, true
}

func (v *VMSSVMCache) SetVMSSAndVM(vmssName, vmName, instanceID, resourceGroup string, vmObject *armcompute.VirtualMachineScaleSetVM) *VMCacheEntry {
	var vmss *VMSSCacheEntry
	if result, found := v.VmssGetter(vmssName); found {
		vmss = result
		klog.Infof("vmss already present: getting vmss from cache")
	} else {
		vmss = &VMSSCacheEntry{
			VMCache:       &sync.Map{},
			Name:          &vmssName,
			ResourceGroup: &resourceGroup,
		}
		v.VMSSCache.Store(vmssName, vmss)
	}

	return vmss.SetVM(vmssName, vmName, instanceID, resourceGroup, vmObject)
}

func (v *VMSSCacheEntry) SetVM(vmssName, vmName, instanceID, resourceGroup string, vmObject *armcompute.VirtualMachineScaleSetVM) *VMCacheEntry {
	var vm *VMCacheEntry
	if _, found := v.VmGetter(vmName); found {
		klog.Infof("vm already present: updating vm")
	}
	vm = &VMCacheEntry{
		VMSSName:      &vmssName,
		Name:          &vmName,
		InstanceID:    &instanceID,
		ResourceGroup: &resourceGroup,
		VM:            vmObject,
	}
	v.VMCache.Store(vmName, vm)

	return vm
}

func getScaleSetName(fullScaleSetName string) (string, error) {
	parts := strings.Split(fullScaleSetName, "/")
	name := parts[len(parts)-1]
	if len(name) == 0 {
		return "", fmt.Errorf("scaleSet name not found")
	}

	return name, nil
}

func (c *Cloud) getVMFromClient(ctx context.Context, scaleSetName, instanceID string) (*armcompute.VirtualMachineScaleSetVM, error) {
	resVM, err := c.VMSSVMClient.Get(ctx, c.ResourceGroup, scaleSetName, instanceID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to finish the request: %v", err)
	}

	return &resVM.VirtualMachineScaleSetVM, nil
}

func (c *Cloud) DeleteVMFromCache(nodeName string) {
	_, vmssEntry, found := c.getVMSSFromNodeName(nodeName)
	if !found {
		klog.Infof("node's scaleset not found")
		return
	}

	vmssEntry.VMCache.Delete(nodeName)
}

func (c *Cloud) getVMSSFromNodeName(nodeName string) (string, *VMSSCacheEntry, bool) {
	scaleSetName := nodeName[:(len(nodeName) - 6)]

	fullScaleSetName := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachineScaleSets/%s", c.SubscriptionID, c.ResourceGroup, scaleSetName)

	vmss, found := c.VMSSVMCache.VmssGetter(fullScaleSetName)

	return fullScaleSetName, vmss, found
}

func GetInstanceIDAndScaleSetNameFromNodeName(nodeName string) (string, string, error) {
	klog.Infof("nodename: %+v", nodeName)
	nameLength := len(nodeName)
	if nameLength < 6 {
		return "", "", fmt.Errorf("not a vmss instance")
	}
	scaleSetName := fmt.Sprintf("%s", string(nodeName[:nameLength-6]))

	klog.Infof("id: %s", string((nodeName)[nameLength-6:]))

	id, err := strconv.Atoi(string((nodeName)[nameLength-6:]))
	if err != nil {
		return "", "", fmt.Errorf("cannot parse instance id from node name")
	}
	instanceID := fmt.Sprintf("%d", id)

	return scaleSetName, instanceID, nil
}
