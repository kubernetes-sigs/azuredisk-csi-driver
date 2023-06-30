package provisioner

import (
	"sync"

	armcompute "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
)

type VMSSEntry struct {
	VMMap *sync.Map
	Name  *string
}

type VMEntry struct {
	VM       *armcompute.StorageProfile
	VMSSName string
}

type Cache struct {
	VMSSMap *sync.Map
}

func NewCache() *Cache {
	return &Cache{
		VMSSMap: &sync.Map{},
	}
}

func (c *Cache) Get(vmssName string) (*VMSSEntry, bool) {
	entry, found := c.VMSSMap.Load(vmssName)

	if !found {
		return nil, false
	}

	vmssEntry := entry.(VMSSEntry)

	return &vmssEntry, true
}

func (vmss *VMSSEntry) Get(vmName string) (*VMEntry, bool) {
	entry, found := vmss.VMMap.Load(vmName)

	if !found {
		return nil, false
	}

	vmEntry := entry.(VMEntry)

	return &vmEntry, true
}

func (c *Cache) Set(vmssName, vmName string, storageProfile *armcompute.StorageProfile) {
	vmss := &VMSSEntry{
		Name: &vmssName,
	}
	c.VMSSMap.Store(vmssName, vmss)

	vmss.Set(vmssName, vmName, storageProfile)
}

func (vmss *VMSSEntry) Set(vmssName, vmName string, storageProfile *armcompute.StorageProfile) {
	vm := &VMEntry{
		VM:       storageProfile,
		VMSSName: vmssName,
	}
	vmss.VMMap.Store(vmName, vm)
}
