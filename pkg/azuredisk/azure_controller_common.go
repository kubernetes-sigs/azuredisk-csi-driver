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

package azuredisk

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2022-08-01/compute"

	"k8s.io/apimachinery/pkg/types"
	kwait "k8s.io/apimachinery/pkg/util/wait"
	cloudprovider "k8s.io/cloud-provider"
	volerr "k8s.io/cloud-provider/volume/errors"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/cloud-provider-azure/pkg/azclient"
	azcache "sigs.k8s.io/cloud-provider-azure/pkg/cache"
	"sigs.k8s.io/cloud-provider-azure/pkg/consts"
	"sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

const (
	// Disk Caching is not supported for disks 4 TiB and larger
	// https://docs.microsoft.com/en-us/azure/virtual-machines/premium-storage-performance#disk-caching
	diskCachingLimit = 4096 // GiB

	maxLUN                 = 64 // max number of LUNs per VM
	errStatusCode400       = "statuscode=400"
	errInvalidParameter    = `code="invalidparameter"`
	errTargetInstanceIDs   = `target="instanceids"`
	sourceSnapshot         = "snapshot"
	sourceVolume           = "volume"
	attachDiskMapKeySuffix = "attachdiskmap"
	detachDiskMapKeySuffix = "detachdiskmap"

	// default initial delay in milliseconds for batch disk attach/detach
	defaultAttachDetachInitialDelayInMs = 1000

	// WriteAcceleratorEnabled support for Azure Write Accelerator on Azure Disks
	// https://docs.microsoft.com/azure/virtual-machines/windows/how-to-enable-write-accelerator
	WriteAcceleratorEnabled = "writeacceleratorenabled"

	// see https://docs.microsoft.com/en-us/rest/api/compute/disks/createorupdate#create-a-managed-disk-by-copying-a-snapshot.
	diskSnapshotPath = "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/snapshots/%s"

	// see https://docs.microsoft.com/en-us/rest/api/compute/disks/createorupdate#create-a-managed-disk-from-an-existing-managed-disk-in-the-same-or-different-subscription.
	managedDiskPath = "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/disks/%s"
)

var defaultBackOff = kwait.Backoff{
	Steps:    20,
	Duration: 2 * time.Second,
	Factor:   1.5,
	Jitter:   0.0,
}

var (
	managedDiskPathRE  = regexp.MustCompile(`.*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/disks/(.+)`)
	diskSnapshotPathRE = regexp.MustCompile(`.*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/snapshots/(.+)`)
)

type controllerCommon struct {
	diskStateMap  sync.Map // <diskURI, attaching/detaching state>
	lockMap       *lockMap
	cloud         *provider.Cloud
	clientFactory azclient.ClientFactory
	// disk queue that is waiting for attach or detach on specific node
	// <nodeName, map<diskURI, *provider.AttachDiskOptions/DetachDiskOptions>>
	attachDiskMap sync.Map
	detachDiskMap sync.Map
	// DisableUpdateCache whether disable update cache in disk attach/detach
	DisableUpdateCache bool
	// DisableDiskLunCheck whether disable disk lun check after disk attach/detach
	DisableDiskLunCheck bool
	// AttachDetachInitialDelayInMs determines initial delay in milliseconds for batch disk attach/detach
	AttachDetachInitialDelayInMs int
	ForceDetachBackoff           bool
}

// ExtendedLocation contains additional info about the location of resources.
type ExtendedLocation struct {
	// Name - The name of the extended location.
	Name string `json:"name,omitempty"`
	// Type - The type of the extended location.
	Type string `json:"type,omitempty"`
}

