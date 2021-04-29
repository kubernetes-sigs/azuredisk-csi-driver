/*
Copyright 2021 The Kubernetes Authors.

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

package provisioner

import (
	"context"
	"fmt"
	"path"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2020-12-01/compute"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientset "k8s.io/client-go/kubernetes"
	cloudprovider "k8s.io/cloud-provider"
	volerr "k8s.io/cloud-provider/volume/errors"
	"k8s.io/klog/v2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

var (
	topologyKeyStr = "N/A"
)

type CloudProvisioner struct {
	cloud *azure.Cloud
}

// listVolumeStatus explains the return status of `listVolumesByResourceGroup`
type listVolumeStatus struct {
	numVisited    int  // the number of iterated azure disks
	isCompleteRun bool // isCompleteRun is flagged true if the function iterated through all azure disks
	entries       []*csi.ListVolumesResponse_Entry
	err           error
}

func NewCloudProvisioner(kubeClient clientset.Interface, topologyKey string) (*CloudProvisioner, error) {
	azCloud, err := azureutils.GetAzureCloudProvider(kubeClient)
	if err != nil {
		return nil, err
	}

	topologyKeyStr = topologyKey

	return &CloudProvisioner{
		cloud: azCloud,
	}, nil
}

func (c *CloudProvisioner) CreateVolume(
	ctx context.Context,
	volumeName string,
	capacityRange *v1alpha1.CapacityRange,
	volumeCapabilities []v1alpha1.VolumeCapability,
	parameters map[string]string,
	secrets map[string]string,
	volumeContentSource *v1alpha1.ContentVolumeSource,
	accessibilityRequirements *v1alpha1.TopologyRequirement) (*v1alpha1.AzVolumeStatusParams, error) {
	err := c.validateCreateVolumeRequestParams(capacityRange, volumeCapabilities, parameters)
	if err != nil {
		return nil, err
	}

	var (
		location                string
		storageAccountType      string
		cachingMode             v1.AzureDataDiskCachingMode
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

	for k, v := range parameters {
		switch strings.ToLower(k) {
		case azureutils.SkuNameField:
			storageAccountType = v
		case azureutils.LocationField:
			location = v
		case azureutils.StorageAccountTypeField:
			storageAccountType = v
		case azureutils.CachingModeField:
			cachingMode = v1.AzureDataDiskCachingMode(v)
		case azureutils.ResourceGroupField:
			resourceGroup = v
		case azureutils.DiskIOPSReadWriteField:
			diskIopsReadWrite = v
		case azureutils.DiskMBPSReadWriteField:
			diskMbpsReadWrite = v
		case azureutils.LogicalSectorSizeField:
			logicalSectorSize, err = strconv.Atoi(v)
			if err != nil {
				return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("parse %s failed with error: %v", v, err))
			}
		case azureutils.DiskNameField:
			diskName = v
		case azureutils.DesIDField:
			diskEncryptionSetID = v
		case azureutils.TagsField:
			customTags = v
		case azure.WriteAcceleratorEnabled:
			writeAcceleratorEnabled = v
		case azureutils.MaxSharesField:
			maxShares, err = strconv.Atoi(v)
			if err != nil {
				return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("parse %s failed with error: %v", v, err))
			}
			if maxShares < 1 {
				return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("parse %s returned with invalid value: %d", v, maxShares))
			}

		// The following parameter is not used by the cloud provisioner, but must be present in the VolumeContext
		// returned to the caller so that it is included in the parameters passed to Node{Publish|Stage}Volume.
		case azureutils.FSTypeField:
			// no-op

		// The following parameter is ignored, but included for backward compatibility with the in-tree azuredisk
		// driver.
		case azureutils.KindField:
			// no-op

		default:
			return nil, fmt.Errorf("invalid parameter %s in storage class", k)
		}
	}

	if diskName == "" {
		diskName = volumeName
	}
	diskName = azureutils.GetValidDiskName(diskName)

	if resourceGroup == "" {
		resourceGroup = c.cloud.ResourceGroup
	}

	// normalize values
	skuName, err := azureutils.NormalizeAzureStorageAccountType(storageAccountType, c.cloud.Config.Cloud, c.cloud.Config.DisableAzureStackCloud)
	if err != nil {
		return nil, err
	}

	if _, err = azureutils.NormalizeAzureDataDiskCachingMode(cachingMode); err != nil {
		return nil, err
	}

	selectedAvailabilityZone := pickAvailabilityZone(accessibilityRequirements, c.GetCloud().Location)
	accessibleTopology := []v1alpha1.Topology{}
	if skuName == compute.StandardSSDZRS || skuName == compute.PremiumZRS {
		klog.V(2).Infof("diskZone(%s) is reset as empty since disk(%s) is ZRS(%s)", selectedAvailabilityZone, diskName, skuName)
		selectedAvailabilityZone = ""
		if len(accessibilityRequirements.Requisite) > 0 {
			accessibleTopology = append(accessibleTopology, accessibilityRequirements.Requisite...)
		} else {
			// make volume scheduled on all 3 availability zones
			for i := 1; i <= 3; i++ {
				topology := v1alpha1.Topology{
					Segments: map[string]string{topologyKeyStr: fmt.Sprintf("%s-%d", c.cloud.Location, i)},
				}
				accessibleTopology = append(accessibleTopology, topology)
			}
			// make volume scheduled on all non-zone nodes
			topology := v1alpha1.Topology{
				Segments: map[string]string{topologyKeyStr: ""},
			}
			accessibleTopology = append(accessibleTopology, topology)
		}
	} else {
		accessibleTopology = []v1alpha1.Topology{
			{
				Segments: map[string]string{topologyKeyStr: selectedAvailabilityZone},
			},
		}
	}

	volSizeBytes := int64(capacityRange.RequiredBytes)
	requestGiB := int(volumehelper.RoundUpGiB(volSizeBytes))
	if requestGiB < azureutils.MinimumDiskSizeGiB {
		requestGiB = azureutils.MinimumDiskSizeGiB
	}

	if ok, err := c.CheckDiskCapacity(ctx, resourceGroup, diskName, requestGiB); !ok {
		return nil, err
	}

	customTagsMap, err := volumehelper.ConvertTagsToMap(customTags)
	if err != nil {
		return nil, err
	}

	klog.V(2).Infof("begin to create disk(%s) account type(%s) rg(%s) location(%s) size(%d) selectedAvailabilityZone(%v) maxShares(%d)",
		diskName, skuName, resourceGroup, location, requestGiB, selectedAvailabilityZone, maxShares)

	tags := make(map[string]string)
	for k, v := range customTagsMap {
		tags[k] = v
	}
	tags[azureutils.CreatedForPVNameKey] = volumeName

	if strings.EqualFold(writeAcceleratorEnabled, "true") {
		tags[azure.WriteAcceleratorEnabled] = "true"
	}
	sourceID := ""
	sourceType := ""

	contentSource := &v1alpha1.ContentVolumeSource{}
	if volumeContentSource != nil {
		sourceID = volumeContentSource.ContentSourceID
		contentSource.ContentSource = volumeContentSource.ContentSource
		contentSource.ContentSourceID = volumeContentSource.ContentSourceID
		sourceType = azureutils.SourceSnapshot
		if volumeContentSource.ContentSource == v1alpha1.ContentVolumeSourceTypeVolume {
			sourceType = azureutils.SourceVolume

			ctx, cancel := context.WithCancel(context.Background())
			if sourceGiB, _ := c.GetSourceDiskSize(ctx, resourceGroup, path.Base(sourceID), 0, azureutils.SourceDiskSearchMaxDepth); sourceGiB != nil && *sourceGiB < int32(requestGiB) {
				parameters[azureutils.ResizeRequired] = strconv.FormatBool(true)
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

	diskURI, err := c.cloud.CreateManagedDisk(volumeOptions)

	if err != nil {
		if strings.Contains(err.Error(), "NotFound") {
			return nil, status.Error(codes.NotFound, err.Error())
		}
		return nil, err
	}

	klog.V(2).Infof("create disk(%s) account type(%s) rg(%s) location(%s) size(%d) tags(%s) successfully", diskName, skuName, resourceGroup, location, requestGiB, tags)

	return &v1alpha1.AzVolumeStatusParams{
		VolumeID:           diskURI,
		CapacityBytes:      capacityRange.RequiredBytes,
		VolumeContext:      parameters,
		ContentSource:      contentSource,
		AccessibleTopology: accessibleTopology,
	}, nil
}

func (c *CloudProvisioner) DeleteVolume(
	ctx context.Context,
	volumeID string,
	secrets map[string]string) error {
	if err := azureutils.IsValidDiskURI(volumeID); err != nil {
		klog.Errorf("validateDiskURI(%s) in DeleteVolume failed with error: %v", volumeID, err)
		return nil
	}

	return c.cloud.DeleteManagedDisk(volumeID)
}

func (c *CloudProvisioner) ListVolumes(
	ctx context.Context,
	maxEntries int32,
	startingToken string) (*csi.ListVolumesResponse, error) {
	start, _ := strconv.Atoi(startingToken)
	kubeClient := c.cloud.KubeClient
	if kubeClient != nil && kubeClient.CoreV1() != nil && kubeClient.CoreV1().PersistentVolumes() != nil {
		klog.V(6).Infof("List Volumes in Cluster:")
		return c.listVolumesInCluster(ctx, start, int(maxEntries))
	}
	klog.V(6).Infof("List Volumes in Node Resource Group: %s", c.cloud.ResourceGroup)
	return c.listVolumesInNodeResourceGroup(ctx, start, int(maxEntries))
}

func (c *CloudProvisioner) PublishVolume(
	ctx context.Context,
	volumeID string,
	nodeID string,
	volumeContext map[string]string) (map[string]string, error) {
	err := c.CheckDiskExists(ctx, volumeID)
	if err != nil {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("Volume not found, failed with error: %v", err))
	}

	nodeName := types.NodeName(nodeID)
	diskName, err := azureutils.GetDiskNameFromAzureManagedDiskURI(volumeID)
	if err != nil {
		return nil, err
	}

	lun, vmState, err := c.cloud.GetDiskLun(diskName, volumeID, nodeName)
	if err == cloudprovider.InstanceNotFound {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("failed to get azure instance id for node %q (%v)", nodeName, err))
	}

	klog.V(2).Infof("GetDiskLun returned: %v. Initiating attaching volume %q to node %q.", lun, volumeID, nodeName)

	if err == nil {
		if vmState != nil && strings.ToLower(*vmState) == "failed" {
			klog.Warningf("VM(%q) is in failed state, update VM first", nodeName)
			if err := c.cloud.UpdateVM(nodeName); err != nil {
				return nil, fmt.Errorf("update instance %q failed with %v", nodeName, err)
			}
		}
		// Volume is already attached to node.
		klog.V(2).Infof("Attach operation is successful. volume %q is already attached to node %q at lun %d.", volumeID, nodeName, lun)
	} else {
		var cachingMode compute.CachingTypes
		if cachingMode, err = azureutils.GetCachingMode(volumeContext); err != nil {
			return nil, err
		}
		klog.V(2).Infof("Trying to attach volume %q to node %q", volumeID, nodeName)

		lun, err = c.cloud.AttachDisk(true, diskName, volumeID, nodeName, cachingMode)
		if err == nil {
			klog.V(2).Infof("Attach operation successful: volume %q attached to node %q.", volumeID, nodeName)
		} else {
			if derr, ok := err.(*volerr.DanglingAttachError); ok {
				klog.Warningf("volume %q is already attached to node %q, try detach first", volumeID, derr.CurrentNode)

				if err = c.cloud.DetachDisk(diskName, volumeID, nodeName); err != nil {
					return nil, status.Errorf(codes.Internal, "Could not detach volume %q from node %q: %v", volumeID, derr.CurrentNode, err)
				}
				klog.V(2).Infof("Trying to attach volume %q to node %q again", volumeID, nodeName)
				lun, err = c.cloud.AttachDisk(true, diskName, volumeID, nodeName, cachingMode)
			}
			if err != nil {
				klog.Errorf("Attach volume %q to instance %q failed with %v", volumeID, nodeName, err)
				return nil, fmt.Errorf("Attach volume %q to instance %q failed with %v", volumeID, nodeName, err)
			}
		}
		klog.V(2).Infof("attach volume %q to node %q successfully", volumeID, nodeName)
	}

	pvInfo := map[string]string{"LUN": strconv.Itoa(int(lun))}
	return pvInfo, nil
}

func (c *CloudProvisioner) UnpublishVolume(
	ctx context.Context,
	volumeID string,
	nodeID string) error {
	nodeName := types.NodeName(nodeID)

	diskName, err := azureutils.GetDiskNameFromAzureManagedDiskURI(volumeID)
	if err != nil {
		return err
	}

	klog.V(2).Infof("Trying to detach volume %s from node %s", volumeID, nodeID)

	if err := c.cloud.DetachDisk(diskName, volumeID, nodeName); err != nil {
		if strings.Contains(err.Error(), azureutils.ErrDiskNotFound) {
			klog.Warningf("volume %s already detached from node %s", volumeID, nodeID)
		} else {
			return status.Errorf(codes.Internal, "Could not detach volume %q from node %q: %v", volumeID, nodeID, err)
		}
	}
	klog.V(2).Infof("detach volume %s from node %s successfully", volumeID, nodeID)

	return nil
}

func (c *CloudProvisioner) ExpandVolume(
	ctx context.Context,
	volumeID string,
	capacityRange *v1alpha1.CapacityRange,
	secrets map[string]string) (*v1alpha1.AzVolumeStatusParams, error) {
	requestSize := *resource.NewQuantity(capacityRange.RequiredBytes, resource.BinarySI)

	if err := azureutils.IsValidDiskURI(volumeID); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "disk URI(%s) is not valid: %v", volumeID, err)
	}

	diskName, err := azureutils.GetDiskNameFromAzureManagedDiskURI(volumeID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "could not get disk name from diskURI(%s) with error(%v)", volumeID, err)
	}
	resourceGroup, err := azureutils.GetResourceGroupFromAzureManagedDiskURI(volumeID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "could not get resource group from diskURI(%s) with error(%v)", volumeID, err)
	}

	result, rerr := c.cloud.DisksClient.Get(ctx, resourceGroup, diskName)
	if rerr != nil {
		return nil, status.Errorf(codes.Internal, "could not get the disk(%s) under rg(%s) with error(%v)", diskName, resourceGroup, rerr.Error())
	}
	if result.DiskProperties.DiskSizeGB == nil {
		return nil, status.Errorf(codes.Internal, "could not get size of the disk(%s)", diskName)
	}
	oldSize := *resource.NewQuantity(int64(*result.DiskProperties.DiskSizeGB), resource.BinarySI)

	klog.V(2).Infof("begin to expand azure disk(%s) with new size(%d)", volumeID, requestSize.Value())
	newSize, err := c.cloud.ResizeDisk(volumeID, oldSize, requestSize)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to resize disk(%s) with error(%v)", volumeID, err)
	}

	currentSize, ok := newSize.AsInt64()
	if !ok {
		return nil, status.Errorf(codes.Internal, "failed to transform disk size with error(%v)", err)
	}

	klog.V(2).Infof("expand azure disk(%s) successfully, currentSize(%d)", volumeID, currentSize)

	return &v1alpha1.AzVolumeStatusParams{
		CapacityBytes:         currentSize,
		NodeExpansionRequired: true,
	}, nil
}

func (c *CloudProvisioner) CreateSnapshot(
	ctx context.Context,
	sourceVolumeID string,
	snapshotName string,
	secrets map[string]string,
	parameters map[string]string) (*csi.CreateSnapshotResponse, error) {
	snapshotName = azureutils.GetValidDiskName(snapshotName)

	var customTags string
	// set incremental snapshot as true by default
	incremental := true
	var resourceGroup string
	var err error

	for k, v := range parameters {
		switch strings.ToLower(k) {
		case azureutils.TagsField:
			customTags = v
		case azureutils.IncrementalField:
			if v == "false" {
				incremental = false
			}
		case azureutils.ResourceGroupField:
			resourceGroup = v
		default:
			return nil, fmt.Errorf("AzureDisk - invalid option %s in VolumeSnapshotClass", k)
		}
	}

	if azureutils.IsAzureStackCloud(c.cloud.Config.Cloud, c.cloud.Config.DisableAzureStackCloud) {
		klog.V(2).Info("Use full snapshot instead as Azure Stack does not support incremental snapshot.")
		incremental = false
	}

	if resourceGroup == "" {
		resourceGroup, err = azureutils.GetResourceGroupFromAzureManagedDiskURI(sourceVolumeID)
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
		Location: &c.cloud.Location,
		Tags:     tags,
	}

	klog.V(2).Infof("begin to create snapshot(%s, incremental: %v) under rg(%s)", snapshotName, incremental, resourceGroup)

	rerr := c.cloud.SnapshotsClient.CreateOrUpdate(ctx, resourceGroup, snapshotName, snapshot)
	if rerr != nil {
		if strings.Contains(rerr.Error().Error(), "existing disk") {
			return nil, status.Error(codes.AlreadyExists, fmt.Sprintf("request snapshot(%s) under rg(%s) already exists, but the SourceVolumeId is different, error details: %v", snapshotName, resourceGroup, rerr.Error()))
		}

		return nil, status.Error(codes.Internal, fmt.Sprintf("create snapshot error: %v", rerr.Error()))
	}
	klog.V(2).Infof("create snapshot(%s) under rg(%s) successfully", snapshotName, resourceGroup)

	csiSnapshot, err := c.GetSnapshotByID(ctx, resourceGroup, snapshotName, sourceVolumeID)
	if err != nil {
		return nil, err
	}

	return &csi.CreateSnapshotResponse{
		Snapshot: csiSnapshot,
	}, nil
}

func (c *CloudProvisioner) ListSnapshots(
	ctx context.Context,
	maxEntries int32,
	startingToken string,
	sourceVolumeID string,
	snapshotID string,
	secrets map[string]string) (*csi.ListSnapshotsResponse, error) {
	// SnapshotID is not empty, return snapshot that match the snapshot id.
	if len(snapshotID) != 0 {
		snapshot, err := c.GetSnapshotByID(ctx, c.cloud.ResourceGroup, snapshotID, sourceVolumeID)
		if err != nil {
			if strings.Contains(err.Error(), azureutils.ResourceNotFound) {
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

	// no SnapshotID is set, return all snapshots that satisfy the request.
	snapshots, rerr := c.cloud.SnapshotsClient.ListByResourceGroup(ctx, c.cloud.ResourceGroup)
	if rerr != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("Unknown list snapshot error: %v", rerr.Error()))
	}

	// There are 4 scenarios for listing snapshots.
	// 1. StartingToken is null, and MaxEntries is null. Return all snapshots from zero.
	// 2. StartingToken is null, and MaxEntries is not null. Return `MaxEntries` snapshots from zero.
	// 3. StartingToken is not null, and MaxEntries is null. Return all snapshots from `StartingToken`.
	// 4. StartingToken is not null, and MaxEntries is not null. Return `MaxEntries` snapshots from `StartingToken`.
	start := 0
	if startingToken != "" {
		start, err := strconv.Atoi(startingToken)
		if err != nil {
			return nil, status.Errorf(codes.Aborted, "ListSnapshots starting token(%s) parsing with error: %v", startingToken, err)

		}
		if start >= len(snapshots) {
			return nil, status.Errorf(codes.Aborted, "ListSnapshots starting token(%d) is greater than total number of snapshots", start)
		}
		if start < 0 {
			return nil, status.Errorf(codes.Aborted, "ListSnapshots starting token(%d) can not be negative", start)
		}
	}

	maxAvailableEntries := len(snapshots) - start
	totalEntries := maxAvailableEntries
	if maxEntries > 0 && int(maxEntries) < maxAvailableEntries {
		totalEntries = int(maxEntries)
	}
	entries := []*csi.ListSnapshotsResponse_Entry{}
	for count := 0; start < len(snapshots) && count < totalEntries; start++ {
		if (sourceVolumeID != "" && sourceVolumeID == azureutils.GetSnapshotSourceVolumeID(&snapshots[start])) || sourceVolumeID == "" {
			csiSnapshot, err := azureutils.GenerateCSISnapshot(sourceVolumeID, &snapshots[start])
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

func (c *CloudProvisioner) DeleteSnapshot(
	ctx context.Context,
	snapshotID string,
	secrets map[string]string) (*csi.DeleteSnapshotResponse, error) {
	snapshotName, resourceGroup, err := c.GetSnapshotAndResourceNameFromSnapshotID(snapshotID)
	if err != nil {
		return nil, err
	}

	if snapshotName == "" && resourceGroup == "" {
		snapshotName = snapshotID
		resourceGroup = c.cloud.ResourceGroup
	}

	klog.V(2).Infof("begin to delete snapshot(%s) under rg(%s)", snapshotName, resourceGroup)
	rerr := c.cloud.SnapshotsClient.Delete(ctx, resourceGroup, snapshotName)
	if rerr != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("delete snapshot error: %v", rerr.Error()))
	}
	klog.V(2).Infof("delete snapshot(%s) under rg(%s) successfully", snapshotName, resourceGroup)

	return &csi.DeleteSnapshotResponse{}, nil
}

func (c *CloudProvisioner) CheckDiskExists(ctx context.Context, diskURI string) error {
	diskName, err := azureutils.GetDiskNameFromAzureManagedDiskURI(diskURI)
	if err != nil {
		return err
	}

	resourceGroup, err := azureutils.GetResourceGroupFromAzureManagedDiskURI(diskURI)
	if err != nil {
		return err
	}

	if _, rerr := c.cloud.DisksClient.Get(ctx, resourceGroup, diskName); rerr != nil {
		return rerr.Error()
	}

	return nil
}

func (c *CloudProvisioner) GetCloud() *azure.Cloud {
	return c.cloud
}

func (c *CloudProvisioner) GetMetricPrefix() string {
	return azureutils.CSIDriverMetricPrefix
}

// GetSourceDiskSize recursively searches for the sourceDisk and returns: sourceDisk disk size, error
func (c *CloudProvisioner) GetSourceDiskSize(ctx context.Context, resourceGroup, diskName string, curDepth, maxDepth int) (*int32, error) {
	if curDepth > maxDepth {
		return nil, status.Error(codes.Internal, fmt.Sprintf("current depth (%d) surpassed the max depth (%d) while searching for the source disk size", curDepth, maxDepth))
	}
	result, rerr := c.cloud.DisksClient.Get(ctx, resourceGroup, diskName)
	if rerr != nil {
		return nil, rerr.Error()
	}
	if result.DiskProperties == nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("DiskProperty not found for disk (%s) in resource group (%s)", diskName, resourceGroup))
	}

	if result.DiskProperties.CreationData != nil && (*result.DiskProperties.CreationData).CreateOption == "Copy" {
		klog.V(2).Infof("Clone source disk has a parent source")
		sourceResourceID := *result.DiskProperties.CreationData.SourceResourceID
		parentResourceGroup, _ := azureutils.GetResourceGroupFromAzureManagedDiskURI(sourceResourceID)
		parentDiskName := path.Base(sourceResourceID)
		return c.GetSourceDiskSize(ctx, parentResourceGroup, parentDiskName, curDepth+1, maxDepth)
	}

	if (*result.DiskProperties).DiskSizeGB == nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("DiskSizeGB for disk (%s) in resourcegroup (%s) is nil", diskName, resourceGroup))
	}
	return (*result.DiskProperties).DiskSizeGB, nil
}

func (c *CloudProvisioner) CheckDiskCapacity(ctx context.Context, resourceGroup, diskName string, requestGiB int) (bool, error) {
	disk, err := c.cloud.DisksClient.Get(ctx, resourceGroup, diskName)
	// Because we can not judge the reason of the error. Maybe the disk does not exist.
	// So here we do not handle the error.
	if err == nil {
		if !reflect.DeepEqual(disk, compute.Disk{}) && disk.DiskSizeGB != nil && int(*disk.DiskSizeGB) != requestGiB {
			return false, status.Errorf(codes.AlreadyExists, "the request volume already exists, but its capacity(%v) is different from (%v)", *disk.DiskProperties.DiskSizeGB, requestGiB)
		}
	}
	return true, nil
}

func (c *CloudProvisioner) GetSnapshotAndResourceNameFromSnapshotID(snapshotID string) (string, string, error) {
	var (
		snapshotName  string
		resourceGroup string
		err           error
	)

	if azureutils.IsARMResourceID(snapshotID) {
		snapshotName, resourceGroup, err = azureutils.GetSnapshotAndResourceNameFromSnapshotID(snapshotID)
	}

	return snapshotName, resourceGroup, err
}

func (c *CloudProvisioner) GetSnapshotByID(ctx context.Context, resourceGroup string, snapshotName string, sourceVolumeID string) (*csi.Snapshot, error) {
	snapshotNameVal, resourceGroupName, err := c.GetSnapshotAndResourceNameFromSnapshotID(snapshotName)
	if err != nil {
		return nil, err
	}

	if snapshotNameVal == "" && resourceGroupName == "" {
		snapshotNameVal = snapshotName
		resourceGroupName = resourceGroup
	}

	snapshot, rerr := c.cloud.SnapshotsClient.Get(ctx, resourceGroupName, snapshotNameVal)
	if rerr != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("get snapshot %s from rg(%s) error: %v", snapshotNameVal, resourceGroupName, rerr.Error()))
	}

	return azureutils.GenerateCSISnapshot(sourceVolumeID, &snapshot)
}

func (c *CloudProvisioner) validateCreateVolumeRequestParams(
	capacityRange *v1alpha1.CapacityRange,
	volumeCaps []v1alpha1.VolumeCapability,
	params map[string]string) error {
	capacityBytes := capacityRange.RequiredBytes
	volSizeBytes := int64(capacityBytes)
	requestGiB := int(volumehelper.RoundUpGiB(volSizeBytes))
	if requestGiB < azureutils.MinimumDiskSizeGiB {
		requestGiB = azureutils.MinimumDiskSizeGiB
	}

	maxVolSize := int(volumehelper.RoundUpGiB(capacityRange.LimitBytes))
	if (maxVolSize > 0) && (maxVolSize < requestGiB) {
		return status.Error(codes.InvalidArgument, "After round-up, volume size exceeds the limit specified")
	}

	maxSharesField := "maxSharesField"
	var maxShares int
	var err error
	for k, v := range params {
		if strings.EqualFold(maxSharesField, k) {
			maxShares, err = strconv.Atoi(v)
			if err != nil {
				return status.Error(codes.InvalidArgument, fmt.Sprintf("parse %s failed with error: %v", v, err))
			}
			if maxShares < 1 {
				return status.Error(codes.InvalidArgument, fmt.Sprintf("parse %s returned with invalid value: %d", v, maxShares))
			}

			break
		}
	}

	if azureutils.IsAzureStackCloud(c.cloud.Config.Cloud, c.cloud.Config.DisableAzureStackCloud) {
		if maxShares > 1 {
			return status.Error(codes.InvalidArgument, fmt.Sprintf("Invalid maxShares value: %d as Azure Stack does not support shared disk.", maxShares))
		}
	}

	if maxShares < 2 {
		for _, c := range volumeCaps {
			mode := c.AccessMode
			if mode != v1alpha1.VolumeCapabilityAccessModeSingleNodeWriter &&
				mode != v1alpha1.VolumeCapabilityAccessModeSingleNodeReaderOnly {
				return status.Error(codes.InvalidArgument, fmt.Sprintf("Volume capability(%v) not supported", mode))
			}
		}
	}

	return nil
}

// listVolumesInCluster is a helper function for ListVolumes used for when there is an available kubeclient
func (c *CloudProvisioner) listVolumesInCluster(ctx context.Context, start, maxEntries int) (*csi.ListVolumesResponse, error) {
	kubeClient := c.cloud.KubeClient
	pvList, err := kubeClient.CoreV1().PersistentVolumes().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "ListVolumes failed while fetching PersistentVolumes List with error: %v", err.Error())
	}

	// get all resource groups and put them into a sorted slice
	rgMap := make(map[string]bool)
	volSet := make(map[string]bool)
	for _, pv := range pvList.Items {
		if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == azureutils.DriverName {
			diskURI := pv.Spec.CSI.VolumeHandle
			if err := azureutils.IsValidDiskURI(diskURI); err != nil {
				klog.Warningf("invalid disk uri (%s) with error(%v)", diskURI, err)
				continue
			}
			rg, err := azureutils.GetResourceGroupFromAzureManagedDiskURI(diskURI)
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
		listStatus := c.listVolumesByResourceGroup(ctx, resourceGroup, entries, localStart, maxEntries-len(entries), volSet)
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
func (c *CloudProvisioner) listVolumesInNodeResourceGroup(ctx context.Context, start, maxEntries int) (*csi.ListVolumesResponse, error) {
	entries := []*csi.ListVolumesResponse_Entry{}
	listStatus := c.listVolumesByResourceGroup(ctx, c.cloud.ResourceGroup, entries, start, maxEntries, nil)
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
func (c *CloudProvisioner) listVolumesByResourceGroup(ctx context.Context, resourceGroup string, entries []*csi.ListVolumesResponse_Entry, start, maxEntries int, volSet map[string]bool) listVolumeStatus {
	disks, derr := c.cloud.DisksClient.ListByResourceGroup(ctx, resourceGroup)
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
			err:        status.Errorf(codes.FailedPrecondition, "ListVolumes starting token(%d) on rg(%s) is greater than total number of volumes", start, c.cloud.ResourceGroup),
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
				attachedNode, err := c.cloud.VMSet.GetNodeNameByProviderID(*disk.ManagedBy)
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

// pickAvailabilityZone selects 1 zone given topology requirement.
// if not found or topology requirement is not zone format, empty string is returned.
func pickAvailabilityZone(requirement *v1alpha1.TopologyRequirement, region string) string {
	if requirement == nil {
		return ""
	}

	for _, topology := range requirement.Preferred {
		topologySegments := topology.Segments
		if zone, exists := topologySegments[azureutils.WellKnownTopologyKey]; exists {
			if azureutils.IsValidAvailabilityZone(zone, region) {
				return zone
			}
		}
		if zone, exists := topologySegments[topologyKeyStr]; exists {
			if azureutils.IsValidAvailabilityZone(zone, region) {
				return zone
			}
		}
	}

	for _, topology := range requirement.Requisite {
		topologySegments := topology.Segments
		if zone, exists := topologySegments[azureutils.WellKnownTopologyKey]; exists {
			if azureutils.IsValidAvailabilityZone(zone, region) {
				return zone
			}
		}
		if zone, exists := topologySegments[topologyKeyStr]; exists {
			if azureutils.IsValidAvailabilityZone(zone, region) {
				return zone
			}
		}
	}
	return ""
}
