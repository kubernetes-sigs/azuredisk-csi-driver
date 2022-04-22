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
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2021-07-01/compute"

	"k8s.io/apimachinery/pkg/types"
	kwait "k8s.io/apimachinery/pkg/util/wait"
	cloudprovider "k8s.io/cloud-provider"
	volerr "k8s.io/cloud-provider/volume/errors"
	"k8s.io/klog/v2"

	"sigs.k8s.io/cloud-provider-azure/pkg/batch"
	azcache "sigs.k8s.io/cloud-provider-azure/pkg/cache"
	"sigs.k8s.io/cloud-provider-azure/pkg/consts"
	"sigs.k8s.io/cloud-provider-azure/pkg/metrics"
)

const (
	// Disk Caching is not supported for disks 4 TiB and larger
	// https://docs.microsoft.com/en-us/azure/virtual-machines/premium-storage-performance#disk-caching
	diskCachingLimit = 4096 // GiB

	maxLUN                 = 64 // max number of LUNs per VM
	errStatusCode400       = "statuscode=400"
	errInvalidParameter    = `code="invalidparameter"`
	errTargetInstanceIds   = `target="instanceids"`
	sourceSnapshot         = "snapshot"
	sourceVolume           = "volume"
	attachDiskMapKeySuffix = "attachdiskmap"
	detachDiskMapKeySuffix = "detachdiskmap"

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
	subscriptionID        string
	location              string
	extendedLocation      *ExtendedLocation
	storageEndpointSuffix string
	resourceGroup         string
	diskStateMap          sync.Map // <diskURI, attaching/detaching state>
	lockMap               *lockMap
	cloud                 *Cloud
	attachDiskProcessor   *batch.Processor
	detachDiskProcessor   *batch.Processor
}

// AttachDiskOptions attach disk options
type AttachDiskOptions struct {
	cachingMode             compute.CachingTypes
	diskName                string
	diskEncryptionSetID     string
	writeAcceleratorEnabled bool
	lun                     int32
}

func (a *AttachDiskOptions) String() string {
	return fmt.Sprintf("AttachDiskOptions{diskName: %q, lun: %d}", a.diskName, a.lun)
}

// ExtendedLocation contains additional info about the location of resources.
type ExtendedLocation struct {
	// Name - The name of the extended location.
	Name string `json:"name,omitempty"`
	// Type - The type of the extended location.
	Type string `json:"type,omitempty"`
}

// getNodeVMSet gets the VMSet interface based on config.VMType and the real virtual machine type.
func (c *controllerCommon) getNodeVMSet(nodeName types.NodeName, crt azcache.AzureCacheReadType) (VMSet, error) {
	// 1. vmType is standard, return cloud.VMSet directly.
	if c.cloud.VMType == consts.VMTypeStandard {
		return c.cloud.VMSet, nil
	}

	// 2. vmType is Virtual Machine Scale Set (vmss), convert vmSet to ScaleSet.
	ss, ok := c.cloud.VMSet.(*ScaleSet)
	if !ok {
		return nil, fmt.Errorf("error of converting vmSet (%q) to ScaleSet with vmType %q", c.cloud.VMSet, c.cloud.VMType)
	}

	// 3. If the node is managed by availability set, then return ss.availabilitySet.
	managedByAS, err := ss.isNodeManagedByAvailabilitySet(mapNodeNameToVMName(nodeName), crt)
	if err != nil {
		return nil, err
	}
	if managedByAS {
		// vm is managed by availability set.
		return ss.availabilitySet, nil
	}

	// 4. Node is managed by vmss
	return ss, nil
}