// AttachDisk attaches a disk to vm
// occupiedLuns is used to avoid conflict with other disk attach in k8s VolumeAttachments
// return (lun, error)
func (c *controllerCommon) AttachDisk(ctx context.Context, diskName, diskURI string, nodeName types.NodeName,
	cachingMode armcompute.CachingTypes, disk *armcompute.Disk, occupiedLuns []int) (int32, error) {
	diskEncryptionSetID := ""
	writeAcceleratorEnabled := false

	// there is possibility that disk is nil when GetDisk is throttled
	// don't check disk state when GetDisk is throttled
	if disk != nil {
		if disk.ManagedBy != nil && (disk.Properties == nil || disk.Properties.MaxShares == nil || *disk.Properties.MaxShares <= 1) {
			vmset, err := c.cloud.GetNodeVMSet(nodeName, azcache.CacheReadTypeUnsafe)
			if err != nil {
				return -1, err
			}
			attachedNode, err := vmset.GetNodeNameByProviderID(*disk.ManagedBy)
			if err != nil {
				return -1, err
			}
			if strings.EqualFold(string(nodeName), string(attachedNode)) {
				klog.Warningf("volume %s is actually attached to current node %s, invalidate vm cache and return error", diskURI, nodeName)
				// update VM(invalidate vm cache)
				if errUpdate := c.UpdateVM(ctx, nodeName); errUpdate != nil {
					return -1, errUpdate
				}
				lun, _, err := c.GetDiskLun(diskName, diskURI, nodeName)
				return lun, err
			}

			attachErr := fmt.Sprintf(
				"disk(%s) already attached to node(%s), could not be attached to node(%s)",
				diskURI, *disk.ManagedBy, nodeName)
			klog.V(2).Infof("found dangling volume %s attached to node %s, could not be attached to node(%s)", diskURI, attachedNode, nodeName)
			return -1, volerr.NewDanglingError(attachErr, attachedNode, "")
		}

		if disk.Properties != nil {
			if disk.Properties.DiskSizeGB != nil && *disk.Properties.DiskSizeGB >= diskCachingLimit && cachingMode != armcompute.CachingTypesNone {
				// Disk Caching is not supported for disks 4 TiB and larger
				// https://docs.microsoft.com/en-us/azure/virtual-machines/premium-storage-performance#disk-caching
				cachingMode = armcompute.CachingTypesNone
				klog.Warningf("size of disk(%s) is %dGB which is bigger than limit(%dGB), set cacheMode as None",
					diskURI, *disk.Properties.DiskSizeGB, diskCachingLimit)
			}

			if disk.Properties.Encryption != nil &&
				disk.Properties.Encryption.DiskEncryptionSetID != nil {
				diskEncryptionSetID = *disk.Properties.Encryption.DiskEncryptionSetID
			}

			if disk.Properties.DiskState != nil && *disk.Properties.DiskState != armcompute.DiskStateUnattached && (disk.Properties.MaxShares == nil || *disk.Properties.MaxShares <= 1) {
				return -1, fmt.Errorf("state of disk(%s) is %s, not in expected %s state", diskURI, *disk.Properties.DiskState, armcompute.DiskStateUnattached)
			}
		}
		if disk.SKU != nil && disk.SKU.Name != nil && *disk.SKU.Name == armcompute.DiskStorageAccountTypesPremiumV2LRS {
			klog.V(2).Infof("disk(%s) is PremiumV2LRS and only supports None caching mode", diskURI)
			cachingMode = armcompute.CachingTypesNone
		}

		if v, ok := disk.Tags[WriteAcceleratorEnabled]; ok {
			if v != nil && strings.EqualFold(*v, "true") {
				writeAcceleratorEnabled = true
			}
		}
	}

	options := provider.AttachDiskOptions{
		Lun:                     -1,
		DiskName:                diskName,
		CachingMode:             compute.CachingTypes(cachingMode),
		DiskEncryptionSetID:     diskEncryptionSetID,
		WriteAcceleratorEnabled: writeAcceleratorEnabled,
	}
	node := strings.ToLower(string(nodeName))
	diskuri := strings.ToLower(diskURI)
	requestNum, err := c.insertAttachDiskRequest(diskuri, node, &options)
	if err != nil {
		return -1, err
	}

	c.lockMap.LockEntry(node)
	defer c.lockMap.UnlockEntry(node)

	if c.AttachDetachInitialDelayInMs > 0 && requestNum == 1 {
		klog.V(2).Infof("wait %dms for more requests on node %s, current disk attach: %s", c.AttachDetachInitialDelayInMs, node, diskURI)
		time.Sleep(time.Duration(c.AttachDetachInitialDelayInMs) * time.Millisecond)
	}

	diskMap, err := c.cleanAttachDiskRequests(node)
	if err != nil {
		return -1, err
	}

	lun, err := c.SetDiskLun(nodeName, diskuri, diskMap, occupiedLuns)
	if err != nil {
		return -1, err
	}

	klog.V(2).Infof("Trying to attach volume %s lun %d to node %s, diskMap len:%d, %+v", diskURI, lun, nodeName, len(diskMap), diskMap)
	if len(diskMap) == 0 {
		if !c.DisableDiskLunCheck {
			// always check disk lun after disk attach complete
			diskLun, vmState, errGetLun := c.GetDiskLun(diskName, diskURI, nodeName)
			if errGetLun != nil {
				return -1, fmt.Errorf("disk(%s) could not be found on node(%s), vmState: %s, error: %w", diskURI, nodeName, pointer.StringDeref(vmState, ""), errGetLun)
			}
			lun = diskLun
		}
		return lun, nil
	}

	vmset, err := c.cloud.GetNodeVMSet(nodeName, azcache.CacheReadTypeUnsafe)
	if err != nil {
		return -1, err
	}
	c.diskStateMap.Store(disk, "attaching")
	defer c.diskStateMap.Delete(disk)

	defer func() {
		// invalidate the cache if there is error in disk attach
		if err != nil {
			_ = vmset.DeleteCacheForNode(string(nodeName))
		}
	}()

	err = vmset.AttachDisk(ctx, nodeName, diskMap)
	if err != nil {
		if IsOperationPreempted(err) {
			klog.Errorf("Retry VM Update on node (%s) due to error (%v)", nodeName, err)
			err = vmset.UpdateVM(ctx, nodeName)
		}
		if err != nil {
			return -1, err
		}
	}

	if !c.DisableDiskLunCheck {
		// always check disk lun after disk attach complete
		diskLun, vmState, errGetLun := c.GetDiskLun(diskName, diskURI, nodeName)
		if errGetLun != nil {
			return -1, fmt.Errorf("disk(%s) could not be found on node(%s), vmState: %s, error: %w", diskURI, nodeName, pointer.StringDeref(vmState, ""), errGetLun)
		}
		lun = diskLun
	}
	return lun, nil
}

