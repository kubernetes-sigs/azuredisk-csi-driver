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
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2020-12-01/compute"
	"github.com/Azure/go-autorest/autorest/to"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientset "k8s.io/client-go/kubernetes"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/klog/v2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/optimization"
	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	azcache "sigs.k8s.io/cloud-provider-azure/pkg/cache"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

var (
	topologyKeyStr = "N/A"
)

type CloudProvisioner struct {
	cloud                      *azure.Cloud
	kubeClient                 *clientset.Clientset
	cloudConfigSecretName      string
	cloudConfigSecretNamespace string
	// a timed cache GetDisk throttling
	getDiskThrottlingCache *azcache.TimedCache
}

// listVolumeStatus explains the return status of `listVolumesByResourceGroup`
type listVolumeStatus struct {
	numVisited    int  // the number of iterated azure disks
	isCompleteRun bool // isCompleteRun is flagged true if the function iterated through all azure disks
	entries       []v1alpha1.VolumeEntry
	err           error
}

func NewCloudProvisioner(
	kubeClient *clientset.Clientset,
	cloudConfigSecretName string,
	cloudConfigSecretNamespace string,
	topologyKey string,
	userAgent string) (*CloudProvisioner, error) {
	azCloud, err := azureutils.GetCloudProviderFromClient(kubeClient, cloudConfigSecretName, cloudConfigSecretNamespace, userAgent)
	if err != nil || azCloud.TenantID == "" || azCloud.SubscriptionID == "" {
		klog.Fatalf("failed to get Azure Cloud Provider, error: %v", err)
		return nil, err
	}

	topologyKeyStr = topologyKey

	cache, err := azcache.NewTimedcache(5*time.Minute, func(key string) (interface{}, error) {
		return nil, nil
	})
	if err != nil {
		klog.Fatalf("failed to create disk throttling cache: %v", err)
	}

	return &CloudProvisioner{
		cloud:                      azCloud,
		kubeClient:                 kubeClient,
		cloudConfigSecretName:      cloudConfigSecretName,
		cloudConfigSecretNamespace: cloudConfigSecretNamespace,
		getDiskThrottlingCache:     cache,
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
	if err := c.validateCreateVolumeRequestParams(capacityRange, volumeCapabilities, parameters); err != nil {
		return nil, err
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
		netAccessPolicy         string
		diskAccessID            string
		enableBursting          *bool
	)

	localCloud := c.cloud
	tags := make(map[string]string)
	if parameters == nil {
		parameters = make(map[string]string)
	}

	for k, v := range parameters {
		switch strings.ToLower(k) {
		case azureconstants.SkuNameField:
			storageAccountType = v
		case azureconstants.LocationField:
			location = v
		case azureconstants.StorageAccountTypeField:
			storageAccountType = v
		case azureconstants.CachingModeField:
			cachingMode = v1.AzureDataDiskCachingMode(v)
		case azureconstants.ResourceGroupField:
			resourceGroup = v
		case azureconstants.DiskIOPSReadWriteField:
			diskIopsReadWrite = v
		case azureconstants.DiskMBPSReadWriteField:
			diskMbpsReadWrite = v
		case azureconstants.LogicalSectorSizeField:
			logicalSectorSize, err = strconv.Atoi(v)
			if err != nil {
				return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("parse %s failed with error: %v", v, err))
			}
		case azureconstants.DiskNameField:
			diskName = v
		case azureconstants.DesIDField:
			diskEncryptionSetID = v
		case azureconstants.TagsField:
			customTags = v
		case azure.WriteAcceleratorEnabled:
			writeAcceleratorEnabled = v
		case azureconstants.MaxSharesField:
			maxShares, err = strconv.Atoi(v)
			if err != nil {
				return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("parse %s failed with error: %v", v, err))
			}
			if maxShares < 1 {
				return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("parse %s returned with invalid value: %d", v, maxShares))
			}
		case azureconstants.MaxMountReplicaCountField:
			continue
		case azureconstants.PvcNameKey:
			tags[azureconstants.PvcNameTag] = v
		case azureconstants.PvcNamespaceKey:
			tags[azureconstants.PvcNamespaceTag] = v
		case azureconstants.PvNameKey:
			tags[azureconstants.PvNameTag] = v

		case azureconstants.PerfProfileField:
			if !optimization.IsValidPerfProfile(v) {
				return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("Perf profile %s is not supported. Supported tuning modes are none and basic.", v))
			}
		case azureconstants.NetworkAccessPolicyField:
			netAccessPolicy = v
		case azureconstants.DiskAccessIDField:
			diskAccessID = v
		case azureconstants.EnableBurstingField:
			if strings.EqualFold(v, azureconstants.TrueValue) {
				enableBursting = to.BoolPtr(true)
			}
		case azureconstants.UserAgentField:
			newUserAgent := v
			localCloud, err = azureutils.GetCloudProviderFromClient(c.kubeClient, c.cloudConfigSecretName, c.cloudConfigSecretNamespace, newUserAgent)
			if err != nil {
				return nil, fmt.Errorf("create cloud with UserAgent(%s) failed with: (%s)", newUserAgent, err)
			}
		// The following parameters are not used by the cloud provisioner, but must be present in the VolumeContext
		// returned to the caller so that it is included in the parameters passed to Node{Publish|Stage}Volume.
		case azureconstants.ZonedField:
			// no op, only for backward compatibility with in-tree driver
		case azureconstants.FsTypeField:
			// no-op
		case azureconstants.KindField:
			// fix csi migration issue: https://github.com/kubernetes/kubernetes/issues/103433
			parameters[azureconstants.KindField] = string(v1.AzureManagedDisk)
		default:
			return nil, fmt.Errorf("invalid parameter %s in storage class", k)
		}
	}

	if diskName == "" {
		diskName = volumeName
	}
	diskName = azureutils.CreateValidDiskName(diskName, true)

	if resourceGroup == "" {
		resourceGroup = c.cloud.ResourceGroup
	}

	// normalize values
	skuName, err := azureutils.NormalizeStorageAccountType(storageAccountType, localCloud.Config.Cloud, localCloud.Config.DisableAzureStackCloud)
	if err != nil {
		return nil, err
	}

	if _, err = azureutils.NormalizeCachingMode(cachingMode, maxShares); err != nil {
		return nil, err
	}

	networkAccessPolicy, err := azureutils.NormalizeNetworkAccessPolicy(netAccessPolicy)
	if err != nil {
		return nil, err
	}

	selectedAvailabilityZone := pickAvailabilityZone(accessibilityRequirements, c.GetCloud().Location)
	accessibleTopology := []v1alpha1.Topology{}
	if skuName == compute.StandardSSDZRS || skuName == compute.PremiumZRS {
		klog.V(2).Infof("diskZone(%s) is reset as empty since disk(%s) is ZRS(%s)", selectedAvailabilityZone, diskName, skuName)
		selectedAvailabilityZone = ""
		if accessibilityRequirements != nil && len(accessibilityRequirements.Requisite) > 0 {
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

	requestGiB := azureconstants.MinimumDiskSizeGiB
	volSizeBytes := volumehelper.GiBToBytes(int64(requestGiB))

	if capacityRange != nil {
		volSizeBytes = int64(capacityRange.RequiredBytes)
		requestGiB = int(volumehelper.RoundUpGiB(volSizeBytes))
		if requestGiB < azureconstants.MinimumDiskSizeGiB {
			requestGiB = azureconstants.MinimumDiskSizeGiB
			volSizeBytes = volumehelper.GiBToBytes(int64(requestGiB))
		}
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

	for k, v := range customTagsMap {
		tags[k] = v
	}

	if strings.EqualFold(writeAcceleratorEnabled, azureconstants.TrueValue) {
		tags[azure.WriteAcceleratorEnabled] = azureconstants.TrueValue
	}
	sourceID := ""
	sourceType := ""
	contentSource := &v1alpha1.ContentVolumeSource{}
	if volumeContentSource != nil {
		sourceID = volumeContentSource.ContentSourceID
		contentSource.ContentSource = volumeContentSource.ContentSource
		contentSource.ContentSourceID = volumeContentSource.ContentSourceID
		sourceType = azureconstants.SourceSnapshot
		if volumeContentSource.ContentSource == v1alpha1.ContentVolumeSourceTypeVolume {
			sourceType = azureconstants.SourceVolume

			ctx, cancel := context.WithCancel(context.Background())
			if sourceGiB, _ := c.GetSourceDiskSize(ctx, resourceGroup, path.Base(sourceID), 0, azureconstants.SourceDiskSearchMaxDepth); sourceGiB != nil && *sourceGiB < int32(requestGiB) {
				parameters[azureconstants.ResizeRequired] = strconv.FormatBool(true)
			}
			cancel()
		}
	}

	parameters[azureconstants.RequestedSizeGib] = strconv.Itoa(requestGiB)
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
		BurstingEnabled:     enableBursting,
	}

	volumeOptions.SkipGetDiskOperation = c.isGetDiskThrottled()

	// Azure Stack Cloud does not support NetworkAccessPolicy
	if !azureutils.IsAzureStackCloud(localCloud.Config.Cloud, localCloud.Config.DisableAzureStackCloud) {
		volumeOptions.NetworkAccessPolicy = networkAccessPolicy
		if diskAccessID != "" {
			volumeOptions.DiskAccessID = &diskAccessID
		}
	}

	diskURI, err := localCloud.CreateManagedDisk(volumeOptions)
	if err != nil {
		if strings.Contains(err.Error(), "NotFound") {
			return nil, status.Error(codes.NotFound, err.Error())
		}
		return nil, err
	}

	klog.V(2).Infof("create disk(%s) account type(%s) rg(%s) location(%s) size(%d) tags(%s) successfully", diskName, skuName, resourceGroup, location, requestGiB, tags)

	return &v1alpha1.AzVolumeStatusParams{
		VolumeID:           diskURI,
		CapacityBytes:      volSizeBytes,
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

	return c.cloud.DeleteManagedDisk(ctx, volumeID)
}

func (c *CloudProvisioner) ListVolumes(
	ctx context.Context,
	maxEntries int32,
	startingToken string) (*v1alpha1.ListVolumesResult, error) {
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
	disk, err := c.CheckDiskExists(ctx, volumeID)
	if err != nil {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("Volume not found, failed with error: %v", err))
	}

	nodeName := types.NodeName(nodeID)
	diskName, err := azureutils.GetDiskName(volumeID)
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
		klog.V(2).Infof("Trying to attach volume %q to node %q.", volumeID, nodeName)
		var cachingMode compute.CachingTypes
		if cachingMode, err = azureutils.GetCachingMode(volumeContext); err != nil {
			return nil, err
		}

		lun, err = c.cloud.AttachDisk(ctx, true, diskName, volumeID, nodeName, cachingMode, disk)
		if err != nil {
			klog.Errorf("attach volume %q to instance %q failed with %v", volumeID, nodeName, err)
			return nil, err
		}
		klog.V(2).Infof("attach operation successful: volume %q attached to node %q.", volumeID, nodeName)
	}

	pvInfo := map[string]string{"LUN": strconv.Itoa(int(lun))}
	return pvInfo, nil
}

