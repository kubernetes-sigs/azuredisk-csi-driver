package azureutils

import (
	"context"
	"strings"
	"sync"
	"time"

	armcompute "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	armnetwork "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v2"
	"github.com/Azure/go-autorest/autorest/azure"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/klog/v2"
)

const (
	// VmssFlexCacheTTLDefaultInSeconds is the TTL of the vmss flex cache
	VmssFlexCacheTTLDefaultInSeconds = 600
	// VmssFlexVMCacheTTLDefaultInSeconds is the TTL of the vmss flex vm cache
	VmssFlexVMCacheTTLDefaultInSeconds = 600
)

// FlexScaleSet implements VMSet interface for Azure Flexible VMSS.
type FlexScaleSet struct {
	*Cloud

	vmssFlexCache *TimedCache

	vmssFlexVMNameToVmssID   *sync.Map
	vmssFlexVMNameToNodeName *sync.Map
	vmssFlexVMCache          *TimedCache

	// lockMap in cache refresh
	lockMap *lockMap
}

func newFlexScaleSet(ctx context.Context, az *Cloud) (VMSet, error) {
	fs := &FlexScaleSet{
		Cloud:                    az,
		vmssFlexVMNameToVmssID:   &sync.Map{},
		vmssFlexVMNameToNodeName: &sync.Map{},
		lockMap:                  newLockMap(),
	}

	var err error
	fs.vmssFlexCache, err = fs.newVmssFlexCache(ctx)
	if err != nil {
		return nil, err
	}
	fs.vmssFlexVMCache, err = fs.newVmssFlexVMCache(ctx)
	if err != nil {
		return nil, err
	}

	return fs, nil
}

func (fs *FlexScaleSet) newVmssFlexCache(ctx context.Context) (*TimedCache, error) {
	getter := func(key string) (interface{}, error) {
		localCache := &sync.Map{}

		allResourceGroups, err := fs.GetResourceGroups()
		if err != nil {
			return nil, err
		}

		for _, resourceGroup := range allResourceGroups.UnsortedList() {
			pager := fs.VirtualMachineScaleSetsClient.NewListPager(resourceGroup, nil)
			var allScaleSets []armcompute.VirtualMachineScaleSet
			for pager.More() {
				page, err := pager.NextPage(context.Background())
				if err != nil {
					klog.Fatalf("failed to advance page: %v", err)
				}
				for _, scaleSet := range page.Value {
					allScaleSets = append(allScaleSets, *scaleSet)
				}
			}

			for i := range allScaleSets {
				scaleSet := allScaleSets[i]
				if scaleSet.ID == nil || *scaleSet.ID == "" {
					klog.Warning("failed to get the ID of VMSS Flex")
					continue
				}

				if *scaleSet.Properties.OrchestrationMode == armcompute.OrchestrationModeFlexible {
					localCache.Store(*scaleSet.ID, &scaleSet)
				}
			}
		}

		return localCache, nil
	}

	if fs.Config.VmssFlexCacheTTLInSeconds == 0 {
		fs.Config.VmssFlexCacheTTLInSeconds = VmssFlexCacheTTLDefaultInSeconds
	}
	return NewTimedcache(time.Duration(fs.Config.VmssFlexCacheTTLInSeconds)*time.Second, getter)
}