// AttachDisk attaches a disk to vm
// return (lun, error)
func (c *controllerCommon) AttachDisk(ctx context.Context, async bool, diskName, diskURI string, nodeName types.NodeName,
	cachingMode compute.CachingTypes, disk *compute.Disk) (int32, error) {
	diskEncryptionSetID := ""
	writeAcceleratorEnabled := false

	// there is possibility that disk is nil when GetDisk is throttled
	// don't check disk state when GetDisk is throttled
	if disk != nil {
		if disk.ManagedBy != nil && (disk.MaxShares == nil || *disk.MaxShares <= 1) {
			vmset, err := c.getNodeVMSet(nodeName, azcache.CacheReadTypeUnsafe)
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

		if disk.DiskProperties != nil {
			if disk.DiskProperties.DiskSizeGB != nil && *disk.DiskProperties.DiskSizeGB >= diskCachingLimit && cachingMode != compute.CachingTypesNone {
				// Disk Caching is not supported for disks 4 TiB and larger
				// https://docs.microsoft.com/en-us/azure/virtual-machines/premium-storage-performance#disk-caching
				cachingMode = compute.CachingTypesNone
				klog.Warningf("size of disk(%s) is %dGB which is bigger than limit(%dGB), set cacheMode as None",
					diskURI, *disk.DiskProperties.DiskSizeGB, diskCachingLimit)
			}

			if disk.DiskProperties.Encryption != nil &&
				disk.DiskProperties.Encryption.DiskEncryptionSetID != nil {
				diskEncryptionSetID = *disk.DiskProperties.Encryption.DiskEncryptionSetID
			}

			if disk.DiskProperties.DiskState != compute.DiskStateUnattached && (disk.MaxShares == nil || *disk.MaxShares <= 1) {
				return -1, fmt.Errorf("state of disk(%s) is %s, not in expected %s state", diskURI, disk.DiskProperties.DiskState, compute.DiskStateUnattached)
			}
		}

		if v, ok := disk.Tags[WriteAcceleratorEnabled]; ok {
			if v != nil && strings.EqualFold(*v, "true") {
				writeAcceleratorEnabled = true
			}
		}
	}

	options := &AttachDiskOptions{
		lun:                     -1,
		diskName:                diskName,
		cachingMode:             cachingMode,
		diskEncryptionSetID:     diskEncryptionSetID,
		writeAcceleratorEnabled: writeAcceleratorEnabled,
	}

	diskToAttach := attachDiskParams{
		diskURI: diskURI,
		options: options,
		async:   async,
	}

	resourceGroup, err := c.cloud.GetNodeResourceGroup(string(nodeName))
	if err != nil {
		resourceGroup = c.resourceGroup
	}

	batchKey := metrics.KeyFromAttributes(c.subscriptionID, strings.ToLower(resourceGroup), strings.ToLower(string(nodeName)))
	r, err := c.attachDiskProcessor.Do(ctx, batchKey, diskToAttach)
	if err == nil {
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case result := <-r.(chan (attachDiskResult)):
			if err = result.err; err == nil {
				return result.lun, nil
			}
		}
	}

	klog.Errorf("azureDisk - attach disk(%s, %s) failed, err: %v", diskName, diskURI, err)
	return -1, err
}

type attachDiskParams struct {
	diskURI string
	options *AttachDiskOptions
	async   bool
}

type attachDiskResult struct {
	lun int32
	err error
}

