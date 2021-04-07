/*
Copyright 2017 The Kubernetes Authors.

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
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2020-12-01/compute"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/protobuf/ptypes"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	cloudprovider "k8s.io/cloud-provider"
	volerr "k8s.io/cloud-provider/volume/errors"
	"k8s.io/klog/v2"

	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	"sigs.k8s.io/cloud-provider-azure/pkg/metrics"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

var (
	// volumeCaps represents how the volume could be accessed.
	volumeCaps = []csi.VolumeCapability_AccessMode{
		{
			Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		},
		{
			Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY,
		},
		{
			Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY,
		},
		{
			Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_SINGLE_WRITER,
		},
		{
			Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
		},
	}
)

const (
	sourceSnapshot           = "snapshot"
	sourceVolume             = "volume"
	azureDiskCSIDriverName   = "azuredisk_csi_driver"
	NotFound                 = "NotFound"
	CreatedForPVNameKey      = "kubernetes.io-created-for-pv-name"
	resizeRequired           = "resizeRequired"
	sourceDiskSearchMaxDepth = 10
)

// listVolumeStatus explains the return status of `listVolumesByResourceGroup`
type listVolumeStatus struct {
	numVisited    int  // the number of iterated azure disks
	isCompleteRun bool // isCompleteRun is flagged true if the function iterated through all azure disks
	entries       []*csi.ListVolumesResponse_Entry
	err           error
}

// CreateVolume provisions an azure disk
func (d *Driver) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	if err := d.ValidateControllerServiceRequest(csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME); err != nil {
		klog.Errorf("invalid create volume req: %v", req)
		return nil, err
	}

	name := req.GetName()
	if len(name) == 0 {
		return nil, status.Error(codes.InvalidArgument, "CreateVolume Name must be provided")
	}
	volCaps := req.GetVolumeCapabilities()
	if len(volCaps) == 0 {
		return nil, status.Error(codes.InvalidArgument, "CreateVolume Volume capabilities must be provided")
	}

	if acquired := d.volumeLocks.TryAcquire(name); !acquired {
		return nil, status.Errorf(codes.Aborted, volumeOperationAlreadyExistsFmt, name)
	}
	defer d.volumeLocks.Release(name)

	capacityBytes := req.GetCapacityRange().GetRequiredBytes()
	volSizeBytes := int64(capacityBytes)
	requestGiB := int(volumehelper.RoundUpGiB(volSizeBytes))
	if requestGiB < minimumDiskSizeGiB {
		requestGiB = minimumDiskSizeGiB
	}

	maxVolSize := int(volumehelper.RoundUpGiB(req.GetCapacityRange().GetLimitBytes()))
	if (maxVolSize > 0) && (maxVolSize < requestGiB) {
		return nil, status.Error(codes.InvalidArgument, "After round-up, volume size exceeds the limit specified")
	}

	var (
		location                string
		storageAccountType      string
		cachingMode             v1.AzureDataDiskCachingMode
		err                     error
		resourceGroup           string
		diskIopsReadWrite       string
		diskMbpsReadWrite       string
		logicalSectorSize       int
		diskName                string
		diskEncryptionSetID     string
		customTags              string
		writeAcceleratorEnabled string
		maxShares               int
	)

	parameters := req.GetParameters()
	if parameters == nil {
		parameters = make(map[string]string)
	}
	for k, v := range parameters {
		switch strings.ToLower(k) {
		case skuNameField:
			storageAccountType = v
		case locationField:
			location = v
		case storageAccountTypeField:
			storageAccountType = v
		case cachingModeField:
			cachingMode = v1.AzureDataDiskCachingMode(v)
		case resourceGroupField:
			resourceGroup = v
		case diskIOPSReadWriteField:
			diskIopsReadWrite = v
		case diskMBPSReadWriteField:
			diskMbpsReadWrite = v
		case logicalSectorSizeField:
			logicalSectorSize, err = strconv.Atoi(v)
			if err != nil {
				return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("parse %s failed with error: %v", v, err))
			}
		case diskNameField:
			diskName = v
		case desIDField:
			diskEncryptionSetID = v
		case tagsField:
			customTags = v
		case azure.WriteAcceleratorEnabled:
			writeAcceleratorEnabled = v
		case maxSharesField:
			maxShares, err = strconv.Atoi(v)
			if err != nil {
				return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("parse %s failed with error: %v", v, err))
			}
			if maxShares < 1 {
				return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("parse %s returned with invalid value: %d", v, maxShares))
			}
		case fsTypeField:
			// no op, only used in NodeStageVolume
		case kindField:
			// no op, only for compatibility
		default:
			return nil, fmt.Errorf("invalid parameter %s in storage class", k)
		}
	}

	if IsAzureStackCloud(d.cloud.Config.Cloud, d.cloud.Config.DisableAzureStackCloud) {
		if maxShares > 1 {
			return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("Invalid maxShares value: %d as Azure Stack does not support shared disk.", maxShares))
		}
	}

	if maxShares < 2 {
		for _, c := range volCaps {
			mode := c.GetAccessMode().Mode
			if mode != csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER &&
				mode != csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY {
				return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("Volume capability(%v) not supported", mode))
			}
		}
	}

	if diskName == "" {
		diskName = name
	}
	diskName = getValidDiskName(diskName)

	if resourceGroup == "" {
		resourceGroup = d.cloud.ResourceGroup
	}

	// normalize values
	skuName, err := normalizeStorageAccountType(storageAccountType, d.cloud.Config.Cloud, d.cloud.Config.DisableAzureStackCloud)
	if err != nil {
		return nil, err
	}

	if _, err = normalizeCachingMode(cachingMode); err != nil {
		return nil, err
	}

	selectedAvailabilityZone := pickAvailabilityZone(req.GetAccessibilityRequirements(), d.cloud.Location)

	if ok, err := d.checkDiskCapacity(ctx, resourceGroup, diskName, requestGiB); !ok {
		return nil, err
	}

	customTagsMap, err := volumehelper.ConvertTagsToMap(customTags)
	if err != nil {
		return nil, err
	}

	mc := metrics.NewMetricContext(azureDiskCSIDriverName, "controller_create_volume", d.cloud.ResourceGroup, d.cloud.SubscriptionID, d.Name)
	isOperationSucceeded := false
	defer func() {
		mc.ObserveOperationWithResult(isOperationSucceeded)
	}()

	klog.V(2).Infof("begin to create azure disk(%s) account type(%s) rg(%s) location(%s) size(%d) selectedAvailabilityZone(%v) maxShares(%d)",
		diskName, skuName, resourceGroup, location, requestGiB, selectedAvailabilityZone, maxShares)

	tags := make(map[string]string)
	contentSource := &csi.VolumeContentSource{}
	for k, v := range customTagsMap {
		tags[k] = v
	}
	tags[CreatedForPVNameKey] = name

	if strings.EqualFold(writeAcceleratorEnabled, "true") {
		tags[azure.WriteAcceleratorEnabled] = "true"
	}
	sourceID := ""
	sourceType := ""
	content := req.GetVolumeContentSource()
	if content != nil {
		if content.GetSnapshot() != nil {
			sourceID = content.GetSnapshot().GetSnapshotId()
			sourceType = sourceSnapshot
			contentSource = &csi.VolumeContentSource{
				Type: &csi.VolumeContentSource_Snapshot{
					Snapshot: &csi.VolumeContentSource_SnapshotSource{
						SnapshotId: sourceID,
					},
				},
			}
		} else {
			sourceID = content.GetVolume().GetVolumeId()
			sourceType = sourceVolume
			contentSource = &csi.VolumeContentSource{
				Type: &csi.VolumeContentSource_Volume{
					Volume: &csi.VolumeContentSource_VolumeSource{
						VolumeId: sourceID,
					},
				},
			}

			ctx, cancel := context.WithCancel(context.Background())
			if sourceGiB, _ := d.GetSourceDiskSize(ctx, resourceGroup, path.Base(sourceID), 0, sourceDiskSearchMaxDepth); sourceGiB != nil && *sourceGiB < int32(requestGiB) {
				parameters[resizeRequired] = strconv.FormatBool(true)
			}
			cancel()
		}
	}

	volumeOptions := &azure.ManagedDiskOptions{
		DiskName:            diskName,
		StorageAccountType:  skuName,
		ResourceGroup:       resourceGroup,
		PVCName:             "",
		SizeGB:              requestGiB,
		Tags:                tags,
		AvailabilityZone:    selectedAvailabilityZone,
		DiskIOPSReadWrite:   diskIopsReadWrite,
		DiskMBpsReadWrite:   diskMbpsReadWrite,
		SourceResourceID:    sourceID,
		SourceType:          sourceType,
		DiskEncryptionSetID: diskEncryptionSetID,
		MaxShares:           int32(maxShares),
		LogicalSectorSize:   int32(logicalSectorSize),
	}
	diskURI, err := d.cloud.CreateManagedDisk(volumeOptions)
	if err != nil {
		if strings.Contains(err.Error(), NotFound) {
			return nil, status.Error(codes.NotFound, err.Error())
		}
		return nil, err
	}

	isOperationSucceeded = true
	klog.V(2).Infof("create azure disk(%s) account type(%s) rg(%s) location(%s) size(%d) tags(%s) successfully", diskName, skuName, resourceGroup, location, requestGiB, tags)

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      diskURI,
			CapacityBytes: capacityBytes,
			VolumeContext: parameters,
			ContentSource: contentSource,
			AccessibleTopology: []*csi.Topology{
				{
					Segments: map[string]string{topologyKey: selectedAvailabilityZone},
				},
			},
		},
	}, nil
}

// DeleteVolume delete an azure disk
func (d *Driver) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}

	if err := d.ValidateControllerServiceRequest(csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME); err != nil {
		return nil, fmt.Errorf("invalid delete volume req: %v", req)
	}
	diskURI := volumeID

	if err := isValidDiskURI(diskURI); err != nil {
		klog.Errorf("validateDiskURI(%s) in DeleteVolume failed with error: %v", diskURI, err)
		return &csi.DeleteVolumeResponse{}, nil
	}

	if acquired := d.volumeLocks.TryAcquire(volumeID); !acquired {
		return nil, status.Errorf(codes.Aborted, volumeOperationAlreadyExistsFmt, volumeID)
	}
	defer d.volumeLocks.Release(volumeID)

	mc := metrics.NewMetricContext(azureDiskCSIDriverName, "controller_delete_volume", d.cloud.ResourceGroup, d.cloud.SubscriptionID, d.Name)
	isOperationSucceeded := false
	defer func() {
		mc.ObserveOperationWithResult(isOperationSucceeded)
	}()

	klog.V(2).Infof("deleting azure disk(%s)", diskURI)
	err := d.cloud.DeleteManagedDisk(diskURI)
	klog.V(2).Infof("delete azure disk(%s) returned with %v", diskURI, err)
	isOperationSucceeded = (err == nil)
	return &csi.DeleteVolumeResponse{}, err
}

// ControllerGetVolume get volume
func (d *Driver) ControllerGetVolume(context.Context, *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

// ControllerPublishVolume attach an azure disk to a required node
func (d *Driver) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	diskURI := req.GetVolumeId()
	if len(diskURI) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID not provided")
	}

	volCap := req.GetVolumeCapability()
	if volCap == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume capability not provided")
	}

	caps := []*csi.VolumeCapability{volCap}
	if !isValidVolumeCapabilities(caps) {
		return nil, status.Error(codes.InvalidArgument, "Volume capability not supported")
	}

	err := d.checkDiskExists(ctx, diskURI)
	if err != nil {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("Volume not found, failed with error: %v", err))
	}

	nodeID := req.GetNodeId()
	if len(nodeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Node ID not provided")
	}

	nodeName := types.NodeName(nodeID)
	diskName, err := GetDiskName(diskURI)
	if err != nil {
		return nil, err
	}

	mc := metrics.NewMetricContext(azureDiskCSIDriverName, "controller_publish_volume", d.cloud.ResourceGroup, d.cloud.SubscriptionID, d.Name)
	isOperationSucceeded := false
	defer func() {
		mc.ObserveOperationWithResult(isOperationSucceeded)
	}()

	klog.V(2).Infof("GetDiskLun returned: %v. Initiating attaching volume %q to node %q.", err, diskURI, nodeName)

	lun, err := d.cloud.GetDiskLun(diskName, diskURI, nodeName)
	if err == cloudprovider.InstanceNotFound {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("failed to get azure instance id for node %q (%v)", nodeName, err))
	}

	if err == nil {
		// Volume is already attached to node.
		klog.V(2).Infof("Attach operation is successful. volume %q is already attached to node %q at lun %d.", diskURI, nodeName, lun)
	} else {
		var cachingMode compute.CachingTypes
		if cachingMode, err = getCachingMode(req.GetVolumeContext()); err != nil {
			return nil, err
		}
		klog.V(2).Infof("Trying to attach volume %q to node %q", diskURI, nodeName)

		lun, err = d.cloud.AttachDisk(true, diskName, diskURI, nodeName, cachingMode)
		if err == nil {
			klog.V(2).Infof("Attach operation successful: volume %q attached to node %q.", diskURI, nodeName)
		} else {
			if derr, ok := err.(*volerr.DanglingAttachError); ok {
				klog.Warningf("volume %q is already attached to node %q, try detach first", diskURI, derr.CurrentNode)
				if err = d.cloud.DetachDisk(diskName, diskURI, derr.CurrentNode); err != nil {
					return nil, status.Errorf(codes.Internal, "Could not detach volume %q from node %q: %v", diskURI, derr.CurrentNode, err)
				}
				klog.V(2).Infof("Trying to attach volume %q to node %q again", diskURI, nodeName)
				lun, err = d.cloud.AttachDisk(true, diskName, diskURI, nodeName, cachingMode)
			}
			if err != nil {
				klog.Errorf("Attach volume %q to instance %q failed with %v", diskURI, nodeName, err)
				return nil, fmt.Errorf("Attach volume %q to instance %q failed with %v", diskURI, nodeName, err)
			}
		}
		klog.V(2).Infof("attach volume %q to node %q successfully", diskURI, nodeName)
	}

	pvInfo := map[string]string{LUN: strconv.Itoa(int(lun))}
	isOperationSucceeded = true
	return &csi.ControllerPublishVolumeResponse{PublishContext: pvInfo}, nil
}

// ControllerUnpublishVolume detach an azure disk from a required node
func (d *Driver) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	diskURI := req.GetVolumeId()
	if len(diskURI) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID not provided")
	}

	nodeID := req.GetNodeId()
	if len(nodeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Node ID not provided")
	}
	nodeName := types.NodeName(nodeID)

	diskName, err := GetDiskName(diskURI)
	if err != nil {
		return nil, err
	}

	mc := metrics.NewMetricContext(azureDiskCSIDriverName, "controller_unpublish_volume", d.cloud.ResourceGroup, d.cloud.SubscriptionID, d.Name)
	isOperationSucceeded := false
	defer func() {
		mc.ObserveOperationWithResult(isOperationSucceeded)
	}()

	klog.V(2).Infof("Trying to detach volume %s from node %s", diskURI, nodeID)

	if err := d.cloud.DetachDisk(diskName, diskURI, nodeName); err != nil {
		if strings.Contains(err.Error(), errDiskNotFound) {
			klog.Warningf("volume %s already detached from node %s", diskURI, nodeID)
		} else {
			return nil, status.Errorf(codes.Internal, "Could not detach volume %q from node %q: %v", diskURI, nodeID, err)
		}
	}
	klog.V(2).Infof("detach volume %s from node %s successfully", diskURI, nodeID)
	isOperationSucceeded = true

	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

// ValidateVolumeCapabilities return the capabilities of the volume
func (d *Driver) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	diskURI := req.GetVolumeId()
	if len(diskURI) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}
	if req.GetVolumeCapabilities() == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume capabilities missing in request")
	}

	err := d.checkDiskExists(ctx, diskURI)
	if err != nil {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("Volume not found, failed with error: %v", err))
	}

	for _, cap := range req.VolumeCapabilities {
		if cap.GetAccessMode().GetMode() != csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER {
			return &csi.ValidateVolumeCapabilitiesResponse{Message: ""}, nil
		}
	}
	return &csi.ValidateVolumeCapabilitiesResponse{Message: ""}, nil
}

// ControllerGetCapabilities returns the capabilities of the Controller plugin
func (d *Driver) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: d.Cap,
	}, nil
}

// GetCapacity returns the capacity of the total available storage pool
func (d *Driver) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

// ListVolumes return all available volumes
func (d *Driver) ListVolumes(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	start := 0
	if req.StartingToken != "" {
		var err error
		start, err = strconv.Atoi(req.StartingToken)
		if err != nil {
			return nil, status.Errorf(codes.Aborted, "ListVolumes starting token(%s) parsing with error: %v", req.StartingToken, err)
		}
		if start < 0 {
			return nil, status.Errorf(codes.Aborted, "ListVolumes starting token(%d) can not be negative", start)
		}
	}
	if d.cloud.KubeClient != nil && d.cloud.KubeClient.CoreV1() != nil && d.cloud.KubeClient.CoreV1().PersistentVolumes() != nil {
		klog.V(6).Infof("List Volumes in Cluster:")
		return d.listVolumesInCluster(ctx, start, int(req.MaxEntries))
	}
	klog.V(6).Infof("List Volumes in Node Resource Group: %s", d.cloud.ResourceGroup)
	return d.listVolumesInNodeResourceGroup(ctx, start, int(req.MaxEntries))
}

// listVolumesInCluster is a helper function for ListVolumes used for when there is an available kubeclient
func (d *Driver) listVolumesInCluster(ctx context.Context, start, maxEntries int) (*csi.ListVolumesResponse, error) {
	pvList, err := d.cloud.KubeClient.CoreV1().PersistentVolumes().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "ListVolumes failed while fetching PersistentVolumes List with error: %v", err.Error())
	}

	// get all resource groups and put them into a sorted slice
	rgMap := make(map[string]bool)
	volSet := make(map[string]bool)
	for _, pv := range pvList.Items {
		if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == DriverName {
			diskURI := pv.Spec.CSI.VolumeHandle
			if err := isValidDiskURI(diskURI); err != nil {
				klog.Warningf("invalid disk uri (%s) with error(%v)", diskURI, err)
				continue
			}
			rg, err := GetResourceGroupFromURI(diskURI)
			if err != nil {
				klog.Warningf("failed to get resource group from disk uri (%s) with error(%v)", diskURI, err)
				continue
			}
			rg, diskURI = strings.ToLower(rg), strings.ToLower(diskURI)
			volSet[diskURI] = true
			if _, visited := rgMap[rg]; visited {
				continue
			}
			rgMap[rg] = true
		}
	}

	resourceGroups := make([]string, len(rgMap))
	i := 0
	for rg := range rgMap {
		resourceGroups[i] = rg
		i++
	}
	sort.Strings(resourceGroups)

	// loop through each resourceGroup to get disk lists
	entries := []*csi.ListVolumesResponse_Entry{}
	numVisited := 0
	isCompleteRun, startFound := true, false
	for _, resourceGroup := range resourceGroups {
		if !isCompleteRun || (maxEntries > 0 && len(entries) >= maxEntries) {
			isCompleteRun = false
			break
		}
		localStart := start - numVisited
		if startFound {
			localStart = 0
		}
		listStatus := d.listVolumesByResourceGroup(ctx, resourceGroup, entries, localStart, maxEntries-len(entries), volSet)
		numVisited += listStatus.numVisited
		if listStatus.err != nil {
			if status.Code(listStatus.err) == codes.FailedPrecondition {
				continue
			}
			return nil, listStatus.err
		}
		startFound = true
		entries = listStatus.entries
		isCompleteRun = isCompleteRun && listStatus.isCompleteRun
	}
	// if start was not found, start token was greater than total number of disks
	if start > 0 && !startFound {
		return nil, status.Errorf(codes.FailedPrecondition, "ListVolumes starting token(%d) is greater than total number of disks", start)
	}

	nextTokenString := ""
	if !isCompleteRun {
		nextTokenString = strconv.Itoa(start + numVisited)
	}

	listVolumesResp := &csi.ListVolumesResponse{
		Entries:   entries,
		NextToken: nextTokenString,
	}

	return listVolumesResp, nil
}

// listVolumesInNodeResourceGroup is a helper function for ListVolumes used for when there is no available kubeclient
func (d *Driver) listVolumesInNodeResourceGroup(ctx context.Context, start, maxEntries int) (*csi.ListVolumesResponse, error) {
	entries := []*csi.ListVolumesResponse_Entry{}
	listStatus := d.listVolumesByResourceGroup(ctx, d.cloud.ResourceGroup, entries, start, maxEntries, nil)
	if listStatus.err != nil {
		return nil, listStatus.err
	}

	nextTokenString := ""
	if !listStatus.isCompleteRun {
		nextTokenString = strconv.Itoa(listStatus.numVisited)
	}

	listVolumesResp := &csi.ListVolumesResponse{
		Entries:   listStatus.entries,
		NextToken: nextTokenString,
	}

	return listVolumesResp, nil
}

// listVolumesByResourceGroup is a helper function that updates the ListVolumeResponse_Entry slice and returns number of total visited volumes, number of volumes that needs to be visited and an error if found
func (d *Driver) listVolumesByResourceGroup(ctx context.Context, resourceGroup string, entries []*csi.ListVolumesResponse_Entry, start, maxEntries int, volSet map[string]bool) listVolumeStatus {
	disks, derr := d.cloud.DisksClient.ListByResourceGroup(ctx, resourceGroup)
	if derr != nil {
		return listVolumeStatus{err: status.Errorf(codes.Internal, "ListVolumes on rg(%s) failed with error: %v", resourceGroup, derr.Error())}
	}
	// if volSet is initialized but is empty, return
	if volSet != nil && len(volSet) == 0 {
		return listVolumeStatus{
			numVisited:    len(disks),
			isCompleteRun: true,
			entries:       entries,
		}
	}
	if start > 0 && start >= len(disks) {
		return listVolumeStatus{
			numVisited: len(disks),
			err:        status.Errorf(codes.FailedPrecondition, "ListVolumes starting token(%d) on rg(%s) is greater than total number of volumes", start, d.cloud.ResourceGroup),
		}
	}
	if start < 0 {
		start = 0
	}
	i := start
	isCompleteRun := true
	// Loop until
	for ; i < len(disks); i++ {
		if maxEntries > 0 && len(entries) >= maxEntries {
			isCompleteRun = false
			break
		}

		disk := disks[i]
		// if given a set of volumes from KubeClient, only continue if the disk can be found in the set
		if volSet != nil && !volSet[strings.ToLower(*disk.ID)] {
			continue
		}
		// HyperVGeneration property is only setup for os disks. Only the non os disks should be included in the list
		if disk.DiskProperties == nil || disk.DiskProperties.HyperVGeneration == "" {
			nodeList := []string{}

			if disk.ManagedBy != nil {
				attachedNode, err := d.cloud.VMSet.GetNodeNameByProviderID(*disk.ManagedBy)
				if err != nil {
					return listVolumeStatus{err: err}
				}
				nodeList = append(nodeList, string(attachedNode))
			}

			entries = append(entries, &csi.ListVolumesResponse_Entry{
				Volume: &csi.Volume{
					VolumeId: *disk.ID,
				},
				Status: &csi.ListVolumesResponse_VolumeStatus{
					PublishedNodeIds: nodeList,
				},
			})
		}
	}
	return listVolumeStatus{
		numVisited:    i - start,
		isCompleteRun: isCompleteRun,
		entries:       entries,
	}
}

// ControllerExpandVolume controller expand volume
func (d *Driver) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	if len(req.GetVolumeId()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}
	if err := d.ValidateControllerServiceRequest(csi.ControllerServiceCapability_RPC_EXPAND_VOLUME); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid expand volume request: %v", req)
	}

	capacityBytes := req.GetCapacityRange().GetRequiredBytes()
	if capacityBytes == 0 {
		return nil, status.Error(codes.InvalidArgument, "volume capacity range missing in request")
	}
	requestSize := *resource.NewQuantity(capacityBytes, resource.BinarySI)

	diskURI := req.GetVolumeId()
	if err := isValidDiskURI(diskURI); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "disk URI(%s) is not valid: %v", diskURI, err)
	}

	diskName, err := GetDiskName(diskURI)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "could not get disk name from diskURI(%s) with error(%v)", diskURI, err)
	}
	resourceGroup, err := GetResourceGroupFromURI(diskURI)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "could not get resource group from diskURI(%s) with error(%v)", diskURI, err)
	}

	mc := metrics.NewMetricContext(azureDiskCSIDriverName, "controller_expand_volume", d.cloud.ResourceGroup, d.cloud.SubscriptionID, d.Name)
	isOperationSucceeded := false
	defer func() {
		mc.ObserveOperationWithResult(isOperationSucceeded)
	}()

	result, rerr := d.cloud.DisksClient.Get(ctx, resourceGroup, diskName)
	if rerr != nil {
		return nil, status.Errorf(codes.Internal, "could not get the disk(%s) under rg(%s) with error(%v)", diskName, resourceGroup, rerr.Error())
	}
	if result.DiskProperties.DiskSizeGB == nil {
		return nil, status.Errorf(codes.Internal, "could not get size of the disk(%s)", diskName)
	}
	oldSize := *resource.NewQuantity(int64(*result.DiskProperties.DiskSizeGB), resource.BinarySI)

	klog.V(2).Infof("begin to expand azure disk(%s) with new size(%d)", diskURI, requestSize.Value())
	newSize, err := d.cloud.ResizeDisk(diskURI, oldSize, requestSize)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to resize disk(%s) with error(%v)", diskURI, err)
	}

	currentSize, ok := newSize.AsInt64()
	if !ok {
		return nil, status.Errorf(codes.Internal, "failed to transform disk size with error(%v)", err)
	}

	isOperationSucceeded = true
	klog.V(2).Infof("expand azure disk(%s) successfully, currentSize(%d)", diskURI, currentSize)

	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         currentSize,
		NodeExpansionRequired: true,
	}, nil
}

// CreateSnapshot create a snapshot
func (d *Driver) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	sourceVolumeID := req.GetSourceVolumeId()
	if len(sourceVolumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "CreateSnapshot Source Volume ID must be provided")
	}
	snapshotName := req.Name
	if len(snapshotName) == 0 {
		return nil, status.Error(codes.InvalidArgument, "snapshot name must be provided")
	}

	snapshotName = getValidDiskName(snapshotName)

	var customTags string
	// set incremental snapshot as true by default
	incremental := true
	var resourceGroup string
	var err error

	parameters := req.GetParameters()
	for k, v := range parameters {
		switch strings.ToLower(k) {
		case tagsField:
			customTags = v
		case incrementalField:
			if v == "false" {
				incremental = false
			}
		case resourceGroupField:
			resourceGroup = v
		default:
			return nil, fmt.Errorf("AzureDisk - invalid option %s in VolumeSnapshotClass", k)
		}
	}

	if IsAzureStackCloud(d.cloud.Config.Cloud, d.cloud.Config.DisableAzureStackCloud) {
		klog.V(2).Info("Use full snapshot instead as Azure Stack does not support incremental snapshot.")
		incremental = false
	}

	if resourceGroup == "" {
		resourceGroup, err = GetResourceGroupFromURI(sourceVolumeID)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "could not get resource group from diskURI(%s) with error(%v)", sourceVolumeID, err)
		}
	}

	customTagsMap, err := volumehelper.ConvertTagsToMap(customTags)
	if err != nil {
		return nil, err
	}
	tags := make(map[string]*string)
	for k, v := range customTagsMap {
		tags[k] = &v
	}

	snapshot := compute.Snapshot{
		SnapshotProperties: &compute.SnapshotProperties{
			CreationData: &compute.CreationData{
				CreateOption: compute.Copy,
				SourceURI:    &sourceVolumeID,
			},
			Incremental: &incremental,
		},
		Location: &d.cloud.Location,
		Tags:     tags,
	}

	mc := metrics.NewMetricContext(azureDiskCSIDriverName, "controller_create_snapshot", d.cloud.ResourceGroup, d.cloud.SubscriptionID, d.Name)
	isOperationSucceeded := false
	defer func() {
		mc.ObserveOperationWithResult(isOperationSucceeded)
	}()

	klog.V(2).Infof("begin to create snapshot(%s, incremental: %v) under rg(%s)", snapshotName, incremental, resourceGroup)
	rerr := d.cloud.SnapshotsClient.CreateOrUpdate(ctx, resourceGroup, snapshotName, snapshot)
	if rerr != nil {
		if strings.Contains(rerr.Error().Error(), "existing disk") {
			return nil, status.Error(codes.AlreadyExists, fmt.Sprintf("request snapshot(%s) under rg(%s) already exists, but the SourceVolumeId is different, error details: %v", snapshotName, resourceGroup, rerr.Error()))
		}

		return nil, status.Error(codes.Internal, fmt.Sprintf("create snapshot error: %v", rerr.Error()))
	}
	klog.V(2).Infof("create snapshot(%s) under rg(%s) successfully", snapshotName, resourceGroup)

	csiSnapshot, err := d.getSnapshotByID(ctx, resourceGroup, snapshotName, sourceVolumeID)
	if err != nil {
		return nil, err
	}

	createResp := &csi.CreateSnapshotResponse{
		Snapshot: csiSnapshot,
	}
	isOperationSucceeded = true
	return createResp, nil
}

// DeleteSnapshot delete a snapshot
func (d *Driver) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	snapshotID := req.SnapshotId
	if len(snapshotID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Snapshot ID must be provided")
	}

	var err error
	snapshotName := snapshotID
	resourceGroup := d.cloud.ResourceGroup

	if isARMResourceID(snapshotID) {
		snapshotName, resourceGroup, err = d.getSnapshotInfo(snapshotID)
		if err != nil {
			return nil, err
		}
	}

	mc := metrics.NewMetricContext(azureDiskCSIDriverName, "controller_delete_snapshot", d.cloud.ResourceGroup, d.cloud.SubscriptionID, d.Name)
	isOperationSucceeded := false
	defer func() {
		mc.ObserveOperationWithResult(isOperationSucceeded)
	}()

	klog.V(2).Infof("begin to delete snapshot(%s) under rg(%s)", snapshotName, resourceGroup)
	rerr := d.cloud.SnapshotsClient.Delete(ctx, resourceGroup, snapshotName)
	if rerr != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("delete snapshot error: %v", rerr.Error()))
	}
	klog.V(2).Infof("delete snapshot(%s) under rg(%s) successfully", snapshotName, resourceGroup)
	isOperationSucceeded = true
	return &csi.DeleteSnapshotResponse{}, nil
}

// ListSnapshots list all snapshots
func (d *Driver) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	// SnapshotId is not empty, return snapshot that match the snapshot id.
	if len(req.GetSnapshotId()) != 0 {
		snapshot, err := d.getSnapshotByID(ctx, d.cloud.ResourceGroup, req.GetSnapshotId(), req.SourceVolumeId)
		if err != nil {
			if strings.Contains(err.Error(), resourceNotFound) {
				return &csi.ListSnapshotsResponse{}, nil
			}
			return nil, err
		}
		entries := []*csi.ListSnapshotsResponse_Entry{
			{
				Snapshot: snapshot,
			},
		}
		listSnapshotResp := &csi.ListSnapshotsResponse{
			Entries: entries,
		}
		return listSnapshotResp, nil
	}

	// no SnapshotId is set, return all snapshots that satisfy the request.
	snapshots, err := d.cloud.SnapshotsClient.ListByResourceGroup(ctx, d.cloud.ResourceGroup)
	if err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("Unknown list snapshot error: %v", err.Error()))
	}

	return getEntriesAndNextToken(req, snapshots)
}

func (d *Driver) getSnapshotByID(ctx context.Context, resourceGroup, snapshotID, sourceVolumeID string) (*csi.Snapshot, error) {
	var err error
	snapshotName := snapshotID
	if isARMResourceID(snapshotID) {
		snapshotName, resourceGroup, err = d.getSnapshotInfo(snapshotID)
		if err != nil {
			return nil, err
		}
	}

	snapshot, rerr := d.cloud.SnapshotsClient.Get(ctx, resourceGroup, snapshotName)
	if rerr != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("get snapshot %s from rg(%s) error: %v", snapshotName, resourceGroup, rerr.Error()))
	}

	return generateCSISnapshot(sourceVolumeID, &snapshot)
}

// GetSourceDiskSize recursively searches for the sourceDisk and returns: sourceDisk disk size, error
func (d *Driver) GetSourceDiskSize(ctx context.Context, resourceGroup, diskName string, curDepth, maxDepth int) (*int32, error) {
	if curDepth > maxDepth {
		return nil, status.Error(codes.Internal, fmt.Sprintf("current depth (%d) surpassed the max depth (%d) while searching for the source disk size", curDepth, maxDepth))
	}
	result, rerr := d.cloud.DisksClient.Get(ctx, resourceGroup, diskName)
	if rerr != nil {
		return nil, rerr.Error()
	}
	if result.DiskProperties == nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("DiskProperty not found for disk (%s) in resource group (%s)", diskName, resourceGroup))
	}

	if result.DiskProperties.CreationData != nil && (*result.DiskProperties.CreationData).CreateOption == "Copy" {
		klog.V(2).Infof("Clone source disk has a parent source")
		sourceResourceID := *result.DiskProperties.CreationData.SourceResourceID
		parentResourceGroup, _ := GetResourceGroupFromURI(sourceResourceID)
		parentDiskName := path.Base(sourceResourceID)
		return d.GetSourceDiskSize(ctx, parentResourceGroup, parentDiskName, curDepth+1, maxDepth)
	}

	if (*result.DiskProperties).DiskSizeGB == nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("DiskSizeGB for disk (%s) in resourcegroup (%s) is nil", diskName, resourceGroup))
	}
	return (*result.DiskProperties).DiskSizeGB, nil
}

func isValidVolumeCapabilities(volCaps []*csi.VolumeCapability) bool {
	hasSupport := func(cap *csi.VolumeCapability) bool {
		for _, c := range volumeCaps {
			// todo: Block volume support
			/* compile error here
			if blk := c.GetBlock(); blk != nil {
				return false
			}
			*/
			if c.GetMode() == cap.AccessMode.GetMode() {
				return true
			}
		}
		return false
	}

	foundAll := true
	for _, c := range volCaps {
		if !hasSupport(c) {
			foundAll = false
		}
	}
	return foundAll
}

