package azureutils

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	armcompute "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	armnetwork "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v2"

	"github.com/Azure/go-autorest/autorest/azure"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"

	azcache "sigs.k8s.io/cloud-provider-azure/pkg/cache"
	"sigs.k8s.io/cloud-provider-azure/pkg/consts"
)

const (
	// ProvisioningStateDeleting ...
	ProvisioningStateDeleting = "Deleting"
)

var (
	// ErrorNotVmssInstance indicates an instance is not belonging to any vmss.
	ErrorNotVmssInstance = errors.New("not a vmss instance")
	ErrScaleSetNotFound  = errors.New("scale set not found")

	scaleSetNameRE           = regexp.MustCompile(`.*/subscriptions/(?:.*)/Microsoft.Compute/virtualMachineScaleSets/(.+)/virtualMachines(?:.*)`)
	resourceGroupRE          = regexp.MustCompile(`.*/subscriptions/(?:.*)/resourceGroups/(.+)/providers/Microsoft.Compute/virtualMachineScaleSets/(?:.*)/virtualMachines(?:.*)`)
	vmssIPConfigurationRE    = regexp.MustCompile(`.*/subscriptions/(?:.*)/resourceGroups/(.+)/providers/Microsoft.Compute/virtualMachineScaleSets/(.+)/virtualMachines/(.+)/networkInterfaces(?:.*)`)
	vmssPIPConfigurationRE   = regexp.MustCompile(`.*/subscriptions/(?:.*)/resourceGroups/(.+)/providers/Microsoft.Compute/virtualMachineScaleSets/(.+)/virtualMachines/(.+)/networkInterfaces/(.+)/ipConfigurations/(.+)/publicIPAddresses/(.+)`)
	vmssVMResourceIDTemplate = `/subscriptions/(?:.*)/resourceGroups/(.+)/providers/Microsoft.Compute/virtualMachineScaleSets/(.+)/virtualMachines/(?:\d+)`
	vmssVMResourceIDRE       = regexp.MustCompile(vmssVMResourceIDTemplate)
	vmssVMProviderIDRE       = regexp.MustCompile(fmt.Sprintf("%s%s", "azure://", vmssVMResourceIDTemplate))
)

// vmssMetaInfo contains the metadata for a VMSS.
type vmssMetaInfo struct {
	vmssName      string
	resourceGroup string
}

// nodeIdentity identifies a node within a subscription.
type nodeIdentity struct {
	resourceGroup string
	vmssName      string
	nodeName      string
}

// ScaleSet implements VMSet interface for Azure scale set.
type ScaleSet struct {
	*Cloud

	// availabilitySet is also required for scaleSet because some instances
	// (e.g. control plane nodes) may not belong to any scale sets.
	// this also allows for clusters with both VM and VMSS nodes.
	availabilitySet VMSet

	// flexScaleSet is required for self hosted K8s cluster (for example, capz)
	// It is also used when there are vmssflex node and other types of node in
	// the same cluster.
	flexScaleSet VMSet

	// vmssCache is timed cache where the Store in the cache is a map of
	// Key: consts.VMSSKey
	// Value: sync.Map of [vmssName]*VMSSEntry
	vmssCache *TimedCache

	// vmssVMCache is timed cache where the Store in the cache is a map of
	// Key: [resourcegroup/vmssName]
	// Value: sync.Map of [vmName]*VMSSVirtualMachineEntry
	vmssVMCache *TimedCache

	// nonVmssUniformNodesCache is used to store node names from non uniform vm.
	// Currently, the nodes can from avset or vmss flex or individual vm.
	// This cache contains an entry called nonVmssUniformNodesEntry.
	// nonVmssUniformNodesEntry contains avSetVMNodeNames list, clusterNodeNames list
	// and current clusterNodeNames.
	nonVmssUniformNodesCache *TimedCache

	// lockMap in cache refresh
	lockMap *lockMap
}

