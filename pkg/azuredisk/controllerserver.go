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
	"errors"
	"fmt"
	"strconv"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/kubernetes/pkg/cloudprovider/providers/azure"
	"k8s.io/kubernetes/pkg/util/keymutex"
	"k8s.io/kubernetes/pkg/volume/util"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2018-10-01/compute"
	"github.com/Azure/azure-sdk-for-go/services/storage/mgmt/2018-07-01/storage"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/pborman/uuid"
	"k8s.io/klog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	// volumeCaps represents how the volume could be accessed.
	// It is SINGLE_NODE_WRITER since azure disk could only be attached to a single node at any given time.
	volumeCaps = []csi.VolumeCapability_AccessMode{
		{
			Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		},
	}

	getLunMutex = keymutex.NewHashed(0)
)

// CreateVolume provisions an azure disk
func (d *Driver) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	if err := d.ValidateControllerServiceRequest(csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME); err != nil {
		klog.Errorf("invalid create volume req: %v", req)
		return nil, err
	}

	volumeCapabilities := req.GetVolumeCapabilities()
	name := req.GetName()
	if len(name) == 0 {
		return nil, status.Error(codes.InvalidArgument, "CreateVolume Name must be provided")
	}
	if len(volumeCapabilities) == 0 {
		return nil, status.Error(codes.InvalidArgument, "CreateVolume Volume capabilities must be provided")
	}

	if !isValidVolumeCapabilities(volumeCapabilities) {
		return nil, status.Error(codes.InvalidArgument, "Volume capabilities not supported")
	}

	volSizeBytes := int64(util.GIB)
	if req.GetCapacityRange() != nil {
		volSizeBytes = req.GetCapacityRange().GetRequiredBytes()
	}

	requestGiB := int(util.RoundUpSize(volSizeBytes, util.GIB))

	maxVolSize := int(req.GetCapacityRange().GetLimitBytes())
	if (maxVolSize > 0) && (maxVolSize < requestGiB) {
		return nil, status.Error(codes.InvalidArgument, "After round-up, volume size exceeds the limit specified")
	}

	var (
		location, account  string
		storageAccountType string
		cachingMode        v1.AzureDataDiskCachingMode
		strKind            string
		err                error
		resourceGroup      string
		diskIopsReadWrite  string
		diskMbpsReadWrite  string
	)

	parameters := req.GetParameters()
	for k, v := range parameters {
		switch strings.ToLower(k) {
		case "skuname":
			storageAccountType = v
		case "location":
			location = v
		case "storageaccount":
			account = v
		case "storageaccounttype":
			storageAccountType = v
		case "kind":
			strKind = v
		case "cachingmode":
			cachingMode = v1.AzureDataDiskCachingMode(v)
		case "resourcegroup":
			resourceGroup = v
			/* new zone implementation in csi, these parameters are not needed
			case "zone":
				zonePresent = true
				availabilityZone = v
			case "zones":
				zonesPresent = true
				availabilityZones, err = util.ZonesToSet(v)
				if err != nil {
					return nil, fmt.Errorf("error parsing zones %s, must be strings separated by commas: %v", v, err)
				}
			case "zoned":
				strZoned = v
			*/
		case "diskiopsreadwrite":
			diskIopsReadWrite = v
		case "diskmbpsreadwrite":
			diskMbpsReadWrite = v
		default:
			//don't return error here since there are some parameters(e.g. fsType) used in disk mount process
			//return nil, fmt.Errorf("AzureDisk - invalid option %s in storage class", k)
		}
	}

	// maxLength = 79 - (4 for ".vhd") = 75
	// todo: get cluster name
	diskName := util.GenerateVolumeName("pvc-disk", uuid.NewUUID().String(), 75)

	// normalize values
	skuName, err := normalizeStorageAccountType(storageAccountType)
	if err != nil {
		return nil, err
	}

	if _, err = normalizeCachingMode(cachingMode); err != nil {
		return nil, err
	}

	kind, err := normalizeKind(strFirstLetterToUpper(strKind))
	if err != nil {
		return nil, err
	}

	if kind != v1.AzureManagedDisk {
		if resourceGroup != "" {
			return nil, errors.New("StorageClass option 'resourceGroup' can be used only for managed disks")
		}
	}

	selectedAvailabilityZone := pickAvailabilityZone(req.GetAccessibilityRequirements())

	klog.V(2).Infof("begin to create azure disk(%s) account type(%s) rg(%s) location(%s) size(%d)", diskName, skuName, resourceGroup, location, requestGiB)

	diskURI := ""
	if kind == v1.AzureManagedDisk {
		tags := make(map[string]string)
		/* todo: check where are the tags in CSI
		if p.options.CloudTags != nil {
			tags = *(p.options.CloudTags)
		}
		*/

		volumeOptions := &azure.ManagedDiskOptions{
			DiskName:           diskName,
			StorageAccountType: skuName,
			ResourceGroup:      resourceGroup,
			PVCName:            "",
			SizeGB:             requestGiB,
			Tags:               tags,
			AvailabilityZone:   selectedAvailabilityZone,
			DiskIOPSReadWrite:  diskIopsReadWrite,
			DiskMBpsReadWrite:  diskMbpsReadWrite,
		}
		diskURI, err = d.cloud.CreateManagedDisk(volumeOptions)
		if err != nil {
			return nil, err
		}
	} else {
		if kind == v1.AzureDedicatedBlobDisk {
			_, diskURI, _, err = d.cloud.CreateVolume(name, account, storageAccountType, location, requestGiB)
			if err != nil {
				return nil, err
			}
		} else {
			diskURI, err = d.cloud.CreateBlobDisk(name, storage.SkuName(storageAccountType), requestGiB)
			if err != nil {
				return nil, err
			}
		}
	}

	klog.V(2).Infof("create azure disk(%s) account type(%s) rg(%s) location(%s) size(%d) successfully", diskName, skuName, resourceGroup, location, requestGiB)

	/*  todo: block volume support
	if utilfeature.DefaultFeatureGate.Enabled(features.BlockVolume) {
		volumeMode = p.options.PVC.Spec.VolumeMode
		if volumeMode != nil && *volumeMode == v1.PersistentVolumeBlock {
			// Block volumes should not have any FSType
			fsType = ""
		}
	}
	*/

	/* todo: snapshot support
	if req.GetVolumeContentSource() != nil {
		contentSource := req.GetVolumeContentSource()
		if contentSource.GetSnapshot() != nil {
		}
	}
	*/

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      diskURI,
			CapacityBytes: req.GetCapacityRange().GetRequiredBytes(),
			VolumeContext: parameters,
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
	if len(req.GetVolumeId()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}

	if err := d.ValidateControllerServiceRequest(csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME); err != nil {
		return nil, fmt.Errorf("invalid delete volume req: %v", req)
	}
	diskURI := req.VolumeId

	klog.V(2).Infof("deleting azure disk(%s)", diskURI)
	if isManagedDisk(diskURI) {
		_, err := getResourceGroupFromDiskURI(diskURI)
		if err != nil {
			klog.Errorf("getResourceGroupFromDiskURI(%s) in DeleteVolume failed with error: %v", err)
			return &csi.DeleteVolumeResponse{}, nil
		}

		if err := d.cloud.DeleteManagedDisk(diskURI); err != nil {
			return &csi.DeleteVolumeResponse{}, err
		}
	} else {
		if err := d.cloud.DeleteBlobDisk(diskURI); err != nil {
			return &csi.DeleteVolumeResponse{}, err
		}
	}
	klog.V(2).Infof("delete azure disk(%s) successfullly", diskURI)

	return &csi.DeleteVolumeResponse{}, nil
}

