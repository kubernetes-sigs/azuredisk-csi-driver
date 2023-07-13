package azureutils

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	armcompute "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	"k8s.io/klog/v2"
)

type VMSSCacheEntry struct {
	VMStorageProfileCache *sync.Map
	Name                  *string
	ResourceGroup         *string
}

type VMCacheEntry struct {
	StorageProfile    *armcompute.StorageProfile
	ProvisioningState *string
	VMSSName          *string
	Name              *string
	ResourceGroup     *string
	InstanceID        *string
}

type VMSSVMStorageProfileCache struct {
	VMSSCache *sync.Map
}

func NewCache() *VMSSVMStorageProfileCache {
	return &VMSSVMStorageProfileCache{
		VMSSCache: &sync.Map{},
	}
}

func (v *VMSSVMStorageProfileCache) VmssGetter(vmssName string) (*VMSSCacheEntry, bool) {
	klog.Infof("VmssGetter line 40 vmName: %+v", vmssName)
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

// Get the storage profile of the specified VM from cache.
// If not in cache, get it from VMSSVMClient
func (c *Cloud) Get(ctx context.Context, vmssName, vmName, instanceID string) (*VMCacheEntry, error) {
	klog.Infof("current vmss cache: %+v", c.VMSSVMStorageProfileCache.VMSSCache)
	klog.Infof("vmss name line 54: %+v", vmssName)
	vmss, found := c.VMSSVMStorageProfileCache.VmssGetter(vmssName)
	klog.Infof("vmss: %+v found: %+v", vmss, found)
	if !found {
		// vmss is not in cache, get vm from client
		scaleSetName, err := getScaleSetName(vmssName)
		if err != nil {
			return nil, err
		}

		vm := c.getVMFromClient(ctx, scaleSetName, instanceID)

		// set the vmss and vm in the cache, if the storage profile is not nil
		if vm != nil && vm.Properties != nil && vm.Properties.StorageProfile != nil {
			klog.Infof("attempting to add vmss %+v to cache: current cache is %+v", vmssName, c.VMSSVMStorageProfileCache.VMSSCache)
			result := c.VMSSVMStorageProfileCache.SetVMSSAndVM(vmssName, vmName, instanceID, c.ResourceGroup, *vm.Properties.ProvisioningState, vm.Properties.StorageProfile)
			return result, nil
		} else {
			return nil, fmt.Errorf("storage profile not found")
		}
	} else {
		// vmss is in cache, try to get vm from cache
		klog.Infof("current vm cache: %+v", vmss.VMStorageProfileCache)
		vm, found := vmss.VmGetter(vmName)
		if !found {
			// vm is not in cache, get vm from client
			scaleSetName, err := getScaleSetName(vmssName)
			if err != nil {
				return nil, err
			}
			vm := c.getVMFromClient(ctx, scaleSetName, instanceID)
			// set the vm in the cache, if storage profile is not nil
			if vm != nil && vm.Properties != nil && vm.Properties.StorageProfile != nil {
				klog.Infof("attempting to update vm: current cache is %+v", c.VMSSVMStorageProfileCache)
				result := vmss.SetVM(vmssName, vmName, instanceID, c.ResourceGroup, *vm.Properties.ProvisioningState, vm.Properties.StorageProfile)
				return result, nil
			} else {
				return nil, fmt.Errorf("storage profile not found")
			}
		} else {
			// vm is found in cache, return the storage profile
			return vm, nil
		}
	}
}

func (v *VMSSCacheEntry) VmGetter(vmName string) (*VMCacheEntry, bool) {
	klog.Infof("vmgetter line 104 vmName: %+v", vmName)
	entry, found := v.VMStorageProfileCache.Load(vmName)

	if !found {
		return nil, false
	}

	vmEntry := entry.(*VMCacheEntry)

	klog.Infof("vmgetter line 113 vmName: %+v", vmName)

	return vmEntry, true
}

func (v *VMSSVMStorageProfileCache) SetVMSSAndVM(vmssName, vmName, instanceID, resourceGroup, state string, storageProfile *armcompute.StorageProfile) *VMCacheEntry {
	klog.Infof("SetVMSSAndVM line 119 vmName: %+v", vmName)
	var vmss *VMSSCacheEntry
	if result, found := v.VmssGetter(vmssName); found {
		vmss = result
		klog.Infof("vmss already present: getting vmss from cache")
	} else {
		vmss = &VMSSCacheEntry{
			VMStorageProfileCache: &sync.Map{},
			Name:                  &vmssName,
			ResourceGroup:         &resourceGroup,
		}
		v.VMSSCache.Store(vmssName, vmss)
	}

	klog.Infof("SetVMSSAndVM line 133 vmName: %+v", vmName)

	return vmss.SetVM(vmssName, vmName, instanceID, resourceGroup, state, storageProfile)
}

func (v *VMSSCacheEntry) SetVM(vmssName, vmName, instanceID, resourceGroup, state string, storageProfile *armcompute.StorageProfile) *VMCacheEntry {
	klog.Infof("SetVM line 139 vmName: %+v", vmName)
	var vm *VMCacheEntry
	if _, found := v.VmGetter(vmName); found {
		klog.Infof("vm already present: updating vm")
	}
	vm = &VMCacheEntry{
		StorageProfile:    storageProfile,
		VMSSName:          &vmssName,
		Name:              &vmName,
		InstanceID:        &instanceID,
		ResourceGroup:     &resourceGroup,
		ProvisioningState: &state,
	}
	v.VMStorageProfileCache.Store(vmName, vm)

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
