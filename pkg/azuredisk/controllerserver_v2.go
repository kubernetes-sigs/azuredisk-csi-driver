// +build azurediskv2

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
	"strconv"

	"github.com/container-storage-interface/spec/lib/go/csi"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"

	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	"sigs.k8s.io/cloud-provider-azure/pkg/metrics"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

// TODO: Make CloudProvisioner independent of csi types.
type CloudProvisioner interface {
	CreateVolume(ctx context.Context, volumeName string, capacityRange *csi.CapacityRange, volumeCapabilities []*csi.VolumeCapability, parameters map[string]string, secrets map[string]string, volumeContentSource *csi.VolumeContentSource, accessibilityRequirements *csi.TopologyRequirement) (*csi.CreateVolumeResponse, error)
	DeleteVolume(ctx context.Context, volumeID string, secrets map[string]string) error
	ListVolumes(ctx context.Context, maxEntries int32, startingToken string) (*csi.ListVolumesResponse, error)
	PublishVolume(ctx context.Context, volumeID string, nodeID string, volumeCapability *csi.VolumeCapability, readOnly bool, secrets map[string]string, volumeContext map[string]string) (*csi.ControllerPublishVolumeResponse, error)
	UnpublishVolume(ctx context.Context, volumeID string, nodeID string, secrets map[string]string) (*csi.ControllerUnpublishVolumeResponse, error)
	ExpandVolume(ctx context.Context, volumeID string, capacityRange *csi.CapacityRange, secrets map[string]string) (*csi.ControllerExpandVolumeResponse, error)
	CreateSnapshot(ctx context.Context, sourceVolumeID string, snapshotName string, secrets map[string]string, parameters map[string]string) (*csi.CreateSnapshotResponse, error)
	ListSnapshots(ctx context.Context, maxEntries int32, startingToken string, sourceVolumeID string, snapshotID string, secrets map[string]string) (*csi.ListSnapshotsResponse, error)
	DeleteSnapshot(ctx context.Context, snapshotID string, secrets map[string]string) (*csi.DeleteSnapshotResponse, error)
	CheckDiskExists(ctx context.Context, diskURI string) error
	GetDiskClientSet() azDiskClientSet.Interface
	GetDiskClientSetAddr() *azDiskClientSet.Interface
	GetCloud() *azure.Cloud
	GetMetricPrefix() string
}

// CreateVolume provisions an azure disk
func (d *DriverV2) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
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

	parameters := req.GetParameters()
	if parameters == nil {
		parameters = make(map[string]string)
	}

	// Delete parameters used only during node publish/stage and not understood or used by the cloud provisioner.
	delete(parameters, fsTypeField)
	delete(parameters, kindField)

	mc := metrics.NewMetricContext(d.cloudProvisioner.GetMetricPrefix(), "controller_create_volume", d.cloudProvisioner.GetCloud().ResourceGroup, d.cloudProvisioner.GetCloud().SubscriptionID, d.Name)
	isOperationSucceeded := false
	defer func() {
		mc.ObserveOperationWithResult(isOperationSucceeded)
	}()

	response, err := d.cloudProvisioner.CreateVolume(ctx, name, req.GetCapacityRange(), volCaps, parameters, req.GetSecrets(), req.GetVolumeContentSource(), req.GetAccessibilityRequirements())

	if err != nil {
		return nil, err
	}

	if response == nil {
		return nil, status.Error(codes.Unknown, "Error creating volume")
	}

	isOperationSucceeded = true

	return response, nil
}

// DeleteVolume delete an azure disk
func (d *DriverV2) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}

	if err := d.ValidateControllerServiceRequest(csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME); err != nil {
		return nil, fmt.Errorf("invalid delete volume req: %v", req)
	}

	if acquired := d.volumeLocks.TryAcquire(volumeID); !acquired {
		return nil, status.Errorf(codes.Aborted, volumeOperationAlreadyExistsFmt, volumeID)
	}
	defer d.volumeLocks.Release(volumeID)

	mc := metrics.NewMetricContext(d.cloudProvisioner.GetMetricPrefix(), "controller_delete_volume", d.cloudProvisioner.GetCloud().ResourceGroup, d.cloudProvisioner.GetCloud().SubscriptionID, d.Name)
	isOperationSucceeded := false
	defer func() {
		mc.ObserveOperationWithResult(isOperationSucceeded)
	}()

	klog.V(2).Infof("deleting disk(%s)", volumeID)
	err := d.cloudProvisioner.DeleteVolume(ctx, volumeID, req.GetSecrets())
	klog.V(2).Infof("delete disk(%s) returned with %v", volumeID, err)
	isOperationSucceeded = (err == nil)
	return &csi.DeleteVolumeResponse{}, err
}

// ControllerGetVolume get volume
func (d *DriverV2) ControllerGetVolume(context.Context, *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

// ControllerPublishVolume attach an azure disk to a required node
func (d *DriverV2) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
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

	mc := metrics.NewMetricContext(d.cloudProvisioner.GetMetricPrefix(), "controller_publish_volume", d.cloudProvisioner.GetCloud().ResourceGroup, d.cloudProvisioner.GetCloud().SubscriptionID, d.Name)
	isOperationSucceeded := false
	defer func() {
		mc.ObserveOperationWithResult(isOperationSucceeded)
	}()

	response, err := d.cloudProvisioner.PublishVolume(ctx, diskURI, nodeID, volCap, req.GetReadonly(), req.GetSecrets(), req.GetVolumeContext())

	if err != nil {
		return nil, err
	}

	if response == nil {
		return nil, status.Error(codes.Unknown, "Error publishing volume")
	}

	isOperationSucceeded = true
	return response, nil
}

