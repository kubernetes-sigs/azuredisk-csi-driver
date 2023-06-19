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
	"k8s.io/utils/pointer"
)

const (
	// VMASCacheTTLDefaultInSeconds is the TTL of the vmas cache
	VMASCacheTTLDefaultInSeconds = 600
)

// availabilitySet implements VMSet interface for Azure availability sets.
type availabilitySet struct {
	*Cloud

	vmasCache *TimedCache
}

type AvailabilitySetEntry struct {
	VMAS          *armcompute.AvailabilitySet
	ResourceGroup string
}

// newStandardSet creates a new availabilitySet.
func newAvailabilitySet(az *Cloud) (VMSet, error) {
	as := &availabilitySet{
		Cloud: az,
	}

	var err error
	as.vmasCache, err = as.newVMASCache()
	if err != nil {
		return nil, err
	}

	return as, nil
}

func (as *availabilitySet) newVMASCache() (*TimedCache, error) {
	getter := func(key string) (interface{}, error) {
		localCache := &sync.Map{}

		allResourceGroups, err := as.GetResourceGroups()
		if err != nil {
			return nil, err
		}

		for _, resourceGroup := range allResourceGroups.UnsortedList() {
			pager := as.AvailabilitySetsClient.NewListPager(resourceGroup, nil)
			var allAvailabilitySets []armcompute.AvailabilitySet
			for pager.More() {
				page, err := pager.NextPage(context.Background())
				if err != nil {
					klog.Fatalf("failed to advance page: %v", err)
				}
				for _, availabilitySet := range page.Value {
					allAvailabilitySets = append(allAvailabilitySets, *availabilitySet)
				}
			}

			for i := range allAvailabilitySets {
				vmas := allAvailabilitySets[i]
				if strings.EqualFold(pointer.StringDeref(vmas.Name, ""), "") {
					klog.Warning("failed to get the name of the VMAS")
					continue
				}
				localCache.Store(pointer.StringDeref(vmas.Name, ""), &AvailabilitySetEntry{
					VMAS:          &vmas,
					ResourceGroup: resourceGroup,
				})
			}
		}

		return localCache, nil
	}

	if as.Config.AvailabilitySetsCacheTTLInSeconds == 0 {
		as.Config.AvailabilitySetsCacheTTLInSeconds = VMASCacheTTLDefaultInSeconds
	}

	return NewTimedcache(time.Duration(as.Config.AvailabilitySetsCacheTTLInSeconds)*time.Second, getter)
}

// PLACEHOLDERS
// GetInstanceIDByNodeName gets the cloud provider ID by node name.
// It must return ("", cloudprovider.InstanceNotFound) if the instance does
// not exist or is no longer running.
func (ss *availabilitySet) GetInstanceIDByNodeName(name string) (string, error) { return "", nil }

// GetInstanceTypeByNodeName gets the instance type by node name.
func (ss *availabilitySet) GetInstanceTypeByNodeName(name string) (string, error) { return "", nil }

// GetIPByNodeName gets machine private IP and public IP by node name.
func (ss *availabilitySet) GetIPByNodeName(name string) (string, string, error) { return "", "", nil }

// GetPrimaryInterface gets machine primary network interface by node name.
func (ss *availabilitySet) GetPrimaryInterface(nodeName string) (armnetwork.Interface, error) {
	return armnetwork.Interface{}, nil
}

// GetNodeNameByProviderID gets the node name by provider ID.
func (ss *availabilitySet) GetNodeNameByProviderID(providerID string) (types.NodeName, error) {
	return "", nil
}

// GetZoneByNodeName gets cloudprovider.Zone by node name.
func (ss *availabilitySet) GetZoneByNodeName(name string) (cloudprovider.Zone, error) {
	return cloudprovider.Zone{}, nil
}

// GetPrimaryVMSetName returns the VM set name depending on the configured vmType.
// It returns config.PrimaryavailabilitySetName for vmss and config.PrimaryAvailabilitySetName for standard vmType.
func (ss *availabilitySet) GetPrimaryVMSetName() string { return "" }

// GetVMSetNames selects all possible availability sets or scale sets
// (depending vmType configured) for service load balancer, if the service has
// no loadbalancer mode annotation returns the primary VMSet. If service annotation
// for loadbalancer exists then return the eligible VMSet.
func (ss *availabilitySet) GetVMSetNames(service *v1.Service, nodes []*v1.Node) (availabilitySetNames *[]string, err error) {
	return nil, nil
}