// ControllerPublishVolume attach an azure disk to a required node
func (d *Driver) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	klog.V(2).Infof("ControllerPublishVolume: called with args %+v", *req)
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
	instanceid, err := d.cloud.InstanceID(context.TODO(), nodeName)
	if err != nil {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("failed to get azure instance id for node %q (%v)", nodeID, err))
	}

	diskName, err := getDiskName(diskURI)
	if err != nil {
		return nil, err
	}

	klog.V(2).Infof("GetDiskLun returned: %v. Initiating attaching volume %q to node %q.", err, diskURI, nodeName)
	getLunMutex.LockKey(instanceid)
	defer func() {
		if err := getLunMutex.UnlockKey(instanceid); err != nil {
			klog.Errorf("failed to UnlockKey: %q", instanceid)
		}
	}()

	lun, err := d.cloud.GetDiskLun(diskName, diskURI, nodeName)
	if err == cloudprovider.InstanceNotFound {
		// Log error and continue with attach
		klog.Warningf(
			"Error checking if volume is already attached to current node (%q). Will continue and try attach anyway. err=%v",
			instanceid, err)
	}

	if err == nil {
		// Volume is already attached to node.
		klog.V(2).Infof("Attach operation is successful. volume %q is already attached to node %q at lun %d.", diskURI, instanceid, lun)
	} else {
		lun, err = d.cloud.GetNextDiskLun(nodeName)
		if err != nil {
			klog.Warningf("no LUN available for instance %q (%v)", nodeName, err)
			return nil, fmt.Errorf("all LUNs are used, cannot attach volume %q to instance %q (%v)", diskURI, instanceid, err)
		}
		isManagedDisk := isManagedDisk(diskURI)

		var cachingMode compute.CachingTypes
		if cachingMode, err = getCachingMode(req.GetVolumeContext()); err != nil {
			return nil, err
		}
		klog.V(2).Infof("Trying to attach volume %q lun %d to node %q", diskURI, lun, nodeName)
		err = d.cloud.AttachDisk(isManagedDisk, diskName, diskURI, nodeName, lun, cachingMode)
		if err == nil {
			klog.V(2).Infof("Attach operation successful: volume %q attached to node %q.", diskURI, nodeName)
		} else {
			klog.Errorf("Attach volume %q to instance %q failed with %v", diskURI, instanceid, err)
			return nil, fmt.Errorf("Attach volume %q to instance %q failed with %v", diskURI, instanceid, err)
		}
		klog.V(2).Infof("attach volume %q lun %d to node %q successfully", diskURI, lun, nodeName)
	}

	pvInfo := map[string]string{"devicePath": strconv.Itoa(int(lun))}
	return &csi.ControllerPublishVolumeResponse{PublishContext: pvInfo}, nil
}