// ControllerUnpublishVolume detach an azure disk from a required node
func (d *DriverV2) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	diskURI := req.GetVolumeId()
	if len(diskURI) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID not provided")
	}

	nodeID := req.GetNodeId()
	if len(nodeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Node ID not provided")
	}

	mc := metrics.NewMetricContext(d.cloudProvisioner.GetMetricPrefix(), "controller_unpublish_volume", d.cloudProvisioner.GetCloud().ResourceGroup, d.cloudProvisioner.GetCloud().SubscriptionID, d.Name)
	isOperationSucceeded := false
	defer func() {
		mc.ObserveOperationWithResult(isOperationSucceeded)
	}()

	response, err := d.cloudProvisioner.UnpublishVolume(ctx, diskURI, nodeID, req.GetSecrets())

	if err != nil {
		return nil, err
	}

	if response == nil {
		return nil, status.Error(codes.Unknown, "Error unpublishing volume")
	}

	isOperationSucceeded = true

	return response, nil
}

// ValidateVolumeCapabilities return the capabilities of the volume
func (d *DriverV2) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	diskURI := req.GetVolumeId()
	if len(diskURI) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}
	if req.GetVolumeCapabilities() == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume capabilities missing in request")
	}

	err := d.cloudProvisioner.CheckDiskExists(ctx, diskURI)
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
func (d *DriverV2) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: d.Cap,
	}, nil
}

// GetCapacity returns the capacity of the total available storage pool
func (d *DriverV2) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

// ListVolumes return all available volumes
func (d *DriverV2) ListVolumes(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	start := 0
	var err error
	startingToken := req.GetStartingToken()
	if startingToken != "" {
		start, err = strconv.Atoi(startingToken)
		if err != nil {
			return nil, status.Errorf(codes.Aborted, "ListVolumes starting token(%s) parsing with error: %v", startingToken, err)
		}
		if start < 0 {
			return nil, status.Errorf(codes.Aborted, "ListVolumes starting token(%d) can not be negative", start)
		}
	}

	response, err := d.cloudProvisioner.ListVolumes(ctx, req.GetMaxEntries(), startingToken)

	if err != nil {
		return nil, err
	}

	if response == nil {
		return nil, status.Error(codes.Unknown, "Error listing volumes")
	}

	return response, nil
}

// ControllerExpandVolume controller expand volume
func (d *DriverV2) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	diskURI := req.GetVolumeId()
	if len(diskURI) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}
	if err := d.ValidateControllerServiceRequest(csi.ControllerServiceCapability_RPC_EXPAND_VOLUME); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid expand volume request: %v", req)
	}

	capacityBytes := req.GetCapacityRange().GetRequiredBytes()
	if capacityBytes == 0 {
		return nil, status.Error(codes.InvalidArgument, "volume capacity range missing in request")
	}

	mc := metrics.NewMetricContext(d.cloudProvisioner.GetMetricPrefix(), "controller_expand_volume", d.cloudProvisioner.GetCloud().ResourceGroup, d.cloudProvisioner.GetCloud().SubscriptionID, d.Name)
	isOperationSucceeded := false
	defer func() {
		mc.ObserveOperationWithResult(isOperationSucceeded)
	}()

	response, err := d.cloudProvisioner.ExpandVolume(ctx, diskURI, req.GetCapacityRange(), req.GetSecrets())

	if err != nil {
		return nil, err
	}

	if response == nil {
		return nil, status.Error(codes.Unknown, "Error exanding volume")
	}

	isOperationSucceeded = true
	return response, nil
}

// CreateSnapshot create a snapshot
func (d *DriverV2) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	sourceVolumeID := req.GetSourceVolumeId()
	if len(sourceVolumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "CreateSnapshot Source Volume ID must be provided")
	}
	snapshotName := req.GetName()
	if len(snapshotName) == 0 {
		return nil, status.Error(codes.InvalidArgument, "snapshot name must be provided")
	}

	mc := metrics.NewMetricContext(d.cloudProvisioner.GetMetricPrefix(), "controller_create_snapshot", d.cloudProvisioner.GetCloud().ResourceGroup, d.cloudProvisioner.GetCloud().SubscriptionID, d.Name)
	isOperationSucceeded := false
	defer func() {
		mc.ObserveOperationWithResult(isOperationSucceeded)
	}()

	response, err := d.cloudProvisioner.CreateSnapshot(ctx, sourceVolumeID, snapshotName, req.GetSecrets(), req.GetParameters())

	if err != nil {
		return nil, err
	}

	if response == nil {
		return nil, status.Error(codes.Unknown, "Error creating snapshot")
	}

	isOperationSucceeded = true
	return response, nil
}

// DeleteSnapshot delete a snapshot
func (d *DriverV2) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	snapshotID := req.GetSnapshotId()
	if len(snapshotID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Snapshot ID must be provided")
	}

	mc := metrics.NewMetricContext(d.cloudProvisioner.GetMetricPrefix(), "controller_delete_snapshot", d.cloudProvisioner.GetCloud().ResourceGroup, d.cloudProvisioner.GetCloud().SubscriptionID, d.Name)
	isOperationSucceeded := false
	defer func() {
		mc.ObserveOperationWithResult(isOperationSucceeded)
	}()

	response, err := d.cloudProvisioner.DeleteSnapshot(ctx, snapshotID, req.GetSecrets())

	if err != nil {
		return nil, err
	}

	if response == nil {
		return nil, status.Error(codes.Unknown, "Error deleting snapshot")
	}

	isOperationSucceeded = true
	return response, nil
}

// ListSnapshots list all snapshots
func (d *DriverV2) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	return d.cloudProvisioner.ListSnapshots(ctx, req.GetMaxEntries(), req.GetStartingToken(), req.GetSourceVolumeId(), req.GetSnapshotId(), req.GetSecrets())
}