func (c *controllerCommon) attachDiskBatchToNode(ctx context.Context, subscriptionID, resourceGroup string, nodeName types.NodeName, disksToAttach []attachDiskParams) ([]chan (attachDiskResult), error) {
	diskMap := make(map[string]*AttachDiskOptions, len(disksToAttach))
	lunChans := make([]chan (attachDiskResult), len(disksToAttach))
	async := false

	for i, disk := range disksToAttach {
		lunChans[i] = make(chan (attachDiskResult), 1)

		diskMap[disk.diskURI] = disk.options

		diskURI := strings.ToLower(disk.diskURI)
		c.diskStateMap.Store(diskURI, "attaching")
		defer c.diskStateMap.Delete(diskURI)

		async = async || disk.async
	}

	err := c.cloud.SetDiskLun(nodeName, diskMap)
	if err != nil {
		return nil, err
	}

	vmset, err := c.getNodeVMSet(nodeName, azcache.CacheReadTypeUnsafe)
	if err != nil {
		return nil, err
	}

	c.lockMap.LockEntry(string(nodeName))
	defer c.lockMap.UnlockEntry(string(nodeName))

	klog.V(2).Infof("azuredisk - trying to attach disks to node %s: %s", nodeName, diskMap)

	future, err := vmset.AttachDisk(ctx, nodeName, diskMap)
	if err != nil {
		return nil, err
	}

	attachFn := func() {
		resultCtx := ctx

		if async {
			// The context, ctx, passed to attachDiskBatchToNode is owned by batch.Processor which will
			// cancel it when we return. Since we're asynchronously waiting for the attach disk result,
			// we must create an independent context passed to WaitForUpdateResult with the deadline
			// provided in ctx. This avoids an earlier return due to ctx being canceled while still
			// respecting the deadline for the overall attach operation.
			resultCtx = context.Background()
			if deadline, ok := ctx.Deadline(); ok {
				var cancel func()
				resultCtx, cancel = context.WithDeadline(resultCtx, deadline)
				defer cancel()
			}
		}

		err = vmset.WaitForUpdateResult(resultCtx, future, resourceGroup, "attach_disk")
		if err == nil {
			klog.V(2).Infof("azuredisk - successfully attached disks to node %s: %s", nodeName, diskMap)
		}

		for i, disk := range disksToAttach {
			lunChans[i] <- attachDiskResult{lun: diskMap[disk.diskURI].lun, err: err}
		}
	}

	if async {
		go attachFn()
	} else {
		attachFn()
	}

	return lunChans, nil
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

	resourceGroup, err := c.cloud.GetNodeResourceGroup(string(nodeName))
	if err != nil {
		resourceGroup = c.resourceGroup
	}

	diskToDetach := detachDiskParams{
		diskName: diskName,
		diskURI:  diskURI,
	}

	batchKey := metrics.KeyFromAttributes(c.subscriptionID, strings.ToLower(resourceGroup), strings.ToLower(string(nodeName)))
	if _, err := c.detachDiskProcessor.Do(ctx, batchKey, diskToDetach); err != nil {
		klog.Errorf("azureDisk - detach disk(%s, %s) failed, err: %v", diskName, diskURI, err)
		return err
	}

	klog.V(2).Infof("azureDisk - detach disk(%s, %s) succeeded", diskName, diskURI)
	return nil
}

type detachDiskParams struct {
	diskName string
	diskURI  string
}

func (c *controllerCommon) detachDiskBatchFromNode(ctx context.Context, subscriptionID, resourceGroup string, nodeName types.NodeName, disksToDetach []detachDiskParams) error {
	diskMap := make(map[string]string, len(disksToDetach))
	for _, disk := range disksToDetach {
		diskMap[disk.diskURI] = disk.diskName

		diskURI := strings.ToLower(disk.diskURI)
		c.diskStateMap.Store(diskURI, "detaching")
		defer c.diskStateMap.Delete(diskURI)
	}

	vmset, err := c.getNodeVMSet(nodeName, azcache.CacheReadTypeUnsafe)
	if err != nil {
		return err
	}

	c.lockMap.LockEntry(string(nodeName))
	defer c.lockMap.UnlockEntry(string(nodeName))

	klog.V(2).Infof("azuredisk - trying to detach disks from node %s: %s", nodeName, diskMap)

	err = vmset.DetachDisk(ctx, nodeName, diskMap)
	if err != nil {
		if isInstanceNotFoundError(err) {
			// if host doesn't exist, no need to detach
			klog.Warningf("azureDisk - got InstanceNotFoundError(%v), assuming disks are already detached: %v", err, diskMap)
			return nil
		}
		return err
	}

	klog.V(2).Infof("azuredisk - successfully detached disks from node %s: %s", nodeName, diskMap)

	return nil
}

// UpdateVM updates a vm
func (c *controllerCommon) UpdateVM(ctx context.Context, nodeName types.NodeName) error {
	vmset, err := c.getNodeVMSet(nodeName, azcache.CacheReadTypeUnsafe)
	if err != nil {
		return err
	}
	node := strings.ToLower(string(nodeName))
	c.lockMap.LockEntry(node)
	defer c.lockMap.UnlockEntry(node)
	return vmset.UpdateVM(ctx, nodeName)
}