// newScaleSet creates a new ScaleSet.
func newScaleSet(ctx context.Context, az *Cloud) (VMSet, error) {
	if az.Config.VmssVirtualMachinesCacheTTLInSeconds == 0 {
		az.Config.VmssVirtualMachinesCacheTTLInSeconds = consts.VMSSVirtualMachinesCacheTTLDefaultInSeconds
	}

	var err error
	as, err := newAvailabilitySet(az)
	if err != nil {
		return nil, err
	}
	fs, err := newFlexScaleSet(ctx, az)
	if err != nil {
		return nil, err
	}

	ss := &ScaleSet{
		Cloud:           az,
		availabilitySet: as,
		flexScaleSet:    fs,
		lockMap:         newLockMap(),
	}

	if !ss.DisableAvailabilitySetNodes || ss.EnableVmssFlexNodes {
		ss.nonVmssUniformNodesCache, err = ss.newNonVmssUniformNodesCache()
		if err != nil {
			return nil, err
		}
	}

	ss.vmssCache, err = ss.newVMSSCache(ctx)
	if err != nil {
		return nil, err
	}

	ss.vmssVMCache, err = ss.newVMSSVirtualMachinesCache()
	if err != nil {
		return nil, err
	}

	return ss, nil
}

type VMManagementType string

const (
	ManagedByVmssUniform  VMManagementType = "ManagedByVmssUniform"
	ManagedByVmssFlex     VMManagementType = "ManagedByVmssFlex"
	ManagedByAvSet        VMManagementType = "ManagedByAvSet"
	ManagedByUnknownVMSet VMManagementType = "ManagedByUnknownVMSet"
)

const (
	// NonVmssUniformNodesKey is the key when querying nonVmssUniformNodes cache
	NonVmssUniformNodesKey = "k8sNonVmssUniformNodesKey"

	// VMManagementTypeLockKey is the key for getting the lock for getVMManagementType function
	VMManagementTypeLockKey = "VMManagementType"

	// NonVmssUniformNodesCacheTTLDefaultInSeconds is the TTL of the non vmss uniform node cache
	NonVmssUniformNodesCacheTTLDefaultInSeconds = 900
)

type NonVmssUniformNodesEntry struct {
	VMSSFlexVMNodeNames   sets.Set[string]
	VMSSFlexVMProviderIDs sets.Set[string]
	AvSetVMNodeNames      sets.Set[string]
	AvSetVMProviderIDs    sets.Set[string]
	ClusterNodeNames      sets.Set[string]
}

type VMSSEntry struct {
	VMSS          *armcompute.VirtualMachineScaleSet
	ResourceGroup string
	LastUpdate    time.Time
}

type VMSSVirtualMachineEntry struct {
	ResourceGroup  string
	VMSSName       string
	InstanceID     string
	VirtualMachine *armcompute.VirtualMachineScaleSetVM
	LastUpdate     time.Time
}

func (ss *ScaleSet) newVMSSCache(ctx context.Context) (*TimedCache, error) {
	getter := func(key string) (interface{}, error) {
		localCache := &sync.Map{} // [vmssName]*vmssEntry

		allResourceGroups, err := ss.GetResourceGroups()
		if err != nil {
			return nil, err
		}

		resourceGroupNotFound := false
		for _, resourceGroup := range allResourceGroups.UnsortedList() {
			pager := ss.VirtualMachineScaleSetsClient.NewListPager(resourceGroup, nil)
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
				if scaleSet.Name == nil || *scaleSet.Name == "" {
					klog.Warning("failed to get the name of VMSS")
					continue
				}
				if *scaleSet.Properties.OrchestrationMode == "" || *scaleSet.Properties.OrchestrationMode == armcompute.OrchestrationModeUniform {
					localCache.Store(*scaleSet.Name, &VMSSEntry{
						VMSS:          &scaleSet,
						ResourceGroup: resourceGroup,
						LastUpdate:    time.Now().UTC(),
					})
				}
			}
		}

		if resourceGroupNotFound {
			// gc vmss vm cache when there is resource group not found
			vmssVMKeys := ss.vmssVMCache.Store.ListKeys()
			for _, cacheKey := range vmssVMKeys {
				vmssName := cacheKey[strings.LastIndex(cacheKey, "/")+1:]
				if _, ok := localCache.Load(vmssName); !ok {
					klog.V(2).Infof("remove vmss %s from vmssVMCache due to rg not found", cacheKey)
					_ = ss.vmssVMCache.Delete(cacheKey)
				}
			}
		}
		return localCache, nil
	}

	if ss.Config.VmssCacheTTLInSeconds == 0 {
		ss.Config.VmssCacheTTLInSeconds = consts.VMSSCacheTTLDefaultInSeconds
	}
	return NewTimedcache(time.Duration(ss.Config.VmssCacheTTLInSeconds)*time.Second, getter)
}