func (fs *FlexScaleSet) newVmssFlexVMCache(ctx context.Context) (*TimedCache, error) {
	getter := func(key string) (interface{}, error) {
		localCache := &sync.Map{}

		pager := fs.VMClient.NewListPager(fs.Cloud.ResourceGroup, nil)
		var vms []armcompute.VirtualMachine
		for pager.More() {
			page, err := pager.NextPage(context.Background())
			if err != nil {
				klog.Fatalf("failed to advance page: %v", err)
			}
			for _, vm := range page.Value {
				vms = append(vms, *vm)
			}
		}

		for i := range vms {
			vm := vms[i]
			if vm.Properties.OSProfile != nil && vm.Properties.OSProfile.ComputerName != nil {
				localCache.Store(strings.ToLower(*vm.Properties.OSProfile.ComputerName), &vm)
				fs.vmssFlexVMNameToVmssID.Store(strings.ToLower(*vm.Properties.OSProfile.ComputerName), key)
				fs.vmssFlexVMNameToNodeName.Store(*vm.Name, strings.ToLower(*vm.Properties.OSProfile.ComputerName))
			}
		}

		pager = fs.VMClient.NewListPager(fs.Cloud.ResourceGroup, nil)
		vms = nil
		for pager.More() {
			page, err := pager.NextPage(context.Background())
			if err != nil {
				klog.Fatalf("failed to advance page: %v", err)
			}
			for _, vm := range page.Value {
				vms = append(vms, *vm)
			}
		}

		for i := range vms {
			vm := vms[i]
			if vm.Name != nil {
				nodeName, ok := fs.vmssFlexVMNameToNodeName.Load(*vm.Name)
				if !ok {
					continue
				}

				cached, ok := localCache.Load(nodeName)
				if ok {
					cachedVM := cached.(*armcompute.VirtualMachine)
					cachedVM.Properties.InstanceView = vm.Properties.InstanceView
				}
			}
		}

		return localCache, nil
	}

	if fs.Config.VmssFlexVMCacheTTLInSeconds == 0 {
		fs.Config.VmssFlexVMCacheTTLInSeconds = VmssFlexVMCacheTTLDefaultInSeconds
	}
	return NewTimedcache(time.Duration(fs.Config.VmssFlexVMCacheTTLInSeconds)*time.Second, getter)
}

// PLACEHOLDERS
// GetInstanceIDByNodeName gets the cloud provider ID by node name.
// It must return ("", cloudprovider.InstanceNotFound) if the instance does
// not exist or is no longer running.
func (fs *FlexScaleSet) GetInstanceIDByNodeName(name string) (string, error) { return "", nil }

// GetInstanceTypeByNodeName gets the instance type by node name.
func (fs *FlexScaleSet) GetInstanceTypeByNodeName(name string) (string, error) { return "", nil }

// GetIPByNodeName gets machine private IP and public IP by node name.
func (fs *FlexScaleSet) GetIPByNodeName(name string) (string, string, error) { return "", "", nil }

// GetPrimaryInterface gets machine primary network interface by node name.
func (fs *FlexScaleSet) GetPrimaryInterface(nodeName string) (armnetwork.Interface, error) {
	return armnetwork.Interface{}, nil
}

// GetNodeNameByProviderID gets the node name by provider ID.
func (fs *FlexScaleSet) GetNodeNameByProviderID(providerID string) (types.NodeName, error) {
	return "", nil
}

// GetZoneByNodeName gets cloudprovider.Zone by node name.
func (fs *FlexScaleSet) GetZoneByNodeName(name string) (cloudprovider.Zone, error) {
	return cloudprovider.Zone{}, nil
}

// GetPrimaryVMSetName returns the VM set name depending on the configured vmType.
// It returns config.PrimaryavailabilitySetName for vmss and config.PrimaryAvailabilitySetName for standard vmType.
func (fs *FlexScaleSet) GetPrimaryVMSetName() string { return "" }

// GetVMSetNames selects all possible availability sets or scale sets
// (depending vmType configured) for service load balancer, if the service has
// no loadbalancer mode annotation returns the primary VMSet. If service annotation
// for loadbalancer exists then return the eligible VMSet.
func (fs *FlexScaleSet) GetVMSetNames(service *v1.Service, nodes []*v1.Node) (availabilitySetNames *[]string, err error) {
	return nil, nil
}

// GetNodeVMSetName returns the availability set or vmss name by the node name.
// It will return empty string when using standalone vms.
func (fs *FlexScaleSet) GetNodeVMSetName(node *v1.Node) (string, error) { return "", nil }

// EnsureHostsInPool ensures the given Node's primary IP configurations are
// participating in the specified LoadBalancer Backend Pool.
func (fs *FlexScaleSet) EnsureHostsInPool(service *v1.Service, nodes []*v1.Node, backendPoolID string, vmSetName string) error {
	return nil
}