// GetNodeVMSetName returns the availability set or vmss name by the node name.
// It will return empty string when using standalone vms.
func (ss *availabilitySet) GetNodeVMSetName(node *v1.Node) (string, error) { return "", nil }

// EnsureHostsInPool ensures the given Node's primary IP configurations are
// participating in the specified LoadBalancer Backend Pool.
func (ss *availabilitySet) EnsureHostsInPool(service *v1.Service, nodes []*v1.Node, backendPoolID string, vmSetName string) error {
	return nil
}

// EnsureHostInPool ensures the given VM's Primary NIC's Primary IP Configuration is
// participating in the specified LoadBalancer Backend Pool.
func (ss *availabilitySet) EnsureHostInPool(service *v1.Service, nodeName types.NodeName, backendPoolID string, vmSetName string) (string, string, string, *armcompute.VirtualMachineScaleSetVM, error) {
	return "", "", "", &armcompute.VirtualMachineScaleSetVM{}, nil
}

// EnsureBackendPoolDeleted ensures the loadBalancer backendAddressPools deleted from the specified nodes.
func (ss *availabilitySet) EnsureBackendPoolDeleted(service *v1.Service, backendPoolIDs []string, vmSetName string, backendAddressPools *[]armnetwork.BackendAddressPool, deleteFromVMSet bool) (bool, error) {
	return false, nil
}

// EnsureBackendPoolDeletedFromVMSets ensures the loadBalancer backendAddressPools deleted from the specified VMSS/VMAS
func (ss *availabilitySet) EnsureBackendPoolDeletedFromVMSets(vmSetNamesMap map[string]bool, backendPoolIDs []string) error {
	return nil
}

// AttachDisk attaches a disk to vm
func (ss *availabilitySet) AttachDisk(ctx context.Context, nodeName types.NodeName, diskMap map[string]*AttachDiskOptions) (*azure.Future, error) {
	return &azure.Future{}, nil
}

// DetachDisk detaches a disk from vm
func (ss *availabilitySet) DetachDisk(ctx context.Context, nodeName types.NodeName, diskMap map[string]string) error {
	return nil
}

// WaitForUpdateResult waits for the response of the update request
func (ss *availabilitySet) WaitForUpdateResult(ctx context.Context, future *azure.Future, nodeName types.NodeName, source string) error {
	return nil
}

// GetDataDisks gets a list of data disks attached to the node.
func (ss *availabilitySet) GetDataDisks(nodeName types.NodeName, crt AzureCacheReadType) ([]armcompute.DataDisk, *string, error) {
	return nil, nil, nil
}

// UpdateVM updates a vm
func (ss *availabilitySet) UpdateVM(ctx context.Context, nodeName types.NodeName) error { return nil }

// UpdateVMAsync updates a vm asynchronously
func (ss *availabilitySet) UpdateVMAsync(ctx context.Context, nodeName types.NodeName) (*azure.Future, error) {
	return nil, nil
}

// GetPowerStatusByNodeName returns the powerState for the specified node.
func (ss *availabilitySet) GetPowerStatusByNodeName(name string) (string, error) { return "", nil }

// GetProvisioningStateByNodeName returns the provisioningState for the specified node.
func (ss *availabilitySet) GetProvisioningStateByNodeName(name string) (string, error) {
	return "", nil
}

// GetPrivateIPsByNodeName returns a slice of all private ips assigned to node (ipv6 and ipv4)
func (ss *availabilitySet) GetPrivateIPsByNodeName(name string) ([]string, error) { return nil, nil }

// GetNodeNameByIPConfigurationID gets the nodeName and vmSetName by IP configuration ID.
func (ss *availabilitySet) GetNodeNameByIPConfigurationID(ipConfigurationID string) (string, string, error) {
	return "", "", nil
}

// GetNodeCIDRMasksByProviderID returns the node CIDR subnet mask by provider ID.
func (ss *availabilitySet) GetNodeCIDRMasksByProviderID(providerID string) (int, int, error) {
	return -1, -1, nil
}

// GetAgentPoolVMSetNames returns all vmSet names according to the nodes
func (ss *availabilitySet) GetAgentPoolVMSetNames(nodes []*v1.Node) (*[]string, error) {
	return nil, nil
}

// DeleteCacheForNode removes the node entry from cache.
func (ss *availabilitySet) DeleteCacheForNode(nodeName string) error { return nil }