// Delete removes an item from the cache.
func (t *TimedCache) Delete(key string) error {
	return t.Store.Delete(&AzureCacheEntry{
		Key: key,
	})
}

func (ss *ScaleSet) getVMManagementTypeByNodeName(nodeName string, crt AzureCacheReadType) (VMManagementType, error) {
	if ss.DisableAvailabilitySetNodes && !ss.EnableVmssFlexNodes {
		return ManagedByVmssUniform, nil
	}
	ss.lockMap.LockEntry(VMManagementTypeLockKey)
	defer ss.lockMap.UnlockEntry(VMManagementTypeLockKey)
	cached, err := ss.nonVmssUniformNodesCache.Get(NonVmssUniformNodesKey, crt)
	if err != nil {
		return ManagedByUnknownVMSet, err
	}

	cachedNodes := cached.(NonVmssUniformNodesEntry).ClusterNodeNames
	// if the node is not in the cache, assume the node has joined after the last cache refresh and attempt to refresh the cache.
	if !cachedNodes.Has(nodeName) {
		if cached.(NonVmssUniformNodesEntry).AvSetVMNodeNames.Has(nodeName) {
			return ManagedByAvSet, nil
		}

		if cached.(NonVmssUniformNodesEntry).VMSSFlexVMNodeNames.Has(nodeName) {
			return ManagedByVmssFlex, nil
		}

		if isNodeInVMSSVMCache(nodeName, ss.vmssVMCache) {
			return ManagedByVmssUniform, nil
		}

		klog.V(2).Infof("Node %s has joined the cluster since the last VM cache refresh in NonVmssUniformNodesEntry, refreshing the cache", nodeName)
		cached, err = ss.nonVmssUniformNodesCache.Get(NonVmssUniformNodesKey, CacheReadTypeForceRefresh)
		if err != nil {
			return ManagedByUnknownVMSet, err
		}
	}

	cachedAvSetVMs := cached.(NonVmssUniformNodesEntry).AvSetVMNodeNames
	cachedVmssFlexVMs := cached.(NonVmssUniformNodesEntry).VMSSFlexVMNodeNames

	if cachedAvSetVMs.Has(nodeName) {
		return ManagedByAvSet, nil
	}
	if cachedVmssFlexVMs.Has(nodeName) {
		return ManagedByVmssFlex, nil
	}

	return ManagedByVmssUniform, nil
}