// insertAttachDiskRequest return (attachDiskRequestQueueLength, error)
func (c *controllerCommon) insertAttachDiskRequest(diskURI, nodeName string, options *provider.AttachDiskOptions) (int, error) {
	var diskMap map[string]*provider.AttachDiskOptions
	attachDiskMapKey := nodeName + attachDiskMapKeySuffix
	c.lockMap.LockEntry(attachDiskMapKey)
	defer c.lockMap.UnlockEntry(attachDiskMapKey)
	v, ok := c.attachDiskMap.Load(nodeName)
	if ok {
		if diskMap, ok = v.(map[string]*provider.AttachDiskOptions); !ok {
			return -1, fmt.Errorf("convert attachDiskMap failure on node(%s)", nodeName)
		}
	} else {
		diskMap = make(map[string]*provider.AttachDiskOptions)
		c.attachDiskMap.Store(nodeName, diskMap)
	}
	// insert attach disk request to queue
	_, ok = diskMap[diskURI]
	if ok {
		klog.V(2).Infof("azureDisk - duplicated attach disk(%s) request on node(%s)", diskURI, nodeName)
	} else {
		diskMap[diskURI] = options
	}
	return len(diskMap), nil
}

// clean up attach disk requests
// return original attach disk requests
func (c *controllerCommon) cleanAttachDiskRequests(nodeName string) (map[string]*provider.AttachDiskOptions, error) {
	var diskMap map[string]*provider.AttachDiskOptions

	attachDiskMapKey := nodeName + attachDiskMapKeySuffix
	c.lockMap.LockEntry(attachDiskMapKey)
	defer c.lockMap.UnlockEntry(attachDiskMapKey)
	v, ok := c.attachDiskMap.Load(nodeName)
	if !ok {
		return diskMap, nil
	}
	if diskMap, ok = v.(map[string]*provider.AttachDiskOptions); !ok {
		return diskMap, fmt.Errorf("convert attachDiskMap failure on node(%s)", nodeName)
	}
	c.attachDiskMap.Store(nodeName, make(map[string]*provider.AttachDiskOptions))
	return diskMap, nil
}