// getNodeDataDisks invokes vmSet interfaces to get data disks for the node.
func (c *controllerCommon) getNodeDataDisks(nodeName types.NodeName, crt azcache.AzureCacheReadType) ([]compute.DataDisk, *string, error) {
	vmset, err := c.getNodeVMSet(nodeName, crt)
	if err != nil {
		return nil, nil, err
	}

	return vmset.GetDataDisks(nodeName, crt)
}

// GetDiskLun finds the lun on the host that the vhd is attached to, given a vhd's diskName and diskURI.
func (c *controllerCommon) GetDiskLun(diskName, diskURI string, nodeName types.NodeName) (int32, *string, error) {
	// getNodeDataDisks need to fetch the cached data/fresh data if cache expired here
	// to ensure we get LUN based on latest entry.
	disks, provisioningState, err := c.getNodeDataDisks(nodeName, azcache.CacheReadTypeDefault)
	if err != nil {
		klog.Errorf("error of getting data disks for node %s: %v", nodeName, err)
		return -1, provisioningState, err
	}

	for _, disk := range disks {
		if disk.Lun != nil && (disk.Name != nil && diskName != "" && strings.EqualFold(*disk.Name, diskName)) ||
			(disk.Vhd != nil && disk.Vhd.URI != nil && diskURI != "" && strings.EqualFold(*disk.Vhd.URI, diskURI)) ||
			(disk.ManagedDisk != nil && strings.EqualFold(*disk.ManagedDisk.ID, diskURI)) {
			if disk.ToBeDetached != nil && *disk.ToBeDetached {
				klog.Warningf("azureDisk - find disk(ToBeDetached): lun %d name %s uri %s", *disk.Lun, diskName, diskURI)
			} else {
				// found the disk
				klog.V(2).Infof("azureDisk - find disk: lun %d name %s uri %s", *disk.Lun, diskName, diskURI)
				return *disk.Lun, provisioningState, nil
			}
		}
	}
	return -1, provisioningState, fmt.Errorf("%s for disk %s", consts.CannotFindDiskLUN, diskName)
}

// SetDiskLun find unused luns and allocate lun for every disk in disksPendingAttach map.
// Return err if not enough luns are found.
func (c *controllerCommon) SetDiskLun(nodeName types.NodeName, disksPendingAttach map[string]*AttachDiskOptions) error {
	disks, _, err := c.getNodeDataDisks(nodeName, azcache.CacheReadTypeDefault)
	if err != nil {
		klog.Errorf("error of getting data disks for node %s: %v", nodeName, err)
		return err
	}

	allLuns := make([]bool, maxLUN)
	uriToLun := make(map[string]int32, len(disks))
	for _, disk := range disks {
		if disk.Lun != nil && *disk.Lun >= 0 && *disk.Lun < maxLUN {
			allLuns[*disk.Lun] = true
			if disk.ManagedDisk != nil {
				uriToLun[*disk.ManagedDisk.ID] = *disk.Lun
			}
		}
	}
	if len(disksPendingAttach) == 0 {
		// attach disk request is empty, return directly
		return nil
	}

	// allocate lun for every disk in disksPendingAttach
	var availableDiskLuns []int32
	freeLunsCount := 0
	for lun, inUse := range allLuns {
		if !inUse {
			availableDiskLuns = append(availableDiskLuns, int32(lun))
			freeLunsCount++
			// found enough luns for to assign to all pending disks
			if freeLunsCount >= len(disksPendingAttach) {
				break
			}
		}
	}

	if len(availableDiskLuns) < len(disksPendingAttach) {
		return fmt.Errorf("could not find enough disk luns(current: %d) for disksPendingAttach(%v, len=%d)",
			len(availableDiskLuns), disksPendingAttach, len(disksPendingAttach))
	}

	count := 0
	for uri, opt := range disksPendingAttach {
		if opt == nil {
			return fmt.Errorf("unexpected nil pointer in disksPendingAttach(%v)", disksPendingAttach)
		}
		// disk already exists and has assigned lun
		lun, exists := uriToLun[uri]
		if exists {
			opt.lun = lun
		}
		opt.lun = availableDiskLuns[count]
		count++
	}

	if count <= 0 {
		return fmt.Errorf("could not find lun of, disksPendingAttach(%v)", disksPendingAttach)
	}
	return nil
}