func (ss *ScaleSet) newNonVmssUniformNodesCache() (*TimedCache, error) {
	getter := func(key string) (interface{}, error) {
		vmssFlexVMNodeNames := sets.New[string]()
		vmssFlexVMProviderIDs := sets.New[string]()
		avSetVMNodeNames := sets.New[string]()
		avSetVMProviderIDs := sets.New[string]()
		resourceGroups, err := ss.GetResourceGroups()
		if err != nil {
			return nil, err
		}
		klog.V(2).Infof("refresh the cache of NonVmssUniformNodesCache in rg %v", resourceGroups)

		for _, resourceGroup := range resourceGroups.UnsortedList() {
			vms, err := ss.Cloud.ListVirtualMachines(resourceGroup)
			if err != nil {
				return nil, fmt.Errorf("getter function of nonVmssUniformNodesCache: failed to list vms in the resource group %s: %w", resourceGroup, err)
			}
			for _, vm := range vms {
				if vm.Properties.OSProfile != nil && vm.Properties.OSProfile.ComputerName != nil {
					if vm.Properties.VirtualMachineScaleSet != nil {
						vmssFlexVMNodeNames.Insert(strings.ToLower(pointer.StringDeref(vm.Properties.OSProfile.ComputerName, "")))
						if vm.ID != nil {
							vmssFlexVMProviderIDs.Insert(ss.ProviderName() + "://" + pointer.StringDeref(vm.ID, ""))
						}
					} else {
						avSetVMNodeNames.Insert(strings.ToLower(pointer.StringDeref(vm.Properties.OSProfile.ComputerName, "")))
						if vm.ID != nil {
							avSetVMProviderIDs.Insert(ss.ProviderName() + "://" + pointer.StringDeref(vm.ID, ""))
						}
					}
				}
			}
		}

		// store all the node names in the cluster when the cache data was created.
		nodeNames, err := ss.GetNodeNames()
		if err != nil {
			return nil, err
		}

		localCache := NonVmssUniformNodesEntry{
			VMSSFlexVMNodeNames:   vmssFlexVMNodeNames,
			VMSSFlexVMProviderIDs: vmssFlexVMProviderIDs,
			AvSetVMNodeNames:      avSetVMNodeNames,
			AvSetVMProviderIDs:    avSetVMProviderIDs,
			ClusterNodeNames:      nodeNames,
		}

		return localCache, nil
	}

	if ss.Config.NonVmssUniformNodesCacheTTLInSeconds == 0 {
		ss.Config.NonVmssUniformNodesCacheTTLInSeconds = NonVmssUniformNodesCacheTTLDefaultInSeconds
	}
	return NewTimedcache(time.Duration(ss.Config.NonVmssUniformNodesCacheTTLInSeconds)*time.Second, getter)
}

// isNodeInVMSSVMCache check whether nodeName is in vmssVMCache
func isNodeInVMSSVMCache(nodeName string, vmssVMCache *TimedCache) bool {
	if vmssVMCache == nil {
		return false
	}

	var isInCache bool

	vmssVMCache.Lock.Lock()
	defer vmssVMCache.Lock.Unlock()

	for _, entry := range vmssVMCache.Store.List() {
		if entry != nil {
			e := entry.(*AzureCacheEntry)
			e.Lock.Lock()
			data := e.Data
			if data != nil {
				data.(*sync.Map).Range(func(vmName, _ interface{}) bool {
					if vmName != nil && vmName.(string) == nodeName {
						isInCache = true
						return false
					}
					return true
				})
			}
			e.Lock.Unlock()
		}

		if isInCache {
			break
		}
	}

	return isInCache
}

