package azureutils

import (
	"context"
	"sync"

	armcompute "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	armnetwork "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v2"
	"github.com/Azure/go-autorest/autorest/azure"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	cloudprovider "k8s.io/cloud-provider"
)

const (
	// VMSSVirtualMachinesCacheTTLDefaultInSeconds is the TTL of the vmss vm cache
	VMSSVirtualMachinesCacheTTLDefaultInSeconds = 600
)

// AttachDiskOptions attach disk options
type AttachDiskOptions struct {
	cachingMode             armcompute.CachingTypes
	diskName                string
	diskEncryptionSetID     string
	writeAcceleratorEnabled bool
	lun                     int32
}

// VMSet defines functions all vmsets (including scale set and availability
// set) should be implemented.
type VMSet interface {
	// GetInstanceIDByNodeName gets the cloud provider ID by node name.
	// It must return ("", cloudprovider.InstanceNotFound) if the instance does
	// not exist or is no longer running.
	GetInstanceIDByNodeName(name string) (string, error)
	// GetInstanceTypeByNodeName gets the instance type by node name.
	GetInstanceTypeByNodeName(name string) (string, error)
	// GetIPByNodeName gets machine private IP and public IP by node name.
	GetIPByNodeName(name string) (string, string, error)
	// GetPrimaryInterface gets machine primary network interface by node name.
	GetPrimaryInterface(nodeName string) (armnetwork.Interface, error)
	// GetNodeNameByProviderID gets the node name by provider ID.
	GetNodeNameByProviderID(providerID string) (types.NodeName, error)

	// GetZoneByNodeName gets cloudprovider.Zone by node name.
	GetZoneByNodeName(name string) (cloudprovider.Zone, error)

	// GetPrimaryVMSetName returns the VM set name depending on the configured vmType.
	// It returns config.PrimaryScaleSetName for vmss and config.PrimaryAvailabilitySetName for standard vmType.
	GetPrimaryVMSetName() string
	// GetVMSetNames selects all possible availability sets or scale sets
	// (depending vmType configured) for service load balancer, if the service has
	// no loadbalancer mode annotation returns the primary VMSet. If service annotation
	// for loadbalancer exists then return the eligible VMSet.
	GetVMSetNames(service *v1.Service, nodes []*v1.Node) (availabilitySetNames *[]string, err error)
	// GetNodeVMSetName returns the availability set or vmss name by the node name.
	// It will return empty string when using standalone vms.
	GetNodeVMSetName(node *v1.Node) (string, error)
	// EnsureHostsInPool ensures the given Node's primary IP configurations are
	// participating in the specified LoadBalancer Backend Pool.
	EnsureHostsInPool(service *v1.Service, nodes []*v1.Node, backendPoolID string, vmSetName string) error
	// EnsureHostInPool ensures the given VM's Primary NIC's Primary IP Configuration is
	// participating in the specified LoadBalancer Backend Pool.
	EnsureHostInPool(service *v1.Service, nodeName types.NodeName, backendPoolID string, vmSetName string) (string, string, string, *armcompute.VirtualMachineScaleSetVM, error)
	// EnsureBackendPoolDeleted ensures the loadBalancer backendAddressPools deleted from the specified nodes.
	EnsureBackendPoolDeleted(service *v1.Service, backendPoolIDs []string, vmSetName string, backendAddressPools *[]armnetwork.BackendAddressPool, deleteFromVMSet bool) (bool, error)
	//EnsureBackendPoolDeletedFromVMSets ensures the loadBalancer backendAddressPools deleted from the specified VMSS/VMAS
	EnsureBackendPoolDeletedFromVMSets(vmSetNamesMap map[string]bool, backendPoolIDs []string) error

	// AttachDisk attaches a disk to vm
	AttachDisk(ctx context.Context, nodeName types.NodeName, diskMap map[string]*AttachDiskOptions) (*azure.Future, error)
	// DetachDisk detaches a disk from vm
	DetachDisk(ctx context.Context, nodeName types.NodeName, diskMap map[string]string) error
	// WaitForUpdateResult waits for the response of the update request
	WaitForUpdateResult(ctx context.Context, future *azure.Future, nodeName types.NodeName, source string) error

	// GetDataDisks gets a list of data disks attached to the node.
	GetDataDisks(nodeName types.NodeName, crt AzureCacheReadType) ([]armcompute.DataDisk, *string, error)

	// UpdateVM updates a vm
	UpdateVM(ctx context.Context, nodeName types.NodeName) error

	// UpdateVMAsync updates a vm asynchronously
	UpdateVMAsync(ctx context.Context, nodeName types.NodeName) (*azure.Future, error)

	// GetPowerStatusByNodeName returns the powerState for the specified node.
	GetPowerStatusByNodeName(name string) (string, error)

	// GetProvisioningStateByNodeName returns the provisioningState for the specified node.
	GetProvisioningStateByNodeName(name string) (string, error)

	// GetPrivateIPsByNodeName returns a slice of all private ips assigned to node (ipv6 and ipv4)
	GetPrivateIPsByNodeName(name string) ([]string, error)

	// GetNodeNameByIPConfigurationID gets the nodeName and vmSetName by IP configuration ID.
	GetNodeNameByIPConfigurationID(ipConfigurationID string) (string, string, error)

	// GetNodeCIDRMasksByProviderID returns the node CIDR subnet mask by provider ID.
	GetNodeCIDRMasksByProviderID(providerID string) (int, int, error)

	// GetAgentPoolVMSetNames returns all vmSet names according to the nodes
	GetAgentPoolVMSetNames(nodes []*v1.Node) (*[]string, error)

	// DeleteCacheForNode removes the node entry from cache.
	DeleteCacheForNode(nodeName string) error
}

// lockMap used to lock on entries
type lockMap struct {
	sync.Mutex
	mutexMap map[string]*sync.Mutex
}

// NewLockMap returns a new lock map
func newLockMap() *lockMap {
	return &lockMap{
		mutexMap: make(map[string]*sync.Mutex),
	}
}

// LockEntry acquires a lock associated with the specific entry
func (lm *lockMap) LockEntry(entry string) {
	lm.Lock()
	// check if entry does not exists, then add entry
	mutex, exists := lm.mutexMap[entry]
	if !exists {
		mutex = &sync.Mutex{}
		lm.mutexMap[entry] = mutex
	}
	lm.Unlock()
	mutex.Lock()
}

// UnlockEntry release the lock associated with the specific entry
func (lm *lockMap) UnlockEntry(entry string) {
	lm.Lock()
	defer lm.Unlock()

	mutex, exists := lm.mutexMap[entry]
	if !exists {
		return
	}
	mutex.Unlock()
}