func (c *CloudProvisioner) UnpublishVolume(
	ctx context.Context,
	volumeID string,
	nodeID string) error {
	nodeName := types.NodeName(nodeID)

	diskName, err := azureutils.GetDiskName(volumeID)
	if err != nil {
		return err
	}

	klog.V(2).Infof("Trying to detach volume %s from node %s", volumeID, nodeID)

	if err := c.cloud.DetachDisk(ctx, diskName, volumeID, nodeName); err != nil {
		if strings.Contains(err.Error(), azureconstants.ErrDiskNotFound) {
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

	diskName, err := azureutils.GetDiskName(volumeID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "could not get disk name from diskURI(%s) with error(%v)", volumeID, err)
	}
	resourceGroup, err := azureutils.GetResourceGroupFromURI(volumeID)
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
	parameters map[string]string) (*v1alpha1.Snapshot, error) {
	snapshotName = azureutils.CreateValidDiskName(snapshotName, true)

	var customTags string
	// set incremental snapshot as true by default
	incremental := true
	var resourceGroup string
	var err error
	localCloud := c.cloud

	for k, v := range parameters {
		switch strings.ToLower(k) {
		case azureconstants.TagsField:
			customTags = v
		case azureconstants.IncrementalField:
			if v == "false" {
				incremental = false
			}
		case azureconstants.ResourceGroupField:
			resourceGroup = v
		case azureconstants.UserAgentField:
			newUserAgent := v
			localCloud, err = azureutils.GetCloudProviderFromClient(c.kubeClient, c.cloudConfigSecretName, c.cloudConfigSecretNamespace, newUserAgent)
			if err != nil {
				return nil, fmt.Errorf("create cloud with UserAgent(%s) failed with: (%s)", newUserAgent, err)
			}
		default:
			return nil, fmt.Errorf("AzureDisk - invalid option %s in VolumeSnapshotClass", k)
		}
	}

	if azureutils.IsAzureStackCloud(localCloud.Config.Cloud, localCloud.Config.DisableAzureStackCloud) {
		klog.V(2).Info("Use full snapshot instead as Azure Stack does not support incremental snapshot.")
		incremental = false
	}

	if resourceGroup == "" {
		resourceGroup, err = azureutils.GetResourceGroupFromURI(sourceVolumeID)
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

	rerr := localCloud.SnapshotsClient.CreateOrUpdate(ctx, resourceGroup, snapshotName, snapshot)
	if rerr != nil {
		if strings.Contains(rerr.Error().Error(), "existing disk") {
			return nil, status.Error(codes.AlreadyExists, fmt.Sprintf("request snapshot(%s) under rg(%s) already exists, but the SourceVolumeId is different, error details: %v", snapshotName, resourceGroup, rerr.Error()))
		}

		return nil, status.Error(codes.Internal, fmt.Sprintf("create snapshot error: %v", rerr.Error()))
	}
	klog.V(2).Infof("create snapshot(%s) under rg(%s) successfully", snapshotName, resourceGroup)

	snapshotObj, err := c.getSnapshotByID(ctx, resourceGroup, snapshotName, sourceVolumeID)
	if err != nil {
		return nil, err
	}

	return snapshotObj, nil
}

func (c *CloudProvisioner) ListSnapshots(
	ctx context.Context,
	maxEntries int32,
	startingToken string,
	sourceVolumeID string,
	snapshotID string,
	secrets map[string]string) (*v1alpha1.ListSnapshotsResult, error) {
	// SnapshotID is not empty, return snapshot that match the snapshot id.
	if len(snapshotID) != 0 {
		snapshot, err := c.getSnapshotByID(ctx, c.cloud.ResourceGroup, snapshotID, sourceVolumeID)
		if err != nil {
			if strings.Contains(err.Error(), azureconstants.ResourceNotFound) {
				return &v1alpha1.ListSnapshotsResult{}, nil
			}
			return nil, err
		}
		entries := []v1alpha1.Snapshot{*snapshot}

		listSnapshotResp := &v1alpha1.ListSnapshotsResult{
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
		var err error
		start, err = strconv.Atoi(startingToken)
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
	entries := []v1alpha1.Snapshot{}
	for count := 0; start < len(snapshots) && count < totalEntries; start++ {
		if (sourceVolumeID != "" && sourceVolumeID == azureutils.GetSourceVolumeID(&snapshots[start])) || sourceVolumeID == "" {
			snapshotObj, err := azureutils.NewAzureDiskSnapshot(sourceVolumeID, &snapshots[start])
			if err != nil {
				return nil, fmt.Errorf("failed to generate snapshot entry: %v", err)
			}
			entries = append(entries, *snapshotObj)
			count++
		}
	}

	nextToken := len(snapshots)
	if start < len(snapshots) {
		nextToken = start
	}

	listSnapshotResp := &v1alpha1.ListSnapshotsResult{
		Entries:   entries,
		NextToken: strconv.Itoa(nextToken),
	}

	return listSnapshotResp, nil
}

func (c *CloudProvisioner) DeleteSnapshot(
	ctx context.Context,
	snapshotID string,
	secrets map[string]string) error {
	snapshotName, resourceGroup, err := c.GetSnapshotAndResourceNameFromSnapshotID(snapshotID)
	if err != nil {
		return err
	}

	if snapshotName == "" && resourceGroup == "" {
		snapshotName = snapshotID
		resourceGroup = c.cloud.ResourceGroup
	}

	klog.V(2).Infof("begin to delete snapshot(%s) under rg(%s)", snapshotName, resourceGroup)
	rerr := c.cloud.SnapshotsClient.Delete(ctx, resourceGroup, snapshotName)
	if rerr != nil {
		return status.Error(codes.Internal, fmt.Sprintf("delete snapshot error: %v", rerr.Error()))
	}
	klog.V(2).Infof("delete snapshot(%s) under rg(%s) successfully", snapshotName, resourceGroup)

	return nil
}

func (c *CloudProvisioner) CheckDiskExists(ctx context.Context, diskURI string) (*compute.Disk, error) {
	diskName, err := azureutils.GetDiskName(diskURI)
	if err != nil {
		return nil, err
	}

	resourceGroup, err := azureutils.GetResourceGroupFromURI(diskURI)
	if err != nil {
		return nil, err
	}

	if c.isGetDiskThrottled() {
		klog.Warningf("skip checkDiskExists(%s) since it's still in throttling", diskURI)
		return nil, nil
	}

	disk, rerr := c.cloud.DisksClient.Get(ctx, resourceGroup, diskName)
	if rerr != nil {
		if strings.Contains(rerr.RawError.Error(), azureconstants.RateLimited) {
			klog.Warningf("checkDiskExists(%s) is throttled with error: %v", diskURI, rerr.Error())
			c.getDiskThrottlingCache.Set(azureconstants.ThrottlingKey, "")
			return nil, nil
		}
		return nil, rerr.Error()
	}

	return &disk, nil
}

func (c *CloudProvisioner) GetCloud() *azure.Cloud {
	return c.cloud
}

func (c *CloudProvisioner) GetMetricPrefix() string {
	return azureconstants.AzureDiskCSIDriverName
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
		parentResourceGroup, _ := azureutils.GetResourceGroupFromURI(sourceResourceID)
		parentDiskName := path.Base(sourceResourceID)
		return c.GetSourceDiskSize(ctx, parentResourceGroup, parentDiskName, curDepth+1, maxDepth)
	}

	if (*result.DiskProperties).DiskSizeGB == nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("DiskSizeGB for disk (%s) in resourcegroup (%s) is nil", diskName, resourceGroup))
	}
	return (*result.DiskProperties).DiskSizeGB, nil
}

func (c *CloudProvisioner) CheckDiskCapacity(ctx context.Context, resourceGroup, diskName string, requestGiB int) (bool, error) {
	if c.isGetDiskThrottled() {
		klog.Warningf("skip checkDiskCapacity((%s, %s) since it's still in throttling", resourceGroup, diskName)
		return true, nil
	}

	disk, rerr := c.cloud.DisksClient.Get(ctx, resourceGroup, diskName)
	// Because we can not judge the reason of the error. Maybe the disk does not exist.
	// So here we do not handle the error.
	if rerr == nil {
		if !reflect.DeepEqual(disk, compute.Disk{}) && disk.DiskSizeGB != nil && int(*disk.DiskSizeGB) != requestGiB {
			return false, status.Errorf(codes.AlreadyExists, "the request volume already exists, but its capacity(%v) is different from (%v)", *disk.DiskProperties.DiskSizeGB, requestGiB)
		}
	} else {
		if strings.Contains(rerr.RawError.Error(), azureconstants.RateLimited) {
			klog.Warningf("checkDiskCapacity(%s, %s) is throttled with error: %v", resourceGroup, diskName, rerr.Error())
			c.getDiskThrottlingCache.Set(azureconstants.ThrottlingKey, "")
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

func (c *CloudProvisioner) getSnapshotByID(ctx context.Context, resourceGroup string, snapshotName string, sourceVolumeID string) (*v1alpha1.Snapshot, error) {
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

	return azureutils.NewAzureDiskSnapshot(sourceVolumeID, &snapshot)
}

func (c *CloudProvisioner) isGetDiskThrottled() bool {
	cache, err := c.getDiskThrottlingCache.Get(azureconstants.ThrottlingKey, azcache.CacheReadTypeDefault)
	if err != nil {
		klog.Warningf("getDiskThrottlingCache(%s) return with error: %s", azureconstants.ThrottlingKey, err)
		return false
	}
	return cache != nil
}

func (c *CloudProvisioner) validateCreateVolumeRequestParams(
	capacityRange *v1alpha1.CapacityRange,
	volumeCaps []v1alpha1.VolumeCapability,
	params map[string]string) error {
	if capacityRange != nil {
		capacityBytes := capacityRange.RequiredBytes
		volSizeBytes := int64(capacityBytes)
		requestGiB := int(volumehelper.RoundUpGiB(volSizeBytes))
		if requestGiB < azureconstants.MinimumDiskSizeGiB {
			requestGiB = azureconstants.MinimumDiskSizeGiB
		}

		maxVolSize := int(volumehelper.RoundUpGiB(capacityRange.LimitBytes))
		if (maxVolSize > 0) && (maxVolSize < requestGiB) {
			return status.Error(codes.InvalidArgument, "After round-up, volume size exceeds the limit specified")
		}
	}

	var maxShares int
	var err error
	for k, v := range params {
		if strings.EqualFold(azureconstants.MaxSharesField, k) {
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
				mode != v1alpha1.VolumeCapabilityAccessModeSingleNodeReaderOnly &&
				mode != v1alpha1.VolumeCapabilityAccessModeSingleNodeSingleWriter &&
				mode != v1alpha1.VolumeCapabilityAccessModeSingleNodeMultiWriter {
				return status.Error(codes.InvalidArgument, fmt.Sprintf("Volume capability(%v) not supported", mode))
			}
		}
	}

	return nil
}

// listVolumesInCluster is a helper function for ListVolumes used for when there is an available kubeclient
func (c *CloudProvisioner) listVolumesInCluster(ctx context.Context, start, maxEntries int) (*v1alpha1.ListVolumesResult, error) {
	kubeClient := c.cloud.KubeClient
	pvList, err := kubeClient.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "ListVolumes failed while fetching PersistentVolumes List with error: %v", err.Error())
	}

	// get all resource groups and put them into a sorted slice
	rgMap := make(map[string]bool)
	volSet := make(map[string]bool)
	for _, pv := range pvList.Items {
		if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == azureconstants.DefaultDriverName {
			diskURI := pv.Spec.CSI.VolumeHandle
			if err := azureutils.IsValidDiskURI(diskURI); err != nil {
				klog.Warningf("invalid disk uri (%s) with error(%v)", diskURI, err)
				continue
			}
			rg, err := azureutils.GetResourceGroupFromURI(diskURI)
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
	entries := []v1alpha1.VolumeEntry{}
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

	listVolumesResp := &v1alpha1.ListVolumesResult{
		Entries:   entries,
		NextToken: nextTokenString,
	}

	return listVolumesResp, nil
}

// listVolumesInNodeResourceGroup is a helper function for ListVolumes used for when there is no available kubeclient
func (c *CloudProvisioner) listVolumesInNodeResourceGroup(ctx context.Context, start, maxEntries int) (*v1alpha1.ListVolumesResult, error) {
	entries := []v1alpha1.VolumeEntry{}
	listStatus := c.listVolumesByResourceGroup(ctx, c.cloud.ResourceGroup, entries, start, maxEntries, nil)
	if listStatus.err != nil {
		return nil, listStatus.err
	}

	nextTokenString := ""
	if !listStatus.isCompleteRun {
		nextTokenString = strconv.Itoa(start + listStatus.numVisited)
	}

	listVolumesResp := &v1alpha1.ListVolumesResult{
		Entries:   listStatus.entries,
		NextToken: nextTokenString,
	}

	return listVolumesResp, nil
}

// listVolumesByResourceGroup is a helper function that updates the ListVolumeResponse_Entry slice and returns number of total visited volumes, number of volumes that needs to be visited and an error if found
func (c *CloudProvisioner) listVolumesByResourceGroup(ctx context.Context, resourceGroup string, entries []v1alpha1.VolumeEntry, start, maxEntries int, volSet map[string]bool) listVolumeStatus {
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

			entries = append(entries, v1alpha1.VolumeEntry{
				Details: &v1alpha1.VolumeDetails{
					VolumeID: *disk.ID,
				},
				Status: &v1alpha1.VolumeStatus{
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
		if zone, exists := topologySegments[azureconstants.WellKnownTopologyKey]; exists {
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
		if zone, exists := topologySegments[azureconstants.WellKnownTopologyKey]; exists {
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