// newVMSSVirtualMachinesCache instantiates a new VMs cache for VMs belonging to the provided VMSS.
func (ss *ScaleSet) newVMSSVirtualMachinesCache() (*TimedCache, error) {
	vmssVirtualMachinesCacheTTL := time.Duration(ss.Config.VmssVirtualMachinesCacheTTLInSeconds) * time.Second

	getter := func(cacheKey string) (interface{}, error) {
		localCache := &sync.Map{} // [nodeName]*VMSSVirtualMachineEntry
		oldCache := make(map[string]*VMSSVirtualMachineEntry)

		entry, exists, err := ss.vmssVMCache.Store.GetByKey(cacheKey)
		if err != nil {
			return nil, err
		}
		if exists {
			cached := entry.(*azcache.AzureCacheEntry).Data
			if cached != nil {
				virtualMachines := cached.(*sync.Map)
				virtualMachines.Range(func(key, value interface{}) bool {
					oldCache[key.(string)] = value.(*VMSSVirtualMachineEntry)
					return true
				})
			}
		}

		result := strings.Split(cacheKey, "/")
		if len(result) < 2 {
			err = fmt.Errorf("Invalid cacheKey (%s)", cacheKey)
			return nil, err
		}

		resourceGroupName, vmssName := result[0], result[1]

		cred, err := azidentity.NewDefaultAzureCredential(nil)
		if err != nil {
			klog.Fatalf("failed to obtain new credential: %v", err)
		}

		client, err := armcompute.NewVirtualMachineScaleSetVMsClient(ss.Cloud.SubscriptionID, cred, nil)
		if err != nil {
			klog.Fatalf("failed to create client: %v", err)
		}

		var vms []armcompute.VirtualMachineScaleSetVM
		pager := client.NewListPager(resourceGroupName, vmssName, nil)
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
			if vm.Properties.OSProfile == nil || vm.Properties.OSProfile.ComputerName == nil {
				klog.Warningf("failed to get computerName for vmssVM (%q)", vmssName)
				continue
			}

			computerName := strings.ToLower(*vm.Properties.OSProfile.ComputerName)
			if vm.Properties.NetworkProfile == nil || vm.Properties.NetworkProfile.NetworkInterfaces == nil {
				klog.Warningf("skip caching vmssVM %s since its network profile hasn't initialized yet (probably still under creating)", computerName)
				continue
			}

			vmssVMCacheEntry := &VMSSVirtualMachineEntry{
				ResourceGroup:  resourceGroupName,
				VMSSName:       vmssName,
				InstanceID:     pointer.StringDeref(vm.InstanceID, ""),
				VirtualMachine: &vm,
				LastUpdate:     time.Now().UTC(),
			}
			// set cache entry to nil when the VM is under deleting.
			if vm.Properties != nil &&
				strings.EqualFold(pointer.StringDeref(vm.Properties.ProvisioningState, ""), string(ProvisioningStateDeleting)) {
				klog.V(4).Infof("VMSS virtualMachine %q is under deleting, setting its cache to nil", computerName)
				vmssVMCacheEntry.VirtualMachine = nil
			}
			localCache.Store(computerName, vmssVMCacheEntry)

			delete(oldCache, computerName)
		}

		// add old missing cache data with nil entries to prevent aggressive
		// ARM calls during cache invalidation
		for name, vmEntry := range oldCache {
			// if the nil cache entry has existed for vmssVirtualMachinesCacheTTL in the cache
			// then it should not be added back to the cache
			if vmEntry.VirtualMachine == nil && time.Since(vmEntry.LastUpdate) > vmssVirtualMachinesCacheTTL {
				klog.V(5).Infof("ignoring expired entries from old cache for %s", name)
				continue
			}
			LastUpdate := time.Now().UTC()
			if vmEntry.VirtualMachine == nil {
				// if this is already a nil entry then keep the time the nil
				// entry was first created, so we can cleanup unwanted entries
				LastUpdate = vmEntry.LastUpdate
			}

			klog.V(5).Infof("adding old entries to new cache for %s", name)
			localCache.Store(name, &VMSSVirtualMachineEntry{
				ResourceGroup:  vmEntry.ResourceGroup,
				VMSSName:       vmEntry.VMSSName,
				InstanceID:     vmEntry.InstanceID,
				VirtualMachine: nil,
				LastUpdate:     LastUpdate,
			})
		}

		return localCache, nil
	}

	return NewTimedcache(vmssVirtualMachinesCacheTTL, getter)
}

// PLACEHOLDERS
// GetInstanceIDByNodeName gets the cloud provider ID by node name.
// It must return ("", cloudprovider.InstanceNotFound) if the instance does
// not exist or is no longer running.
func (ss *ScaleSet) GetInstanceIDByNodeName(name string) (string, error)

// GetInstanceTypeByNodeName gets the instance type by node name.
func (ss *ScaleSet) GetInstanceTypeByNodeName(name string) (string, error)

// GetIPByNodeName gets machine private IP and public IP by node name.
func (ss *ScaleSet) GetIPByNodeName(name string) (string, string, error)

// GetPrimaryInterface gets machine primary network interface by node name.
func (ss *ScaleSet) GetPrimaryInterface(nodeName string) (armnetwork.Interface, error)

// GetNodeNameByProviderID gets the node name by provider ID.
func (ss *ScaleSet) GetNodeNameByProviderID(providerID string) (types.NodeName, error)

// GetZoneByNodeName gets cloudprovider.Zone by node name.
func (ss *ScaleSet) GetZoneByNodeName(name string) (cloudprovider.Zone, error)

// GetPrimaryVMSetName returns the VM set name depending on the configured vmType.
// It returns config.PrimaryScaleSetName for vmss and config.PrimaryAvailabilitySetName for standard vmType.
func (ss *ScaleSet) GetPrimaryVMSetName() string