// DetachDisk detaches a disk from VM
func (c *controllerCommon) DetachDisk(ctx context.Context, diskName, diskURI string, nodeName types.NodeName) error {
	if _, err := c.cloud.InstanceID(ctx, nodeName); err != nil {
		if errors.Is(err, cloudprovider.InstanceNotFound) {
			// if host doesn't exist, no need to detach
			klog.Warningf("azureDisk - failed to get azure instance id(%s), DetachDisk(%s) will assume disk is already detached",
				nodeName, diskURI)
			return nil
		}
		klog.Warningf("failed to get azure instance id (%v)", err)
		return fmt.Errorf("failed to get azure instance id for node %q: %w", nodeName, err)
	}

	vmset, err := c.cloud.GetNodeVMSet(nodeName, azcache.CacheReadTypeUnsafe)
	if err != nil {
		return err
	}

	node := strings.ToLower(string(nodeName))
	disk := strings.ToLower(diskURI)
	requestNum, err := c.insertDetachDiskRequest(diskName, disk, node)
	if err != nil {
		return err
	}

	c.lockMap.LockEntry(node)
	defer c.lockMap.UnlockEntry(node)

	if c.AttachDetachInitialDelayInMs > 0 && requestNum == 1 {
		klog.V(2).Infof("wait %dms for more requests on node %s, current disk detach: %s", c.AttachDetachInitialDelayInMs, node, diskURI)
		time.Sleep(time.Duration(c.AttachDetachInitialDelayInMs) * time.Millisecond)
	}
	diskMap, err := c.cleanDetachDiskRequests(node)
	if err != nil {
		return err
	}

	klog.V(2).Infof("Trying to detach volume %s from node %s, diskMap len:%d, %s", diskURI, nodeName, len(diskMap), diskMap)
	if len(diskMap) > 0 {
		c.diskStateMap.Store(disk, "detaching")
		defer c.diskStateMap.Delete(disk)
		if err = vmset.DetachDisk(ctx, nodeName, diskMap, false); err != nil {
			if isInstanceNotFoundError(err) {
				// if host doesn't exist, no need to detach
				klog.Warningf("azureDisk - got InstanceNotFoundError(%v), DetachDisk(%s) will assume disk is already detached",
					err, diskURI)
				return nil
			}
			if c.ForceDetachBackoff && !azureutils.IsThrottlingError(err) {
				klog.Errorf("azureDisk - DetachDisk(%s) from node %s failed with error: %v, retry with force detach", diskURI, nodeName, err)
				err = vmset.DetachDisk(ctx, nodeName, diskMap, true)
			}
		}
	}

	if err != nil {
		klog.Errorf("azureDisk - detach disk(%s, %s) failed, err: %v", diskName, diskURI, err)
		return err
	}

	if !c.DisableDiskLunCheck {
		// always check disk lun after disk detach complete
		lun, vmState, errGetLun := c.GetDiskLun(diskName, diskURI, nodeName)
		if errGetLun == nil || !strings.Contains(errGetLun.Error(), consts.CannotFindDiskLUN) {
			return fmt.Errorf("disk(%s) is still attached to node(%s) on lun(%d), vmState: %s, error: %w", diskURI, nodeName, lun, pointer.StringDeref(vmState, ""), errGetLun)
		}
	}

	klog.V(2).Infof("azureDisk - detach disk(%s, %s) succeeded", diskName, diskURI)
	return nil
}

