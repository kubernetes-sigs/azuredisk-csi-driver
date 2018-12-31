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

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cloud-provider"
	"k8s.io/kubernetes/pkg/cloudprovider/providers/azure"
	"k8s.io/kubernetes/pkg/util/keymutex"
	"k8s.io/kubernetes/pkg/volume/util"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2018-10-01/compute"
	"github.com/Azure/azure-sdk-for-go/services/storage/mgmt/2018-07-01/storage"
	"github.com/container-storage-interface/spec/lib/go/csi/v0"
	"github.com/golang/glog"
	"github.com/pborman/uuid"

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

	// controllerCaps represents the capability of controller service
	controllerCaps = []csi.ControllerServiceCapability_RPC_Type{
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
	}

	getLunMutex = keymutex.NewHashed(0)
)

// CreateVolume provisions an azure disk
func (d *Driver) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	if err := d.ValidateControllerServiceRequest(csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME); err != nil {
		glog.Errorf("invalid create volume req: %v", req)
		return nil, err
	}

	volumeCapabilities := req.GetVolumeCapabilities()
	name := req.GetName()
	if len(name) == 0 {
		return nil, status.Error(codes.InvalidArgument, "CreateVolume Name must be provided")
	}
	if volumeCapabilities == nil || len(volumeCapabilities) == 0 {
		return nil, status.Error(codes.InvalidArgument, "CreateVolume Volume capabilities must be provided")
	}

	if !isValidVolumeCapabilities(volumeCapabilities) {
		return nil, status.Error(codes.InvalidArgument, "Volume capabilities not supported")
	}

	volSizeBytes := int64(req.GetCapacityRange().GetRequiredBytes())
	requestGiB := int(util.RoundUpSize(volSizeBytes, 1024*1024*1024))

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
			return nil, fmt.Errorf("AzureDisk - invalid option %s in storage class", k)
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

	if cachingMode, err = normalizeCachingMode(cachingMode); err != nil {
		return nil, err
	}

	// create disk
	glog.V(2).Infof("begin to create azure disk(%s) account type(%s) rg(%s) location(%s) size(%d)", diskName, skuName, resourceGroup, location, requestGiB)

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

	glog.V(2).Infof("create azure disk(%s) account type(%s) rg(%s) location(%s) size(%d) successfully", diskName, skuName, resourceGroup, location, requestGiB)

	/*  todo: block volume support
	if utilfeature.DefaultFeatureGate.Enabled(features.BlockVolume) {
		volumeMode = p.options.PVC.Spec.VolumeMode
		if volumeMode != nil && *volumeMode == v1.PersistentVolumeBlock {
			// Block volumes should not have any FSType
			fsType = ""
		}
	}
	*/

	if req.GetVolumeContentSource() != nil {
		contentSource := req.GetVolumeContentSource()
		if contentSource.GetSnapshot() != nil {
		}
	}

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			Id:            diskURI,
			CapacityBytes: req.GetCapacityRange().GetRequiredBytes(),
			Attributes:    parameters,
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

	glog.V(2).Infof("deleting azure disk(%s)", diskURI)
	if isManagedDisk(diskURI) {
		if err := d.cloud.DeleteManagedDisk(diskURI); err != nil {
			return &csi.DeleteVolumeResponse{}, err
		}
	} else {
		if err := d.cloud.DeleteBlobDisk(diskURI); err != nil {
			return &csi.DeleteVolumeResponse{}, err
		}
	}

	return &csi.DeleteVolumeResponse{}, nil
}

// ControllerPublishVolume attach an azure disk to a required node
func (d *Driver) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	glog.V(2).Infof("ControllerPublishVolume: called with args %+v", *req)
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

	lun, err := d.cloud.GetDiskLun(diskName, diskURI, nodeName)
	if err == cloudprovider.InstanceNotFound {
		// Log error and continue with attach
		glog.Warningf(
			"Error checking if volume is already attached to current node (%q). Will continue and try attach anyway. err=%v",
			instanceid, err)
	}

	if err == nil {
		// Volume is already attached to node.
		glog.V(2).Infof("Attach operation is successful. volume %q is already attached to node %q at lun %d.", diskURI, instanceid, lun)
	} else {
		glog.V(2).Infof("GetDiskLun returned: %v. Initiating attaching volume %q to node %q.", err, diskURI, nodeName)
		getLunMutex.LockKey(instanceid)
		defer getLunMutex.UnlockKey(instanceid)

		lun, err = d.cloud.GetNextDiskLun(nodeName)
		if err != nil {
			glog.Warningf("no LUN available for instance %q (%v)", nodeName, err)
			return nil, fmt.Errorf("all LUNs are used, cannot attach volume %q to instance %q (%v)", diskURI, instanceid, err)
		}
		glog.V(2).Infof("Trying to attach volume %q lun %d to node %q.", diskURI, lun, nodeName)
		isManagedDisk := isManagedDisk(diskURI)
		// todo: get cachingMode from req.GetVolumeAttributes(), now it's default as ReadOnly

		err = d.cloud.AttachDisk(isManagedDisk, diskName, diskURI, nodeName, lun, compute.CachingTypesReadOnly)
		if err == nil {
			glog.V(2).Infof("Attach operation successful: volume %q attached to node %q.", diskURI, nodeName)
		} else {
			glog.V(2).Infof("Attach volume %q to instance %q failed with %v", diskURI, instanceid, err)
			return nil, fmt.Errorf("Attach volume %q to instance %q failed with %v", diskURI, instanceid, err)
		}
	}

	pvInfo := map[string]string{"devicePath": strconv.Itoa(int(lun))}
	return &csi.ControllerPublishVolumeResponse{PublishInfo: pvInfo}, nil
}

// ControllerUnpublishVolume detach an azure disk from a required node
func (d *Driver) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	glog.V(2).Infof("ControllerUnpublishVolume: called with args %+v", *req)
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
	defer getLunMutex.UnlockKey(instanceid)

	if err := d.cloud.DetachDiskByName(diskName, diskURI, nodeName); err != nil {
		return nil, status.Errorf(codes.Internal, "Could not detach volume %q from node %q: %v", diskURI, nodeID, err)
	}
	glog.V(2).Infof("ControllerUnpublishVolume: volume %s detached from node %s", diskURI, nodeID)

	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

// ValidateVolumeCapabilities return the capabilities of the volume
func (d *Driver) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	if len(req.GetVolumeId()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}
	if req.GetVolumeCapabilities() == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume capabilities missing in request")
	}

	for _, cap := range req.VolumeCapabilities {
		if cap.GetAccessMode().GetMode() != csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER {
			return &csi.ValidateVolumeCapabilitiesResponse{Supported: false, Message: ""}, nil
		}
	}
	return &csi.ValidateVolumeCapabilitiesResponse{Supported: true, Message: ""}, nil
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

// ControllerGetCapabilities returns the capabilities of the Controller plugin
func (d *Driver) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	glog.V(2).Infof("Using default ControllerGetCapabilities")

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