// GetVMSetNames selects all possible availability sets or scale sets
// (depending vmType configured) for service load balancer, if the service has
// no loadbalancer mode annotation returns the primary VMSet. If service annotation
// for loadbalancer exists then return the eligible VMSet.
func (ss *ScaleSet) GetVMSetNames(service *v1.Service, nodes []*v1.Node) (availabilitySetNames *[]string, err error)

// GetNodeVMSetName returns the availability set or vmss name by the node name.
// It will return empty string when using standalone vms.
func (ss *ScaleSet) GetNodeVMSetName(node *v1.Node) (string, error)

// EnsureHostsInPool ensures the given Node's primary IP configurations are
// participating in the specified LoadBalancer Backend Pool.
func (ss *ScaleSet) EnsureHostsInPool(service *v1.Service, nodes []*v1.Node, backendPoolID string, vmSetName string) error

// EnsureHostInPool ensures the given VM's Primary NIC's Primary IP Configuration is
// participating in the specified LoadBalancer Backend Pool.
func (ss *ScaleSet) EnsureHostInPool(service *v1.Service, nodeName types.NodeName, backendPoolID string, vmSetName string) (string, string, string, *armcompute.VirtualMachineScaleSetVM, error)

// EnsureBackendPoolDeleted ensures the loadBalancer backendAddressPools deleted from the specified nodes.
func (ss *ScaleSet) EnsureBackendPoolDeleted(service *v1.Service, backendPoolIDs []string, vmSetName string, backendAddressPools *[]armnetwork.BackendAddressPool, deleteFromVMSet bool) (bool, error)

// EnsureBackendPoolDeletedFromVMSets ensures the loadBalancer backendAddressPools deleted from the specified VMSS/VMAS
func (ss *ScaleSet) EnsureBackendPoolDeletedFromVMSets(vmSetNamesMap map[string]bool, backendPoolIDs []string) error

// AttachDisk attaches a disk to vm
func (ss *ScaleSet) AttachDisk(ctx context.Context, nodeName types.NodeName, diskMap map[string]*AttachDiskOptions) (*azure.Future, error)

// DetachDisk detaches a disk from vm
func (ss *ScaleSet) DetachDisk(ctx context.Context, nodeName types.NodeName, diskMap map[string]string) error

// WaitForUpdateResult waits for the response of the update request
func (ss *ScaleSet) WaitForUpdateResult(ctx context.Context, future *azure.Future, nodeName types.NodeName, source string) error

// GetDataDisks gets a list of data disks attached to the node.
func (ss *ScaleSet) GetDataDisks(nodeName types.NodeName, crt AzureCacheReadType) ([]armcompute.DataDisk, *string, error)

// UpdateVM updates a vm
func (ss *ScaleSet) UpdateVM(ctx context.Context, nodeName types.NodeName) error

// UpdateVMAsync updates a vm asynchronously
func (ss *ScaleSet) UpdateVMAsync(ctx context.Context, nodeName types.NodeName) (*azure.Future, error)

// GetPowerStatusByNodeName returns the powerState for the specified node.
func (ss *ScaleSet) GetPowerStatusByNodeName(name string) (string, error)

// GetProvisioningStateByNodeName returns the provisioningState for the specified node.
func (ss *ScaleSet) GetProvisioningStateByNodeName(name string) (string, error)

// GetPrivateIPsByNodeName returns a slice of all private ips assigned to node (ipv6 and ipv4)
func (ss *ScaleSet) GetPrivateIPsByNodeName(name string) ([]string, error)

// GetNodeNameByIPConfigurationID gets the nodeName and vmSetName by IP configuration ID.
func (ss *ScaleSet) GetNodeNameByIPConfigurationID(ipConfigurationID string) (string, string, error)

// GetNodeCIDRMasksByProviderID returns the node CIDR subnet mask by provider ID.
func (ss *ScaleSet) GetNodeCIDRMasksByProviderID(providerID string) (int, int, error)

// GetAgentPoolVMSetNames returns all vmSet names according to the nodes
func (ss *ScaleSet) GetAgentPoolVMSetNames(nodes []*v1.Node) (*[]string, error)

// DeleteCacheForNode removes the node entry from cache.
func (ss *ScaleSet) DeleteCacheForNode(nodeName string) error
