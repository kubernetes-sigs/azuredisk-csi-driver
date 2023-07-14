package azureutils

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
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
	klog.Infof("VmssGetter line 40 vmssName: %+v", vmssName)
	entry, found := v.VMSSCache.Load(vmssName)
	klog.Infof("vmssgetter entry: %+v found: %+v vmssName: %+v", entry, found, vmssName)

	if !found {
		klog.Infof("line 44 found: %+v", found)
		return nil, false
	}

	vmssEntry := entry.(*VMSSCacheEntry)

	klog.Infof("VmssGetter line 51 vmName: %+v", vmssName)

	return vmssEntry, true
}

// Get the vm object of the specified VM from cache.
// If not in cache, get it from VMSSVMClient
func (c *Cloud) GetVMSSVM(ctx context.Context, vmName string) (*VMCacheEntry, error) {
	klog.Infof("current vmss cache: %+v", c.VMSSVMCache.VMSSCache)
	fullScaleSetName, vmss, found := c.getVMSSFromNodeName(vmName)
	scaleSetName, instanceID, err := GetInstanceIDAndScaleSetNameFromNodeName(vmName)
	if err != nil {
		return nil, err
	}
	klog.Infof("vmss: %+v found: %+v", vmss, found)
	if !found {
		vm := c.getVMFromClient(ctx, scaleSetName, instanceID)

		// set the vmss and vm in the cache, if the storage profile is not nil
		if vm != nil {
			result := c.VMSSVMCache.SetVMSSAndVM(fullScaleSetName, vmName, instanceID, c.ResourceGroup, vm)
			return result, nil
		} else {
			return nil, fmt.Errorf("vm not found")
		}
	} else {
		// vmss is in cache, try to get vm from cache
		klog.Infof("current vm cache: %+v", vmss.VMCache)
		vm, found := vmss.VmGetter(vmName)
		if !found {
			// vm is not in cache, get vm from client
			vm := c.getVMFromClient(ctx, scaleSetName, instanceID)
			// set the vm in the cache, if storage profile is not nil
			if vm != nil {
				klog.Infof("attempting to update vm: current cache is %+v", c.VMSSVMCache)
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
	klog.Infof("vmgetter line 104 vmName: %+v", vmName)
	entry, found := v.VMCache.Load(vmName)

	if !found {
		return nil, false
	}

	vmEntry := entry.(*VMCacheEntry)

	klog.Infof("vmgetter line 113 vmName: %+v", vmName)

	return vmEntry, true
}

func (v *VMSSVMCache) SetVMSSAndVM(vmssName, vmName, instanceID, resourceGroup string, vmObject *armcompute.VirtualMachineScaleSetVM) *VMCacheEntry {
	klog.Infof("SetVMSSAndVM line 119 vmName: %+v", vmName)
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

	klog.Infof("SetVMSSAndVM line 133 vmName: %+v", vmName)

	return vmss.SetVM(vmssName, vmName, instanceID, resourceGroup, vmObject)
}

func (v *VMSSCacheEntry) SetVM(vmssName, vmName, instanceID, resourceGroup string, vmObject *armcompute.VirtualMachineScaleSetVM) *VMCacheEntry {
	klog.Infof("SetVM line 139 vmName: %+v", vmName)
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

	klog.Infof("SetVM line 154 vmName: %+v", vmName)

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

func (c *Cloud) getVMFromClient(ctx context.Context, scaleSetName, instanceID string) *armcompute.VirtualMachineScaleSetVM {
	klog.Infof("scaleSetName getVMFromClient %s instanceID %s", scaleSetName, instanceID)
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		klog.Fatalf("failed to get new credential: %v", err)
	}
	vmssVMClient, err := armcompute.NewVirtualMachineScaleSetVMsClient(c.SubscriptionID, cred, nil)
	if err != nil {
		klog.Fatalf("failed to get client: %v", err)
	}

	resVM, err := vmssVMClient.Get(ctx, c.ResourceGroup, scaleSetName, instanceID, nil)
	if err != nil {
		klog.Fatalf("failed to finish the request: %v", err)
	}

	klog.Infof("getVMFromClient scaleSetName: %+v instanceID: %+v nodeName: %+v", scaleSetName, instanceID, *resVM.VirtualMachineScaleSetVM.Name)

	return &resVM.VirtualMachineScaleSetVM
}

func (c *Cloud) getVMSSFromNodeName(nodeName string) (string, *VMSSCacheEntry, bool) {
	scaleSetName := nodeName[:(len(nodeName) - 6)]

	fullScaleSetName := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachineScaleSets/%s", c.SubscriptionID, c.ResourceGroup, scaleSetName)

	vmss, found := c.VMSSVMCache.VmssGetter(fullScaleSetName)

	return fullScaleSetName, vmss, found
}

func GetInstanceIDAndScaleSetNameFromNodeName(nodeName string) (string, string, error) {
	nameLength := len(nodeName)
	if nameLength < 6 {
		return "", "", fmt.Errorf("not a vmss instance")
	}
	scaleSetName := fmt.Sprintf("%s", string(nodeName[:nameLength-6]))

	id, err := strconv.Atoi(string((nodeName)[nameLength-6:]))
	if err != nil {
		return "", "", fmt.Errorf("cannot parse instance id from node name")
	}
	instanceID := fmt.Sprintf("%d", id)

	return scaleSetName, instanceID, nil
}