// EnsureHostInPool ensures the given VM's Primary NIC's Primary IP Configuration is
// participating in the specified LoadBalancer Backend Pool.
func (fs *FlexScaleSet) EnsureHostInPool(service *v1.Service, nodeName types.NodeName, backendPoolID string, vmSetName string) (string, string, string, *armcompute.VirtualMachineScaleSetVM, error) {
	return "", "", "", &armcompute.VirtualMachineScaleSetVM{}, nil
}

// EnsureBackendPoolDeleted ensures the loadBalancer backendAddressPools deleted from the specified nodes.
func (fs *FlexScaleSet) EnsureBackendPoolDeleted(service *v1.Service, backendPoolIDs []string, vmSetName string, backendAddressPools *[]armnetwork.BackendAddressPool, deleteFromVMSet bool) (bool, error) {
	return false, nil
}

// EnsureBackendPoolDeletedFromVMSets ensures the loadBalancer backendAddressPools deleted from the specified VMSS/VMAS
func (fs *FlexScaleSet) EnsureBackendPoolDeletedFromVMSets(vmSetNamesMap map[string]bool, backendPoolIDs []string) error {
	return nil
}

// AttachDisk attaches a disk to vm
func (fs *FlexScaleSet) AttachDisk(ctx context.Context, nodeName types.NodeName, diskMap map[string]*AttachDiskOptions) (*azure.Future, error) {
	return &azure.Future{}, nil
}

// DetachDisk detaches a disk from vm
func (fs *FlexScaleSet) DetachDisk(ctx context.Context, nodeName types.NodeName, diskMap map[string]string) error {
	return nil
}

// WaitForUpdateResult waits for the response of the update request
func (fs *FlexScaleSet) WaitForUpdateResult(ctx context.Context, future *azure.Future, nodeName types.NodeName, source string) error {
	return nil
}

// GetDataDisks gets a list of data disks attached to the node.
func (fs *FlexScaleSet) GetDataDisks(nodeName types.NodeName, crt AzureCacheReadType) ([]armcompute.DataDisk, *string, error) {
	return nil, nil, nil
}

// UpdateVM updates a vm
func (fs *FlexScaleSet) UpdateVM(ctx context.Context, nodeName types.NodeName) error { return nil }

// UpdateVMAsync updates a vm asynchronously
func (fs *FlexScaleSet) UpdateVMAsync(ctx context.Context, nodeName types.NodeName) (*azure.Future, error) {
	return nil, nil
}

// GetPowerStatusByNodeName returns the powerState for the specified node.
func (fs *FlexScaleSet) GetPowerStatusByNodeName(name string) (string, error) { return "", nil }

// GetProvisioningStateByNodeName returns the provisioningState for the specified node.
func (fs *FlexScaleSet) GetProvisioningStateByNodeName(name string) (string, error) {
	return "", nil
}

// GetPrivateIPsByNodeName returns a slice of all private ips assigned to node (ipv6 and ipv4)
func (fs *FlexScaleSet) GetPrivateIPsByNodeName(name string) ([]string, error) { return nil, nil }

// GetNodeNameByIPConfigurationID gets the nodeName and vmSetName by IP configuration ID.
func (fs *FlexScaleSet) GetNodeNameByIPConfigurationID(ipConfigurationID string) (string, string, error) {
	return "", "", nil
}

// GetNodeCIDRMasksByProviderID returns the node CIDR subnet mask by provider ID.
func (fs *FlexScaleSet) GetNodeCIDRMasksByProviderID(providerID string) (int, int, error) {
	return -1, -1, nil
}

// GetAgentPoolVMSetNames returns all vmSet names according to the nodes
func (fs *FlexScaleSet) GetAgentPoolVMSetNames(nodes []*v1.Node) (*[]string, error) {
	return nil, nil
}

// DeleteCacheForNode removes the node entry from cache.
func (fs *FlexScaleSet) DeleteCacheForNode(nodeName string) error { return nil }