// UpdateVM updates a vm
func (c *controllerCommon) UpdateVM(ctx context.Context, nodeName types.NodeName) error {
	vmset, err := c.cloud.GetNodeVMSet(nodeName, azcache.CacheReadTypeUnsafe)
	if err != nil {
		return err
	}
	node := strings.ToLower(string(nodeName))
	c.lockMap.LockEntry(node)
	defer c.lockMap.UnlockEntry(node)

	defer func() {
		_ = vmset.DeleteCacheForNode(string(nodeName))
	}()

	klog.V(2).Infof("azureDisk - update: vm(%s)", nodeName)
	return vmset.UpdateVM(ctx, nodeName)
}

// insertDetachDiskRequest return (detachDiskRequestQueueLength, error)
func (c *controllerCommon) insertDetachDiskRequest(diskName, diskURI, nodeName string) (int, error) {
	var diskMap map[string]string
	detachDiskMapKey := nodeName + detachDiskMapKeySuffix
	c.lockMap.LockEntry(detachDiskMapKey)
	defer c.lockMap.UnlockEntry(detachDiskMapKey)
	v, ok := c.detachDiskMap.Load(nodeName)
	if ok {
		if diskMap, ok = v.(map[string]string); !ok {
			return -1, fmt.Errorf("convert detachDiskMap failure on node(%s)", nodeName)
		}
	} else {
		diskMap = make(map[string]string)
		c.detachDiskMap.Store(nodeName, diskMap)
	}
	// insert detach disk request to queue
	_, ok = diskMap[diskURI]
	if ok {
		klog.V(2).Infof("azureDisk - duplicated detach disk(%s) request on node(%s)", diskURI, nodeName)
	} else {
		diskMap[diskURI] = diskName
	}
	return len(diskMap), nil
}

// clean up detach disk requests
// return original detach disk requests
func (c *controllerCommon) cleanDetachDiskRequests(nodeName string) (map[string]string, error) {
	var diskMap map[string]string

	detachDiskMapKey := nodeName + detachDiskMapKeySuffix
	c.lockMap.LockEntry(detachDiskMapKey)
	defer c.lockMap.UnlockEntry(detachDiskMapKey)
	v, ok := c.detachDiskMap.Load(nodeName)
	if !ok {
		return diskMap, nil
	}
	if diskMap, ok = v.(map[string]string); !ok {
		return diskMap, fmt.Errorf("convert detachDiskMap failure on node(%s)", nodeName)
	}
	// clean up original requests in disk map
	c.detachDiskMap.Store(nodeName, make(map[string]string))
	return diskMap, nil
}

// GetNodeDataDisks invokes vmSet interfaces to get data disks for the node.
func (c *controllerCommon) GetNodeDataDisks(nodeName types.NodeName, crt azcache.AzureCacheReadType) ([]*armcompute.DataDisk, *string, error) {
	vmset, err := c.cloud.GetNodeVMSet(nodeName, crt)
	if err != nil {
		return nil, nil, err
	}

	return vmset.GetDataDisks(nodeName, crt)
}

// GetDiskLun finds the lun on the host that the vhd is attached to, given a vhd's diskName and diskURI.
func (c *controllerCommon) GetDiskLun(diskName, diskURI string, nodeName types.NodeName) (int32, *string, error) {
	// GetNodeDataDisks need to fetch the cached data/fresh data if cache expired here
	// to ensure we get LUN based on latest entry.
	disks, provisioningState, err := c.GetNodeDataDisks(nodeName, azcache.CacheReadTypeDefault)
	if err != nil {
		klog.Errorf("error of getting data disks for node %s: %v", nodeName, err)
		return -1, provisioningState, err
	}

	for _, disk := range disks {
		if disk.Lun != nil && (disk.Name != nil && diskName != "" && strings.EqualFold(*disk.Name, diskName)) ||
			(disk.Vhd != nil && disk.Vhd.URI != nil && diskURI != "" && strings.EqualFold(*disk.Vhd.URI, diskURI)) ||
			(disk.ManagedDisk != nil && strings.EqualFold(*disk.ManagedDisk.ID, diskURI)) {
			if disk.ToBeDetached != nil && *disk.ToBeDetached {
				klog.Warningf("azureDisk - found disk(ToBeDetached): lun %d name %s uri %s", *disk.Lun, diskName, diskURI)
			} else {
				// found the disk
				klog.V(2).Infof("azureDisk - found disk: lun %d name %s uri %s", *disk.Lun, diskName, diskURI)
				return *disk.Lun, provisioningState, nil
			}
		}
	}
	return -1, provisioningState, fmt.Errorf("%s for disk %s", consts.CannotFindDiskLUN, diskName)
}