// pickAvailabilityZone selects 1 zone given topology requirement.
// if not found or topology requirement is not zone format, empty string is returned.
func pickAvailabilityZone(requirement *csi.TopologyRequirement, region string) string {
	if requirement == nil {
		return ""
	}
	for _, topology := range requirement.GetPreferred() {
		if zone, exists := topology.GetSegments()[topologyKey]; exists {
			if isAvailabilityZone(zone, region) {
				return zone
			}
		}
	}
	for _, topology := range requirement.GetRequisite() {
		if zone, exists := topology.GetSegments()[topologyKey]; exists {
			if isAvailabilityZone(zone, region) {
				return zone
			}
		}
	}
	return ""
}

func getCachingMode(attributes map[string]string) (compute.CachingTypes, error) {
	var (
		cachingMode v1.AzureDataDiskCachingMode
		err         error
	)

	for k, v := range attributes {
		switch strings.ToLower(k) {
		case cachingModeField:
			cachingMode = v1.AzureDataDiskCachingMode(v)
		}
	}

	cachingMode, err = normalizeCachingMode(cachingMode)
	return compute.CachingTypes(cachingMode), err
}

func generateCSISnapshot(sourceVolumeID string, snapshot *compute.Snapshot) (*csi.Snapshot, error) {
	if snapshot == nil || snapshot.SnapshotProperties == nil {
		return nil, fmt.Errorf("snapshot property is nil")
	}

	tp, err := ptypes.TimestampProto(snapshot.SnapshotProperties.TimeCreated.ToTime())
	if err != nil {
		return nil, fmt.Errorf("Failed to covert creation timestamp: %v", err)
	}
	ready, _ := isCSISnapshotReady(*snapshot.SnapshotProperties.ProvisioningState)

	if snapshot.SnapshotProperties.DiskSizeGB == nil {
		return nil, fmt.Errorf("diskSizeGB of snapshot property is nil")
	}

	if sourceVolumeID == "" {
		sourceVolumeID = getSourceVolumeID(snapshot)
	}

	return &csi.Snapshot{
		SizeBytes:      volumehelper.GiBToBytes(int64(*snapshot.SnapshotProperties.DiskSizeGB)),
		SnapshotId:     *snapshot.ID,
		SourceVolumeId: sourceVolumeID,
		CreationTime:   tp,
		ReadyToUse:     ready,
	}, nil
}