// DisksAreAttached checks if a list of volumes are attached to the node with the specified NodeName.
func (c *controllerCommon) DisksAreAttached(diskNames []string, nodeName types.NodeName) (map[string]bool, error) {
	attached := make(map[string]bool)
	for _, diskName := range diskNames {
		attached[diskName] = false
	}

	// doing stalled read for getNodeDataDisks to ensure we don't call ARM
	// for every reconcile call. The cache is invalidated after Attach/Detach
	// disk. So the new entry will be fetched and cached the first time reconcile
	// loop runs after the Attach/Disk OP which will reflect the latest model.
	disks, _, err := c.getNodeDataDisks(nodeName, azcache.CacheReadTypeUnsafe)
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

func filterDetachingDisks(unfilteredDisks []compute.DataDisk) []compute.DataDisk {
	filteredDisks := []compute.DataDisk{}
	for _, disk := range unfilteredDisks {
		if disk.ToBeDetached != nil && *disk.ToBeDetached {
			if disk.Name != nil {
				klog.V(2).Infof("Filtering disk: %s with ToBeDetached flag set.", *disk.Name)
			}
		} else {
			filteredDisks = append(filteredDisks, disk)
		}
	}
	return filteredDisks
}

func (c *controllerCommon) filterNonExistingDisks(ctx context.Context, unfilteredDisks []compute.DataDisk) []compute.DataDisk {
	filteredDisks := []compute.DataDisk{}
	for _, disk := range unfilteredDisks {
		filter := false
		if disk.ManagedDisk != nil && disk.ManagedDisk.ID != nil {
			diskURI := *disk.ManagedDisk.ID
			exist, err := c.cloud.checkDiskExists(ctx, diskURI)
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

	if _, rerr := c.cloud.DisksClient.Get(ctx, subsID, resourceGroup, diskName); rerr != nil {
		if rerr.HTTPStatusCode == http.StatusNotFound {
			return false, nil
		}
		return false, rerr.Error()
	}

	return true, nil
}

func getValidCreationData(subscriptionID, resourceGroup, sourceResourceID, sourceType string) (compute.CreationData, error) {
	if sourceResourceID == "" {
		return compute.CreationData{
			CreateOption: compute.DiskCreateOptionEmpty,
		}, nil
	}

	switch sourceType {
	case sourceSnapshot:
		if match := diskSnapshotPathRE.FindString(sourceResourceID); match == "" {
			sourceResourceID = fmt.Sprintf(diskSnapshotPath, subscriptionID, resourceGroup, sourceResourceID)
		}

	case sourceVolume:
		if match := managedDiskPathRE.FindString(sourceResourceID); match == "" {
			sourceResourceID = fmt.Sprintf(managedDiskPath, subscriptionID, resourceGroup, sourceResourceID)
		}
	default:
		return compute.CreationData{
			CreateOption: compute.DiskCreateOptionEmpty,
		}, nil
	}

	splits := strings.Split(sourceResourceID, "/")
	if len(splits) > 9 {
		if sourceType == sourceSnapshot {
			return compute.CreationData{}, fmt.Errorf("sourceResourceID(%s) is invalid, correct format: %s", sourceResourceID, diskSnapshotPathRE)
		}
		return compute.CreationData{}, fmt.Errorf("sourceResourceID(%s) is invalid, correct format: %s", sourceResourceID, managedDiskPathRE)
	}
	return compute.CreationData{
		CreateOption:     compute.DiskCreateOptionCopy,
		SourceResourceID: &sourceResourceID,
	}, nil
}

func isInstanceNotFoundError(err error) bool {
	errMsg := strings.ToLower(err.Error())
	if strings.Contains(errMsg, strings.ToLower(consts.VmssVMNotActiveErrorMessage)) {
		return true
	}
	return strings.Contains(errMsg, errStatusCode400) && strings.Contains(errMsg, errInvalidParameter) && strings.Contains(errMsg, errTargetInstanceIds)
}