// SetDiskLun find unused luns and allocate lun for every disk in diskMap.
// occupiedLuns is used to avoid conflict with other disk attach in k8s VolumeAttachments
// Return lun of diskURI, -1 if all luns are used.
func (c *controllerCommon) SetDiskLun(nodeName types.NodeName, diskURI string, diskMap map[string]*provider.AttachDiskOptions, occupiedLuns []int) (int32, error) {
	disks, _, err := c.GetNodeDataDisks(nodeName, azcache.CacheReadTypeDefault)
	if err != nil {
		klog.Errorf("error of getting data disks for node %s: %v", nodeName, err)
		return -1, err
	}

	lun := int32(-1)
	_, isDiskInMap := diskMap[diskURI]
	used := make([]bool, maxLUN)
	for _, disk := range disks {
		if disk.Lun != nil {
			used[*disk.Lun] = true
			if !isDiskInMap {
				// find lun of diskURI since diskURI is not in diskMap
				if disk.ManagedDisk != nil && strings.EqualFold(*disk.ManagedDisk.ID, diskURI) {
					lun = *disk.Lun
				}
			}
		}
	}
	if !isDiskInMap && lun < 0 {
		return -1, fmt.Errorf("could not find disk(%s) in current disk list(len: %d) nor in diskMap(%v)", diskURI, len(disks), diskMap)
	}
	if len(diskMap) == 0 {
		// attach disk request is empty, return directly
		return lun, nil
	}

	for _, lun := range occupiedLuns {
		if lun >= 0 && lun <= maxLUN {
			used[int32(lun)] = true
		}
	}

	// allocate lun for every disk in diskMap
	var diskLuns []int32
	count := 0
	for k, v := range used {
		if !v {
			diskLuns = append(diskLuns, int32(k))
			count++
			if count >= len(diskMap) {
				break
			}
		}
	}

	if len(diskLuns) != len(diskMap) {
		return -1, fmt.Errorf("could not find enough disk luns(current: %d) for diskMap(%v, len=%d), diskURI(%s)",
			len(diskLuns), diskMap, len(diskMap), diskURI)
	}

	count = 0
	for uri, opt := range diskMap {
		if opt == nil {
			return -1, fmt.Errorf("unexpected nil pointer in diskMap(%v), diskURI(%s)", diskMap, diskURI)
		}
		if strings.EqualFold(uri, diskURI) {
			lun = diskLuns[count]
		}
		opt.Lun = diskLuns[count]
		count++
	}
	if lun < 0 {
		return lun, fmt.Errorf("could not find lun of diskURI(%s), diskMap(%v)", diskURI, diskMap)
	}
	return lun, nil
}

// DisksAreAttached checks if a list of volumes are attached to the node with the specified NodeName.
func (c *controllerCommon) DisksAreAttached(diskNames []string, nodeName types.NodeName) (map[string]bool, error) {
	attached := make(map[string]bool)
	for _, diskName := range diskNames {
		attached[diskName] = false
	}

	// doing stalled read for GetNodeDataDisks to ensure we don't call ARM
	// for every reconcile call. The cache is invalidated after Attach/Detach
	// disk. So the new entry will be fetched and cached the first time reconcile
	// loop runs after the Attach/Disk OP which will reflect the latest model.
	disks, _, err := c.GetNodeDataDisks(nodeName, azcache.CacheReadTypeUnsafe)
	if err != nil {
		if errors.Is(err, cloudprovider.InstanceNotFound) {
			// if host doesn't exist, no need to detach
			klog.Warningf("azureDisk - Cannot find node %s, DisksAreAttached will assume disks %v are not attached to it.",
				nodeName, diskNames)
			return attached, nil
		}

		return attached, err
	}

	for _, disk := range disks {
		for _, diskName := range diskNames {
			if disk.Name != nil && diskName != "" && strings.EqualFold(*disk.Name, diskName) {
				attached[diskName] = true
			}
		}
	}

	return attached, nil
}