// ControllerUnpublishVolume detach an azure disk from a required node
func (d *Driver) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	klog.V(2).Infof("ControllerUnpublishVolume: called with args %+v", *req)
	diskURI := req.GetVolumeId()
	if len(diskURI) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID not provided")
	}

	nodeID := req.GetNodeId()
	if len(nodeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Node ID not provided")
	}
	nodeName := types.NodeName(nodeID)

	instanceid, err := d.cloud.InstanceID(context.TODO(), nodeName)
	if err != nil {
		return nil, fmt.Errorf("failed to get azure instance id for node %q (%v)", nodeID, err)
	}

	diskName, err := getDiskName(diskURI)
	if err != nil {
		return nil, err
	}

	getLunMutex.LockKey(instanceid)
	defer func() {
		if err := getLunMutex.UnlockKey(instanceid); err != nil {
			klog.Errorf("failed to UnlockKey: %q", instanceid)
		}
	}()

	klog.V(2).Infof("Trying to detach volume %s from node %s", diskURI, nodeID)
	if err := d.cloud.DetachDiskByName(diskName, diskURI, nodeName); err != nil {
		if strings.Contains(err.Error(), errDiskNotFound) {
			klog.Warningf("volume %s already detached from node %s", diskURI, nodeID)
		} else {
			return nil, status.Errorf(codes.Internal, "Could not detach volume %q from node %q: %v", diskURI, nodeID, err)
		}
	}
	klog.V(2).Infof("detach volume %s from node %s successfully", diskURI, nodeID)

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
	klog.V(2).Infof("Using default ControllerGetCapabilities")

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
	return nil, status.Error(codes.Unimplemented, "")
}

// CreateSnapshot create a snapshot (todo)
func (d *Driver) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

// DeleteSnapshot delete a snapshot (todo)
func (d *Driver) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

// ListSnapshots list all snapshots (todo)
func (d *Driver) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
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
// if not found, empty string is returned.
func pickAvailabilityZone(requirement *csi.TopologyRequirement) string {
	if requirement == nil {
		return ""
	}
	for _, topology := range requirement.GetPreferred() {
		zone, exists := topology.GetSegments()[topologyKey]
		if exists {
			return zone
		}
	}
	for _, topology := range requirement.GetRequisite() {
		zone, exists := topology.GetSegments()[topologyKey]
		if exists {
			return zone
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
		case "cachingmode":
			cachingMode = v1.AzureDataDiskCachingMode(v)
		}
	}

	cachingMode, err = normalizeCachingMode(cachingMode)
	return compute.CachingTypes(cachingMode), err
}