func isCSISnapshotReady(state string) (bool, error) {
	switch strings.ToLower(state) {
	case "succeeded":
		return true, nil
	default:
		return false, nil
	}
}

// The format of snapshot id is /subscriptions/xxx/resourceGroups/xxx/providers/Microsoft.Compute/snapshots/snapshot-xxx-xxx.
func (d *Driver) getSnapshotInfo(snapshotID string) (snapshotName, resourceGroup string, err error) {
	if snapshotName, err = getSnapshotName(snapshotID); err != nil {
		return "", "", err
	}
	if resourceGroup, err = GetResourceGroupFromURI(snapshotID); err != nil {
		return "", "", err
	}
	return snapshotName, resourceGroup, err
}

// There are 4 scenarios for listing snapshots.
// 1. StartingToken is null, and MaxEntries is null. Return all snapshots from zero.
// 2. StartingToken is null, and MaxEntries is not null. Return `MaxEntries` snapshots from zero.
// 3. StartingToken is not null, and MaxEntries is null. Return all snapshots from `StartingToken`.
// 4. StartingToken is not null, and MaxEntries is not null. Return `MaxEntries` snapshots from `StartingToken`.
func getEntriesAndNextToken(req *csi.ListSnapshotsRequest, snapshots []compute.Snapshot) (*csi.ListSnapshotsResponse, error) {
	if req == nil {
		return nil, status.Errorf(codes.Aborted, "request is nil")
	}

	var err error
	start := 0
	if req.StartingToken != "" {
		start, err = strconv.Atoi(req.StartingToken)
		if err != nil {
			return nil, status.Errorf(codes.Aborted, "ListSnapshots starting token(%s) parsing with error: %v", req.StartingToken, err)

		}
		if start >= len(snapshots) {
			return nil, status.Errorf(codes.Aborted, "ListSnapshots starting token(%d) is greater than total number of snapshots", start)
		}
		if start < 0 {
			return nil, status.Errorf(codes.Aborted, "ListSnapshots starting token(%d) can not be negative", start)
		}
	}

	maxEntries := len(snapshots) - start
	if req.MaxEntries > 0 && int(req.MaxEntries) < maxEntries {
		maxEntries = int(req.MaxEntries)
	}
	entries := []*csi.ListSnapshotsResponse_Entry{}
	for count := 0; start < len(snapshots) && count < maxEntries; start++ {
		if (req.SourceVolumeId != "" && req.SourceVolumeId == getSourceVolumeID(&snapshots[start])) || req.SourceVolumeId == "" {
			csiSnapshot, err := generateCSISnapshot(req.SourceVolumeId, &snapshots[start])
			if err != nil {
				return nil, fmt.Errorf("failed to generate snapshot entry: %v", err)
			}
			entries = append(entries, &csi.ListSnapshotsResponse_Entry{Snapshot: csiSnapshot})
			count++
		}
	}

	nextToken := len(snapshots)
	if start < len(snapshots) {
		nextToken = start
	}

	listSnapshotResp := &csi.ListSnapshotsResponse{
		Entries:   entries,
		NextToken: strconv.Itoa(nextToken),
	}

	return listSnapshotResp, nil
}