func (c *controllerCommon) filterNonExistingDisks(ctx context.Context, unfilteredDisks []*armcompute.DataDisk) []*armcompute.DataDisk {
	filteredDisks := []*armcompute.DataDisk{}
	for _, disk := range unfilteredDisks {
		filter := false
		if disk.ManagedDisk != nil && disk.ManagedDisk.ID != nil {
			diskURI := *disk.ManagedDisk.ID
			exist, err := c.checkDiskExists(ctx, diskURI)
			if err != nil {
				klog.Errorf("checkDiskExists(%s) failed with error: %v", diskURI, err)
			} else {
				// only filter disk when checkDiskExists returns <false, nil>
				filter = !exist
				if filter {
					klog.Errorf("disk(%s) does not exist, removed from data disk list", diskURI)
				}
			}
		}

		if !filter {
			filteredDisks = append(filteredDisks, disk)
		}
	}
	return filteredDisks
}

func (c *controllerCommon) checkDiskExists(ctx context.Context, diskURI string) (bool, error) {
	diskName := path.Base(diskURI)
	resourceGroup, subsID, err := getInfoFromDiskURI(diskURI)
	if err != nil {
		return false, err
	}

	diskClient, err := c.clientFactory.GetDiskClientForSub(subsID)
	if err != nil {
		return false, err
	}
	if _, err := diskClient.Get(ctx, resourceGroup, diskName); err != nil {
		var respErr = &azcore.ResponseError{}
		if errors.As(err, &respErr) && respErr.StatusCode == http.StatusNotFound {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func IsOperationPreempted(err error) bool {
	return strings.Contains(err.Error(), consts.OperationPreemptedErrorCode)
}

func getValidCreationData(subscriptionID, resourceGroup string, options *ManagedDiskOptions) (armcompute.CreationData, error) {
	if options.SourceResourceID == "" {
		return armcompute.CreationData{
			CreateOption:    to.Ptr(armcompute.DiskCreateOptionEmpty),
			PerformancePlus: options.PerformancePlus,
		}, nil
	}

	sourceResourceID := options.SourceResourceID
	switch options.SourceType {
	case sourceSnapshot:
		if match := diskSnapshotPathRE.FindString(sourceResourceID); match == "" {
			sourceResourceID = fmt.Sprintf(diskSnapshotPath, subscriptionID, resourceGroup, sourceResourceID)
		}

	case sourceVolume:
		if match := managedDiskPathRE.FindString(sourceResourceID); match == "" {
			sourceResourceID = fmt.Sprintf(managedDiskPath, subscriptionID, resourceGroup, sourceResourceID)
		}
	default:
		return armcompute.CreationData{
			CreateOption:    to.Ptr(armcompute.DiskCreateOptionEmpty),
			PerformancePlus: options.PerformancePlus,
		}, nil
	}

	splits := strings.Split(sourceResourceID, "/")
	if len(splits) > 9 {
		if options.SourceType == sourceSnapshot {
			return armcompute.CreationData{}, fmt.Errorf("sourceResourceID(%s) is invalid, correct format: %s", sourceResourceID, diskSnapshotPathRE)
		}
		return armcompute.CreationData{}, fmt.Errorf("sourceResourceID(%s) is invalid, correct format: %s", sourceResourceID, managedDiskPathRE)
	}
	return armcompute.CreationData{
		CreateOption:     to.Ptr(armcompute.DiskCreateOptionCopy),
		SourceResourceID: &sourceResourceID,
		PerformancePlus:  options.PerformancePlus,
	}, nil
}

func isInstanceNotFoundError(err error) bool {
	errMsg := strings.ToLower(err.Error())
	if strings.Contains(errMsg, strings.ToLower(consts.VmssVMNotActiveErrorMessage)) {
		return true
	}
	return strings.Contains(errMsg, errStatusCode400) && strings.Contains(errMsg, errInvalidParameter) && strings.Contains(errMsg, errTargetInstanceIDs)
}
