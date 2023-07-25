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
	"net/http"
	"os"
	"path"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	armcompute "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/optimization"
	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/workflow"
	azcache "sigs.k8s.io/cloud-provider-azure/pkg/cache"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

var (
	topologyKeyStr   = "N/A"
	diskCachingLimit = 4096 // GiB
)

type CloudAttachResult struct {
	publishContext      map[string]string
	attachResultChannel chan error
}

func NewCloudAttachResult() CloudAttachResult {
	return CloudAttachResult{attachResultChannel: make(chan error, 1)}
}

func (c *CloudAttachResult) SetPublishContext(publishContext map[string]string) {
	c.publishContext = publishContext
}

func (c *CloudAttachResult) PublishContext() map[string]string {
	return c.publishContext
}

func (c *CloudAttachResult) ResultChannel() chan error {
	return c.attachResultChannel
}

type CloudProvisioner struct {
	cloud                   *azureutils.Cloud
	kubeClient              kubernetes.Interface
	config                  *azdiskv1beta2.AzDiskDriverConfiguration
	enableOnlineDiskResize  bool
	perfOptimizationEnabled bool
	enableAsyncAttach       bool
	// a timed cache GetDisk throttling
	getDiskThrottlingCache *azcache.TimedCache
}

// listVolumeStatus explains the return status of `listVolumesByResourceGroup`
type listVolumeStatus struct {
	numVisited    int  // the number of iterated azure disks
	isCompleteRun bool // isCompleteRun is flagged true if the function iterated through all azure disks
	entries       []azdiskv1beta2.VolumeEntry
	err           error
}

func NewCloudProvisioner(
	ctx context.Context,
	kubeClient kubernetes.Interface,
	config *azdiskv1beta2.AzDiskDriverConfiguration,
	topologyKey string,
	userAgent string,
) (*CloudProvisioner, error) {
	azCloud, err := azureutils.GetCloudProviderFromClient(
		ctx,
		kubeClient,
		config,
		userAgent)
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
		cloud:                  azCloud,
		kubeClient:             kubeClient,
		config:                 config,
		getDiskThrottlingCache: cache,
	}, nil
}

func (c *CloudProvisioner) GetSubscriptionID() string {
	return c.cloud.SubscriptionID
}

func (c *CloudProvisioner) GetResourceGroup() string {
	return c.cloud.ResourceGroup
}

func (c *CloudProvisioner) GetLocation() string {
	return c.cloud.Location
}

func (c *CloudProvisioner) GetFailureDomain(ctx context.Context, nodeID string) (string, error) {
	var zone cloudprovider.Zone
	var err error

	if runtime.GOOS == "windows" {
		zone, err = c.cloud.GetZoneByNodeName(ctx, nodeID)
	} else {
		hostname, err := os.Hostname()
		if err != nil {
			zone = cloudprovider.Zone{}
			err = fmt.Errorf("failure getting hostname from kernel")
		} else {
			zone, err = c.cloud.GetZoneByNodeName(ctx, strings.ToLower(hostname))
		}
	}

	if err != nil {
		return "", err
	}

	return zone.FailureDomain, nil
}

func (c *CloudProvisioner) GetInstanceType(ctx context.Context, nodeID string) (string, error) {
	var err error

	if runtime.GOOS == "windows" {
		resp, err := c.cloud.VMClient.Get(ctx, c.cloud.ResourceGroup, nodeID, nil)
		if err == nil && resp.VirtualMachine.Properties != nil && resp.VirtualMachine.Properties.HardwareProfile != nil &&
		resp.VirtualMachine.Properties.HardwareProfile.VMSize != nil {
			return string(*resp.VirtualMachine.Properties.HardwareProfile.VMSize), nil
		}

		klog.Warningf("failed to get instance type from metadata for node %s: %v", nodeID, err)
	} else {
		klog.Infof("value for instance: %+v, %b", c, true)
		resp, err := c.cloud.VMClient.Get(ctx, c.cloud.ResourceGroup, nodeID, nil)
		if err != nil {
			return "", err
		}

		if resp.VirtualMachine.Properties != nil && resp.VirtualMachine.Properties.AvailabilitySet != nil {
			asName, err := GetLastSegment(*resp.VirtualMachine.Properties.AvailabilitySet.ID, "/")
			if err != nil {
				return "", fmt.Errorf("failed to get asName from availability set ID: %v", err)
			}

			resp, err := c.cloud.ASClient.Get(ctx, c.cloud.ResourceGroup, asName, nil)
			if err != nil {
				return "", err
			}

			if resp.AvailabilitySet.SKU != nil && resp.AvailabilitySet.SKU.Name != nil {
				return *resp.AvailabilitySet.SKU.Name, nil
			}

		} else if resp.VirtualMachine.Properties != nil && resp.VirtualMachine.Properties.VirtualMachineScaleSet != nil {
			ssName, err := GetLastSegment(*resp.VirtualMachine.Properties.VirtualMachineScaleSet.ID, "/")
			if err != nil {
				return "", fmt.Errorf("failed to get ssName from scale set ID: %v", err)
			}

			resp, err := c.cloud.VMSSClient.Get(ctx, c.cloud.ResourceGroup, ssName, nil)
			if err != nil {
				return "", err
			}

			if resp.VirtualMachineScaleSet.SKU != nil && resp.VirtualMachineScaleSet.SKU.Name != nil {
				return *resp.VirtualMachineScaleSet.SKU.Name, nil
			}

		}

		klog.Warningf("failed to get instances from cloud provider: %b, %b", c == nil, true)
	}

	if err == nil {
		err = fmt.Errorf("failed to get instance type for node %s", nodeID)
	}

	return "", err
}

func (c *CloudProvisioner) CreateVolume(
	ctx context.Context,
	volumeName string,
	capacityRange *azdiskv1beta2.CapacityRange,
	volumeCapabilities []azdiskv1beta2.VolumeCapability,
	parameters map[string]string,
	secrets map[string]string,
	volumeContentSource *azdiskv1beta2.ContentVolumeSource,
	accessibilityRequirements *azdiskv1beta2.TopologyRequirement) (*azdiskv1beta2.AzVolumeStatusDetail, error) {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	var diskParams azureutils.ManagedDiskParameters
	diskParams, err = azureutils.ParseDiskParameters(parameters, azureutils.StrictValidation)
	if err != nil {
		err = status.Errorf(codes.InvalidArgument, "Failed parsing disk parameters: %v", err)
		return nil, err
	}

	if err = c.validateCreateVolumeRequestParams(capacityRange, volumeCapabilities, diskParams); err != nil {
		return nil, err
	}

	if diskParams.Location == "" {
		diskParams.Location = c.cloud.Location
	}

	localCloud := c.cloud
	isAdvancedPerfProfile := strings.EqualFold(diskParams.PerfProfile, azureconstants.PerfProfileAdvanced)
	// If perfProfile is set to advanced and no/invalid device settings are provided, fail the request
	if c.isPerfOptimizationEnabled() && isAdvancedPerfProfile {
		if err := optimization.AreDeviceSettingsValid(azureconstants.DummyBlockDevicePathLinux, diskParams.DeviceSettings); err != nil {
			return nil, err
		}
	}

	if diskParams.DiskName == "" {
		diskParams.DiskName = volumeName
	}
	diskParams.DiskName = azureutils.CreateValidDiskName(diskParams.DiskName, true)

	if diskParams.ResourceGroup == "" {
		diskParams.ResourceGroup = c.cloud.ResourceGroup
	}

	if diskParams.UserAgent != "" {
		localCloud, err = azureutils.GetCloudProviderFromClient(
			ctx,
			c.kubeClient,
			c.config,
			diskParams.UserAgent)
		if err != nil {
			err = status.Errorf(codes.Internal, "create cloud with UserAgent(%s) failed with: (%s)", diskParams.UserAgent, err)
			return nil, err
		}
	}
	// normalize values
	var skuName armcompute.DiskStorageAccountTypes
	skuName, err = azureutils.NormalizeStorageAccountType(diskParams.AccountType, localCloud.Config.Cloud, localCloud.Config.DisableAzureStackCloud)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if _, err = azureutils.NormalizeCachingMode(diskParams.CachingMode, diskParams.MaxShares); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if err = azureutils.ValidateDiskEncryptionType(diskParams.DiskEncryptionType); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	var networkAccessPolicy armcompute.NetworkAccessPolicy
	networkAccessPolicy, err = azureutils.NormalizeNetworkAccessPolicy(diskParams.NetworkAccessPolicy)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	selectedAvailabilityZone := pickAvailabilityZone(accessibilityRequirements, c.cloud.Location)
	accessibleTopology := []azdiskv1beta2.Topology{}
	if skuName == armcompute.DiskStorageAccountTypesStandardSSDZRS || skuName == armcompute.DiskStorageAccountTypesPremiumZRS {
		w.Logger().V(2).Infof("diskZone(%s) is reset as empty since disk(%s) is ZRS(%s)", selectedAvailabilityZone, diskParams.DiskName, skuName)
		selectedAvailabilityZone = ""
		// make volume scheduled on all 3 availability zones
		for i := 1; i <= 3; i++ {
			topology := azdiskv1beta2.Topology{
				Segments: map[string]string{topologyKeyStr: fmt.Sprintf("%s-%d", c.cloud.Location, i)},
			}
			accessibleTopology = append(accessibleTopology, topology)
		}
		// make volume scheduled on all non-zone nodes
		topology := azdiskv1beta2.Topology{
			Segments: map[string]string{topologyKeyStr: ""},
		}
		accessibleTopology = append(accessibleTopology, topology)
	} else {
		accessibleTopology = []azdiskv1beta2.Topology{
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

	if ok, derr := c.CheckDiskCapacity(ctx, diskParams.ResourceGroup, diskParams.DiskName, requestGiB); !ok {
		err = derr
		return nil, err
	}

	klog.V(2).Infof("begin to create disk(%s) account type(%s) rg(%s) location(%s) size(%d) selectedAvailabilityZone(%v) maxShares(%d)",
		diskParams.DiskName, skuName, diskParams.ResourceGroup, diskParams.Location, requestGiB, selectedAvailabilityZone, diskParams.MaxShares)

	if strings.EqualFold(diskParams.WriteAcceleratorEnabled, azureconstants.TrueValue) {
		diskParams.Tags[azure.WriteAcceleratorEnabled] = azureconstants.TrueValue
	}
	sourceID := ""
	sourceType := ""
	contentSource := &azdiskv1beta2.ContentVolumeSource{}
	if volumeContentSource != nil {
		sourceID = volumeContentSource.ContentSourceID
		contentSource.ContentSource = volumeContentSource.ContentSource
		contentSource.ContentSourceID = volumeContentSource.ContentSourceID
		sourceType = azureconstants.SourceSnapshot
		if volumeContentSource.ContentSource == azdiskv1beta2.ContentVolumeSourceTypeVolume {
			sourceType = azureconstants.SourceVolume

			ctx, cancel := context.WithCancel(ctx)
			if sourceGiB, _ := c.GetSourceDiskSize(ctx, diskParams.ResourceGroup, path.Base(sourceID), 0, azureconstants.SourceDiskSearchMaxDepth); sourceGiB != nil && *sourceGiB < int32(requestGiB) {
				diskParams.VolumeContext[azureconstants.ResizeRequired] = strconv.FormatBool(true)
			}
			cancel()
		}
	}

	if skuName == armcompute.DiskStorageAccountTypesUltraSSDLRS {
		if diskParams.DiskIOPSReadWrite == "" && diskParams.DiskMBPSReadWrite == "" {
			// set default DiskIOPSReadWrite, DiskMBPSReadWrite per request size
			diskParams.DiskIOPSReadWrite = strconv.Itoa(azureutils.GetDefaultDiskIOPSReadWrite(requestGiB))
			diskParams.DiskMBPSReadWrite = strconv.Itoa(azureutils.GetDefaultDiskMBPSReadWrite(requestGiB))
			klog.V(2).Infof("set default DiskIOPSReadWrite as %s, DiskMBPSReadWrite as %s on disk(%s)", diskParams.DiskIOPSReadWrite, diskParams.DiskMBPSReadWrite, diskParams.DiskName)
		}
	}

	diskParams.VolumeContext[azureconstants.RequestedSizeGib] = strconv.Itoa(requestGiB)

	diskThrottled := c.isGetDiskThrottled()

	creationData, err := azureutils.GetValidCreationData(c.cloud.SubscriptionID, diskParams.ResourceGroup, sourceID, sourceType)
	if err != nil {
		klog.Warningf("failed to get creation data: %v", err)
	}

	diskSizeGB := int32(requestGiB)
	maxShares := int32(diskParams.MaxShares)

	iops := 0
	if diskParams.DiskIOPSReadWrite != "" {
		iops, err = strconv.Atoi(diskParams.DiskIOPSReadWrite)
		if err != nil {
			return nil, fmt.Errorf("failed to parse DiskIOPSReadWrite: %v", err)
		}

	}

	mbps := 0
	if diskParams.DiskMBPSReadWrite != "" {
		mbps, err = strconv.Atoi(diskParams.DiskMBPSReadWrite)
		if err != nil {
			return nil, fmt.Errorf("failed to parse DiskMBPSReadWrite: %v", err)
		}

	}

	tags := make(map[string]*string)
	azureDDTag := "kubernetes-azure-dd"
	tags["k8s-azure-created-by"] = &azureDDTag
	for k, v := range diskParams.Tags {
		key := strings.Replace(k, "/", "-", -1)
		value := strings.Replace(v, "/", "-", -1)
		tags[key] = &value
	}

	var createZones []*string
	if len(selectedAvailabilityZone) > 0 {
		var requestedZone string
		isAvailabilityZone := strings.HasPrefix(selectedAvailabilityZone, fmt.Sprintf("%s-", localCloud.Location))
		if isAvailabilityZone {
			requestedZone = strings.TrimPrefix(selectedAvailabilityZone, fmt.Sprintf("%s-", localCloud.Location))
			createZones = append(createZones, &requestedZone)
		}
	}

	encryptionType := armcompute.EncryptionType(diskParams.DiskEncryptionType)

	disk := armcompute.Disk{
		Location: &diskParams.Location,
		Properties: &armcompute.DiskProperties{
			CreationData:      &creationData,
			DiskSizeGB:        &diskSizeGB,
			BurstingEnabled:   diskParams.EnableBursting,
			DiskIOPSReadWrite: pointer.Int64(int64(iops)),
			DiskMBpsReadWrite: pointer.Int64(int64(mbps)),
		},
		SKU: &armcompute.DiskSKU{
			Name: &skuName,
		},
		Tags: tags,
	}

	if diskParams.DiskEncryptionSetID != "" {
		disk.Properties.Encryption = &armcompute.Encryption{
			DiskEncryptionSetID: &diskParams.DiskEncryptionSetID,
			Type:                &encryptionType,
		}
	}

	if maxShares > 1 {
		disk.Properties.MaxShares = &maxShares
	}

	if len(createZones) > 0 {
		disk.Zones = createZones
	}

	// Azure Stack Cloud does not support NetworkAccessPolicy
	if !azureutils.IsAzureStackCloud(localCloud.Config.Cloud, localCloud.Config.DisableAzureStackCloud) {
		disk.Properties.NetworkAccessPolicy = &networkAccessPolicy
		if diskParams.DiskAccessID != "" {
			disk.Properties.DiskAccessID = &diskParams.DiskAccessID
		}
	}

	poller, err := c.cloud.DisksClient.BeginCreateOrUpdate(ctx, diskParams.ResourceGroup, diskParams.DiskName, disk, nil)

	if err != nil {
		return nil, fmt.Errorf("failed to finish the request: %v", err)
	}

	resp, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to pull the result: %v", err)
	}

	diskID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/disks/%s", c.cloud.SubscriptionID, diskParams.ResourceGroup, diskParams.DiskName)
	if *resp.Disk.ID != "" {
		diskID = *resp.Disk.ID
	}

	if diskThrottled {
		klog.Warningf("azureDisk - GetDisk(%s, StorageAccountType:%s) is throttled, unable to confirm provisioningState in poll process", diskParams.DiskName, skuName)
	} else {
		if disk.Properties.ProvisioningState != nil && *disk.ID != "" {
			diskID = *disk.ID
		}
	}

	w.Logger().V(2).Infof("create disk(%s) account type(%s) rg(%s) location(%s) size(%d) tags(%s) successfully", diskParams.DiskName, skuName, diskParams.ResourceGroup, diskParams.Location, requestGiB, diskParams.Tags)

	return &azdiskv1beta2.AzVolumeStatusDetail{
		VolumeID:           diskID,
		CapacityBytes:      volSizeBytes,
		VolumeContext:      diskParams.VolumeContext,
		ContentSource:      contentSource,
		AccessibleTopology: accessibleTopology,
	}, nil
}

func (c *CloudProvisioner) DeleteVolume(
	ctx context.Context,
	volumeID string,
	secrets map[string]string) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	if err = azureutils.IsValidDiskURI(volumeID); err != nil {
		w.Logger().Errorf(err, "validateDiskURI(%s) in DeleteVolume failed with error", volumeID)
		return nil
	}

	disksClient := c.cloud.DisksClient
	diskName := path.Base(volumeID)

	resp, err := disksClient.Get(ctx, c.cloud.ResourceGroup, diskName, nil)
	if err != nil {
		klog.Infof("error: %+v", err)
		rerr := err.(*azcore.ResponseError)
		if rerr.StatusCode == http.StatusNotFound {
			klog.Infof("disk %+v is already deleted", volumeID)
			return nil
		}
	}

	if resp.Disk.ManagedBy != nil {
		return fmt.Errorf("disk %+v is in attached state", volumeID)
	}

	poller, err := disksClient.BeginDelete(ctx, c.cloud.ResourceGroup, diskName, nil)
	if err != nil {
		return fmt.Errorf("failed to finish the request: %v", err)
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to pull the result: %v", err)
	}

	return err
}

func (c *CloudProvisioner) ListVolumes(
	ctx context.Context,
	maxEntries int32,
	startingToken string) (*azdiskv1beta2.ListVolumesResult, error) {
	start, _ := strconv.Atoi(startingToken)
	kubeClient := c.cloud.KubeClient
	if kubeClient != nil && kubeClient.CoreV1() != nil && kubeClient.CoreV1().PersistentVolumes() != nil {
		klog.V(6).Infof("List Volumes in Cluster:")
		return c.listVolumesInCluster(ctx, start, int(maxEntries))
	}
	klog.V(6).Infof("List Volumes in Node Resource Group: %s", c.cloud.ResourceGroup)
	return c.listVolumesInNodeResourceGroup(ctx, start, int(maxEntries))
}

// PublishVolume calls AttachDisk asynchronously and returns early lun assignment value and a channel for the async attach results.
func (c *CloudProvisioner) PublishVolume(
	ctx context.Context,
	volumeID string,
	nodeID string,
	volumeContext map[string]string) (attachResult CloudAttachResult) {
	var err error
	var waitForCloud bool
	attachResult = NewCloudAttachResult()
	defer func() {
		if !waitForCloud {
			attachResult.ResultChannel() <- err
			close(attachResult.ResultChannel())
		}
	}()

	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	var disk *armcompute.Disk
	disk, err = c.CheckDiskExists(ctx, volumeID)
	if err != nil {
		err = status.Errorf(codes.NotFound, "Volume not found, failed with error: %v", err)
		return
	}

	nodeName := types.NodeName(nodeID)

	var diskName string
	diskName, err = azureutils.GetDiskName(volumeID)
	if err != nil {
		err = status.Error(codes.Internal, err.Error())
		return
	}

	vmssVMClient := c.cloud.VMSSVMClient
	providerID := ""
	if disk.ManagedBy != nil {
		providerID = *disk.ManagedBy
	}

	var vmState *string
	lun := int32(-1)
	var attachedNode string
	var scaleSetName string
	var instanceID string

	if providerID != "" {

		attachedNode, err = c.GetNodeNameFromProviderID(ctx, providerID)
		if err != nil {
			err = fmt.Errorf("failed to get node name from providerID: %v", err)
			return 
		}
		var fullScaleSetName string
		instanceID, fullScaleSetName, err = GetInstanceIDAndFullScaleSetNameFromProviderID(providerID)
		if err != nil {
			err = fmt.Errorf("failed to get instanceID and full scaleSet name from providerID: %v", err)
			return
		}

		scaleSetName, err = GetLastSegment(fullScaleSetName, "/")
		if err != nil {
			err = fmt.Errorf("failed to extract scaleset name: %v", err)
			return
		}

		vmEntry, err := c.cloud.GetVMSSVM(ctx, attachedNode)
		var storageProfile *armcompute.StorageProfile
		if vmEntry != nil && vmEntry.VM != nil && vmEntry.VM.Properties != nil && vmEntry.VM.Properties.StorageProfile != nil {
			storageProfile = vmEntry.VM.Properties.StorageProfile
		} else {
			err = fmt.Errorf("storage profile on node %+v not found", string(nodeName))
			return
		}

		if vmEntry != nil && vmEntry.VM != nil && vmEntry.VM.Properties != nil && vmEntry.VM.Properties.ProvisioningState != nil {
			vmState = vmEntry.VM.Properties.ProvisioningState
		} else {
			err = fmt.Errorf("provisioning state on node %+v not found", string(nodeName))
			return
		}

		lun, err = GetDiskLun(diskName, volumeID, storageProfile.DataDisks)
		if err != nil {
			err = fmt.Errorf("failed to find disk lun: %v", err)
			return
		}
	}

	w.Logger().V(2).Infof("Initiating attaching volume %q to node %q.", volumeID, nodeName)

	// disk already attached to nodeName
	if strings.EqualFold(string(nodeName), strings.ToLower(attachedNode)) {
		if vmState != nil && strings.ToLower(*vmState) == "failed" {
			w.Logger().Infof("VM(%q) is in failed state, update VM first", nodeName)

			poller, err := vmssVMClient.BeginUpdate(ctx, c.cloud.ResourceGroup, scaleSetName, instanceID, armcompute.VirtualMachineScaleSetVM{
				Name:       to.Ptr(string(nodeName)),
				InstanceID: &instanceID,
			}, nil)
			if err != nil {
				err = fmt.Errorf("failed to finish the request: %v", err)
				return
			}
			_, err = poller.PollUntilDone(ctx, nil)
			if err != nil {
				err = fmt.Errorf("failed to pull the result: %v", err)
				return
			}
		}
		// Volume is already attached to node.
		w.Logger().V(2).Infof("Attach operation is successful. volume %q is already attached to node %q.", volumeID, nodeName)
	} else {
		w.Logger().V(2).Infof("Trying to attach volume %q to node %q.", volumeID, nodeName)
		var cachingMode armcompute.CachingTypes
		if cachingMode, err = azureutils.GetCachingMode(volumeContext); err != nil {
			err = status.Error(codes.Internal, err.Error())
			return 
		}

		if disk.Properties.DiskSizeGB != nil && *disk.Properties.DiskSizeGB >= int32(diskCachingLimit) && cachingMode != armcompute.CachingTypesNone {
			// Disk Caching is not supported for disks 4 TiB and larger
			cachingMode = armcompute.CachingTypesNone
			klog.Warningf("size of disk(%s) is %dGB which is bigger than limit(%dGB), set cacheMode as None",
				volumeID, *disk.Properties.DiskSizeGB, diskCachingLimit)
		}

		lunCh := make(chan int32, 1)
		resultLunCh := make(chan int32, 1)
		ctx = context.WithValue(ctx, azure.LunChannelContextKey, lunCh)
		waitForCloud = true
		go func() {
			var resultErr error
			var resultLun int32
			ctx, w := workflow.New(ctx)
			defer func() { 
				attachResult.ResultChannel() <- resultErr
				close(attachResult.ResultChannel())

				w.Finish(resultErr) 
			}()

			scaleSetName, instanceID, resultErr := GetInstanceIDAndScaleSetNameFromNodeName(string(nodeName))
			if resultErr != nil {
				resultErr = fmt.Errorf("failed to get instance id and vmss name from node name: %v", resultErr)
				return
			}

			vmEntry, resultErr := c.cloud.GetVMSSVM(ctx, string(nodeName))
			if resultErr != nil {
				resultErr = fmt.Errorf("failed to get vm from cache: %v", resultErr)
				return
			}

			var storageProfile *armcompute.StorageProfile
			if vmEntry != nil && vmEntry.VM != nil && vmEntry.VM.Properties != nil && vmEntry.VM.Properties.StorageProfile != nil {
				storageProfile = vmEntry.VM.Properties.StorageProfile
			} else {
				resultErr = fmt.Errorf("storage profile on node %+v not found", string(nodeName))
				return
			}
			disks := storageProfile.DataDisks

			attached := false
			usedLuns := make([]bool, len(disks)+1)
			count := 0
			for _, disk := range disks {
				count++
				if disk.ManagedDisk != nil && strings.EqualFold(*disk.ManagedDisk.ID, strings.ToLower(volumeID)) && disk.Lun != nil {
					if *disk.Lun == lun {
						attached = true
						break
					} else {
						resultErr = fmt.Errorf("disk(%s) already attached to node(%s) on LUN(%d), but target LUN is %d", volumeID, nodeName, *disk.Lun, lun)
					}
				}
				usedLuns[*disk.Lun] = true
			}

			writeAcceleratorEnabled := false
			if v, ok := disk.Tags["writeacceleratorenabled"]; ok {
				if v != nil && strings.EqualFold(*v, "true") {
					writeAcceleratorEnabled = true
				}
			}

			diskEncryptionSetID := ""
			if disk.Properties != nil && disk.Properties.Encryption != nil && disk.Properties.Encryption.DiskEncryptionSetID != nil {
				diskEncryptionSetID = *disk.Properties.Encryption.DiskEncryptionSetID
			}

			if attached {
				klog.V(2).Infof("azureDisk - disk(%s) already attached to node(%s) on LUN(%d)", volumeID, nodeName, lun)
			} else {
				managedDisk := &armcompute.ManagedDiskParameters{ID: &volumeID}
				if diskEncryptionSetID == "" {
					if storageProfile.OSDisk != nil &&
						storageProfile.OSDisk.ManagedDisk != nil &&
						storageProfile.OSDisk.ManagedDisk.DiskEncryptionSet != nil &&
						storageProfile.OSDisk.ManagedDisk.DiskEncryptionSet.ID != nil {
						// set diskEncryptionSet as value of os disk by default
						diskEncryptionSetID = *storageProfile.OSDisk.ManagedDisk.DiskEncryptionSet.ID
					}
				}
				if diskEncryptionSetID != "" {
					managedDisk.DiskEncryptionSet = &armcompute.DiskEncryptionSetParameters{ID: &diskEncryptionSetID}
				}
				newDisk := &armcompute.DataDisk{
					Name:                    &diskName,
					Caching:                 &cachingMode,
					CreateOption:            to.Ptr(armcompute.DiskCreateOptionTypesAttach),
					ManagedDisk:             managedDisk,
					WriteAcceleratorEnabled: pointer.Bool(writeAcceleratorEnabled),
				}
				if lun != -1 {
					newDisk.Lun = &lun
				} else {
					for index, used := range usedLuns {
						if !used {
							newDisk.Lun = to.Ptr(int32(index))
							break
						}
					}
				}
				disks = append(disks, newDisk)
			}

			newVM := armcompute.VirtualMachineScaleSetVM{
				Properties: &armcompute.VirtualMachineScaleSetVMProperties{
					StorageProfile: &armcompute.StorageProfile{
						DataDisks: disks,
					},
				},
			}

			poller, resultErr := vmssVMClient.BeginUpdate(ctx, c.cloud.ResourceGroup, scaleSetName, instanceID, newVM, nil)
			if resultErr != nil {
				resultErr = fmt.Errorf("failed to finish the request: %v", resultErr)
				return
			}
			_, resultErr = poller.PollUntilDone(ctx, nil)
			if resultErr != nil {
				resultErr = fmt.Errorf("failed to pull the result: %v", resultErr)
				return
			} else {
				w.Logger().V(2).Infof("attach operation successful: volume %q attached to node %q.", volumeID, nodeName)
			}

			resultLun, resultErr = GetDiskLun(diskName, volumeID, disks)
			if resultErr != nil {
				resultErr = fmt.Errorf("failed to find disk lun: %v", resultErr)
				return
			}

			resp, resultErr := vmssVMClient.Get(ctx, c.cloud.ResourceGroup, scaleSetName, instanceID, nil)
			if resultErr != nil {
				resultErr = fmt.Errorf("failed to get vm: %v", resultErr)
				return
			}

			// update the cache
			c.cloud.VMSSVMCache.SetVMSSAndVM(*vmEntry.VMSSName, *vmEntry.Name, *vmEntry.InstanceID, *vmEntry.ResourceGroup, &resp.VirtualMachineScaleSetVM)

			resultLunCh <- resultLun
			close(resultLunCh)
		}()

		select {
		case lun = <-lunCh:
		case lun = <-resultLunCh:
		}
	}

	publishContext := map[string]string{"LUN": strconv.Itoa(int(lun))}
	attachResult.SetPublishContext(publishContext)
	return attachResult
}

func (c *CloudProvisioner) UnpublishVolume(
	ctx context.Context,
	volumeID string,
	nodeID string) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	nodeName := types.NodeName(nodeID)

	var diskName string
	diskName, err = azureutils.GetDiskName(volumeID)
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}

	w.Logger().V(2).Infof("Trying to detach volume %s from node %s", volumeID, nodeID)

	scaleSetName, instanceID, err := GetInstanceIDAndScaleSetNameFromNodeName(string(nodeName))
	if err != nil {
		return err
	}

	vmssVmClient := c.cloud.VMSSVMClient
	vmEntry, err := c.cloud.GetVMSSVM(ctx, string(nodeName))
	if err != nil {
		return fmt.Errorf("failed to get vm from cache: %v", err)
	}

	var storageProfile *armcompute.StorageProfile
	if vmEntry != nil && vmEntry.VM != nil && vmEntry.VM.Properties != nil && vmEntry.VM.Properties.StorageProfile != nil {
		storageProfile = vmEntry.VM.Properties.StorageProfile
	} else {
		return fmt.Errorf("storage profile on node %+v not found", string(nodeName))
	}

	var disks []*armcompute.DataDisk
	disks = make([]*armcompute.DataDisk, len(storageProfile.DataDisks))
	copy(disks, storageProfile.DataDisks)

	found := false
	for i, disk := range disks {
		if disk.Lun != nil && (disk.Name != nil && diskName != "" && strings.EqualFold(*disk.Name, diskName)) ||
			(disk.Vhd != nil && disk.Vhd.URI != nil && volumeID != "" && strings.EqualFold(*disk.Vhd.URI, volumeID)) ||
			(disk.ManagedDisk != nil && volumeID != "" && strings.EqualFold(*disk.ManagedDisk.ID, volumeID)) {
			// found the disk
			klog.V(2).Infof("azureDisk - detach disk: name %s uri %s", diskName, volumeID)
			disks[i].ToBeDetached = pointer.Bool(true)
			found = true
		}
	}

	var newDisks []*armcompute.DataDisk

	if !found {
		klog.Warningf("to be detached disk(%s) on node(%s) not found", diskName, string(nodeName))
	} else {
		for _, disk := range disks {
			// if disk.ToBeDetached is true
			if disk.ToBeDetached == nil || (disk.ToBeDetached != nil && *disk.ToBeDetached == false) {
				newDisks = append(newDisks, disk)
			}
		}
	}

	newVM := armcompute.VirtualMachineScaleSetVM{
		Properties: &armcompute.VirtualMachineScaleSetVMProperties{
			StorageProfile: &armcompute.StorageProfile{
				DataDisks: newDisks,
			},
		},
	}

	poller, err := vmssVmClient.BeginUpdate(ctx, c.cloud.ResourceGroup, scaleSetName, instanceID, newVM, nil)
	if err != nil {
		return fmt.Errorf("failed to finish request: %v", err)
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		err = status.Errorf(codes.Internal, "could not detach volume %q from node %q: %v", volumeID, nodeID, err)
		return err
	}

	resp, err := vmssVmClient.Get(ctx, c.cloud.ResourceGroup, scaleSetName, instanceID, nil)
	if err != nil {
		return fmt.Errorf("failed to get vm: %v", err)
	}

	// update the cache
	c.cloud.VMSSVMCache.SetVMSSAndVM(*vmEntry.VMSSName, *vmEntry.Name, *vmEntry.InstanceID, *vmEntry.ResourceGroup, &resp.VirtualMachineScaleSetVM)

	return nil
}

func (c *CloudProvisioner) ExpandVolume(
	ctx context.Context,
	volumeID string,
	capacityRange *azdiskv1beta2.CapacityRange,
	secrets map[string]string) (*azdiskv1beta2.AzVolumeStatusDetail, error) {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	requestSize := *resource.NewQuantity(capacityRange.RequiredBytes, resource.BinarySI)

	if err = azureutils.IsValidDiskURI(volumeID); err != nil {
		err = status.Errorf(codes.InvalidArgument, "disk URI(%s) is not valid: %v", volumeID, err)
		return nil, err
	}

	var diskName string
	diskName, err = azureutils.GetDiskName(volumeID)
	if err != nil {
		err = status.Errorf(codes.Internal, "could not get disk name from diskURI(%s) with error(%v)", volumeID, err)
		return nil, err
	}

	var resourceGroup string
	resourceGroup, err = azureutils.GetResourceGroupFromURI(volumeID)
	if err != nil {
		err = status.Errorf(codes.Internal, "could not get resource group from diskURI(%s) with error(%v)", volumeID, err)
		return nil, err
	}

	result, rerr := c.cloud.DisksClient.Get(ctx, resourceGroup, diskName, nil)
	if rerr != nil {
		err = status.Errorf(codes.Internal, "could not get the disk(%s) under rg(%s) with error(%v)", diskName, resourceGroup, rerr.Error())
		return nil, err
	}
	if result.Disk.Properties.DiskSizeGB == nil {
		err = status.Errorf(codes.Internal, "could not get size of the disk(%s)", diskName)
		return nil, err
	}

	w.Logger().V(2).Infof("begin to expand azure disk(%s) with new size(%d)", volumeID, requestSize.Value())

	poller, err := c.cloud.DisksClient.BeginUpdate(ctx, c.cloud.ResourceGroup, diskName, armcompute.DiskUpdate{
		Properties: &armcompute.DiskUpdateProperties{
			DiskSizeGB: pointer.Int32(int32(requestSize.Value())),
		},
	}, nil)
	if err != nil {
		err = status.Errorf(codes.Internal, "failed to resize disk(%s) with error(%v)", volumeID, err)
		return nil, err
	}
	res, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to pull the result: %v", err)
	}

	currentSize := int64(*res.Disk.Properties.DiskSizeGB)

	w.Logger().V(2).Infof("expand azure disk(%s) successfully, currentSize(%d)", volumeID, currentSize)

	return &azdiskv1beta2.AzVolumeStatusDetail{
		CapacityBytes:         currentSize,
		NodeExpansionRequired: true,
	}, nil
}

func (c *CloudProvisioner) CreateSnapshot(
	ctx context.Context,
	sourceVolumeID string,
	snapshotName string,
	secrets map[string]string,
	parameters map[string]string) (*azdiskv1beta2.Snapshot, error) {
	snapshotName = azureutils.CreateValidDiskName(snapshotName, true)

	var customTags string
	// set incremental snapshot as true by default
	incremental := true
	var resourceGroup, subsID, dataAccessAuthMode string
	var err error
	localCloud := c.cloud
	location := c.cloud.Location

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
		case azureconstants.SubscriptionIDField:
			subsID = v
		case azureconstants.DataAccessAuthModeField:
			dataAccessAuthMode = v
		case azureconstants.LocationField:
			location = v
		case azureconstants.UserAgentField:
			newUserAgent := v
			localCloud, err = azureutils.GetCloudProviderFromClient(
				ctx,
				c.kubeClient,
				c.config,
				newUserAgent)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "create cloud with UserAgent(%s) failed with: (%s)", newUserAgent, err)
			}
		default:
			return nil, status.Errorf(codes.Internal, "AzureDisk - invalid option %s in VolumeSnapshotClass", k)
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
	if subsID == "" {
		subsID = azureutils.GetSubscriptionIDFromURI(sourceVolumeID)
	}

	customTagsMap, err := volumehelper.ConvertTagsToMap(customTags)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, err.Error())
	}
	tags := make(map[string]*string)
	for k, v := range customTagsMap {
		value := v
		tags[k] = &value
	}

	snapshot := armcompute.Snapshot{
		Properties: &armcompute.SnapshotProperties{
			CreationData: &armcompute.CreationData{
				CreateOption: to.Ptr(armcompute.DiskCreateOptionCopy),
				SourceURI:    &sourceVolumeID,
			},
			Incremental: &incremental,
		},
		Location: &location,
		Tags:     tags,
	}
	if dataAccessAuthMode != "" {
		if err := azureutils.ValidateDataAccessAuthMode(dataAccessAuthMode); err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		snapshot.Properties.DataAccessAuthMode = to.Ptr(armcompute.DataAccessAuthMode(dataAccessAuthMode))
	}

	klog.V(2).Infof("begin to create snapshot(%s, incremental: %v) under rg(%s)", snapshotName, incremental, resourceGroup)

	poller, rerr := localCloud.SnapshotsClient.BeginCreateOrUpdate(ctx, resourceGroup, snapshotName, snapshot, nil)
	if rerr != nil {
		if strings.Contains(rerr.Error(), "existing disk") {
			return nil, status.Error(codes.AlreadyExists, fmt.Sprintf("request snapshot(%s) under rg(%s) already exists, but the SourceVolumeId is different, error details: %v", snapshotName, resourceGroup, rerr.Error()))
		}

		azureutils.SleepIfThrottled(rerr, azureconstants.SnapshotOpThrottlingSleepSec)
		return nil, status.Error(codes.Internal, fmt.Sprintf("create snapshot error: %v", rerr.Error()))
	}
	_, rerr = poller.PollUntilDone(ctx, nil)
	if rerr != nil {
		return nil, fmt.Errorf("failed to pull the result: %v", rerr)
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
	secrets map[string]string) (*azdiskv1beta2.ListSnapshotsResult, error) {
	// SnapshotID is not empty, return snapshot that match the snapshot id.
	if len(snapshotID) != 0 {
		snapshot, err := c.getSnapshotByID(ctx, c.cloud.ResourceGroup, snapshotID, sourceVolumeID)
		if err != nil {
			if strings.Contains(err.Error(), azureconstants.ResourceNotFound) {
				return &azdiskv1beta2.ListSnapshotsResult{}, nil
			}
			return nil, status.Error(codes.Internal, err.Error())
		}
		entries := []azdiskv1beta2.Snapshot{*snapshot}

		listSnapshotResp := &azdiskv1beta2.ListSnapshotsResult{
			Entries: entries,
		}
		return listSnapshotResp, nil
	}

	// no SnapshotID is set, return all snapshots that satisfy the request.
	pager := c.cloud.SnapshotsClient.NewListByResourceGroupPager(c.cloud.ResourceGroup, nil)
	var snapshots []armcompute.Snapshot
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to advance page: %v", err)
		}
		for _, snapshot := range page.Value {
			snapshots = append(snapshots, *snapshot)
		}
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
	entries := []azdiskv1beta2.Snapshot{}
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

	listSnapshotResp := &azdiskv1beta2.ListSnapshotsResult{
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
	poller, rerr := c.cloud.SnapshotsClient.BeginDelete(ctx, resourceGroup, snapshotName, nil)
	if rerr != nil {
		return status.Error(codes.Internal, fmt.Sprintf("delete snapshot error: %v", rerr.Error()))
	}
	_, rerr = poller.PollUntilDone(ctx, nil)
	klog.V(2).Infof("delete snapshot(%s) under rg(%s) successfully", snapshotName, resourceGroup)

	return nil
}

func (c *CloudProvisioner) CheckDiskExists(ctx context.Context, diskURI string) (*armcompute.Disk, error) {
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

	disk, rerr := c.cloud.DisksClient.Get(ctx, resourceGroup, diskName, nil)
	if rerr != nil {
		return nil, rerr
	}

	return &disk.Disk, nil
}

// GetSourceDiskSize recursively searches for the sourceDisk and returns: sourceDisk disk size, error
func (c *CloudProvisioner) GetSourceDiskSize(ctx context.Context, resourceGroup, diskName string, curDepth, maxDepth int) (*int32, error) {
	if curDepth > maxDepth {
		return nil, status.Error(codes.Internal, fmt.Sprintf("current depth (%d) surpassed the max depth (%d) while searching for the source disk size", curDepth, maxDepth))
	}
	result, rerr := c.cloud.DisksClient.Get(ctx, resourceGroup, diskName, nil)
	if rerr != nil {
		return nil, rerr
	}
	if result.Properties == nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("DiskProperty not found for disk (%s) in resource group (%s)", diskName, resourceGroup))
	}

	if result.Properties.CreationData != nil && *(*result.Properties.CreationData).CreateOption == "Copy" {
		klog.V(2).Infof("Clone source disk has a parent source")
		sourceResourceID := *result.Properties.CreationData.SourceResourceID
		parentResourceGroup, _ := azureutils.GetResourceGroupFromURI(sourceResourceID)
		parentDiskName := path.Base(sourceResourceID)
		return c.GetSourceDiskSize(ctx, parentResourceGroup, parentDiskName, curDepth+1, maxDepth)
	}

	if (*result.Properties).DiskSizeGB == nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("DiskSizeGB for disk (%s) in resourcegroup (%s) is nil", diskName, resourceGroup))
	}
	return (*result.Properties).DiskSizeGB, nil
}

func (c *CloudProvisioner) CheckDiskCapacity(ctx context.Context, resourceGroup, diskName string, requestGiB int) (bool, error) {
	if c.isGetDiskThrottled() {
		klog.Warningf("skip checkDiskCapacity((%s, %s) since it's still in throttling", resourceGroup, diskName)
		return true, nil
	}

	klog.Infof("line 1290 ctx: %+v rg: %+v disk: %+v", ctx, resourceGroup, diskName)


	klog.Infof("client: %+v",  c.cloud.DisksClient)
	disk, rerr := c.cloud.DisksClient.Get(ctx, resourceGroup, diskName, nil)
	// Because we can not judge the reason of the error. Maybe the disk does not exist.
	// So here we do not handle the error.
	if rerr == nil {
		if !reflect.DeepEqual(disk, armcompute.Disk{}) && disk.Properties.DiskSizeGB != nil && int(*disk.Properties.DiskSizeGB) != requestGiB {
			return false, status.Errorf(codes.AlreadyExists, "the request volume already exists, but its capacity(%v) is different from (%v)", *disk.Properties.DiskSizeGB, requestGiB)
		}
	} else {
		return false, rerr
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

func (c *CloudProvisioner) getSnapshotByID(ctx context.Context, resourceGroup string, snapshotName string, sourceVolumeID string) (*azdiskv1beta2.Snapshot, error) {
	snapshotNameVal, resourceGroupName, err := c.GetSnapshotAndResourceNameFromSnapshotID(snapshotName)
	if err != nil {
		return nil, err
	}

	if snapshotNameVal == "" && resourceGroupName == "" {
		snapshotNameVal = snapshotName
		resourceGroupName = resourceGroup
	}

	snapshot, rerr := c.cloud.SnapshotsClient.Get(ctx, resourceGroupName, snapshotNameVal, nil)
	if rerr != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("get snapshot %s from rg(%s) error: %v", snapshotNameVal, resourceGroupName, rerr.Error()))
	}

	return azureutils.NewAzureDiskSnapshot(sourceVolumeID, &snapshot.Snapshot)
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
	capacityRange *azdiskv1beta2.CapacityRange,
	volumeCaps []azdiskv1beta2.VolumeCapability,
	diskParams azureutils.ManagedDiskParameters) error {
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

	if azureutils.IsAzureStackCloud(c.cloud.Config.Cloud, c.cloud.Config.DisableAzureStackCloud) {
		if diskParams.MaxShares > 1 {
			return status.Error(codes.InvalidArgument, fmt.Sprintf("Invalid maxShares value: %d as Azure Stack does not support shared disk.", diskParams.MaxShares))
		}
	}

	if diskParams.Location == "" {
		diskParams.Location = c.cloud.Location
	}

	return nil
}

// listVolumesInCluster is a helper function for ListVolumes used for when there is an available kubeclient
func (c *CloudProvisioner) listVolumesInCluster(ctx context.Context, start, maxEntries int) (*azdiskv1beta2.ListVolumesResult, error) {
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
	entries := []azdiskv1beta2.VolumeEntry{}
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

	listVolumesResp := &azdiskv1beta2.ListVolumesResult{
		Entries:   entries,
		NextToken: nextTokenString,
	}

	return listVolumesResp, nil
}

// listVolumesInNodeResourceGroup is a helper function for ListVolumes used for when there is no available kubeclient
func (c *CloudProvisioner) listVolumesInNodeResourceGroup(ctx context.Context, start, maxEntries int) (*azdiskv1beta2.ListVolumesResult, error) {
	entries := []azdiskv1beta2.VolumeEntry{}
	listStatus := c.listVolumesByResourceGroup(ctx, c.cloud.ResourceGroup, entries, start, maxEntries, nil)
	if listStatus.err != nil {
		return nil, listStatus.err
	}

	nextTokenString := ""
	if !listStatus.isCompleteRun {
		nextTokenString = strconv.Itoa(start + listStatus.numVisited)
	}

	listVolumesResp := &azdiskv1beta2.ListVolumesResult{
		Entries:   listStatus.entries,
		NextToken: nextTokenString,
	}

	return listVolumesResp, nil
}

// listVolumesByResourceGroup is a helper function that updates the ListVolumeResponse_Entry slice and returns number of total visited volumes, number of volumes that needs to be visited and an error if found
func (c *CloudProvisioner) listVolumesByResourceGroup(ctx context.Context, resourceGroup string, entries []azdiskv1beta2.VolumeEntry, start, maxEntries int, volSet map[string]bool) listVolumeStatus {
	pager := c.cloud.DisksClient.NewListByResourceGroupPager(resourceGroup, nil)
	var disks []armcompute.Disk
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return listVolumeStatus{
				numVisited: len(disks),
				err:        fmt.Errorf("failed to advance page: %v", err),
			}
		}
		for _, disk := range page.Value {
			disks = append(disks, *disk)
		}
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
		if disk.Properties == nil || *disk.Properties.HyperVGeneration == "" {
			nodeList := []string{}

			if disk.ManagedBy != nil {
				attachedNode, err := c.GetNodeNameFromProviderID(ctx, *disk.ManagedBy)
				if err != nil {
					return listVolumeStatus{err: err}
				}
				nodeList = append(nodeList, string(attachedNode))
			}

			entries = append(entries, azdiskv1beta2.VolumeEntry{
				Details: &azdiskv1beta2.VolumeDetails{
					VolumeID: *disk.ID,
				},
				Status: &azdiskv1beta2.VolumeStatus{
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
func pickAvailabilityZone(requirement *azdiskv1beta2.TopologyRequirement, region string) string {
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

func (c *CloudProvisioner) isPerfOptimizationEnabled() bool {
	return c.config.NodeConfig.EnablePerfOptimization
}

func GetLastSegment(ID, separator string) (string, error) {
	parts := strings.Split(ID, separator)
	name := parts[len(parts)-1]
	if len(name) == 0 {
		return "", fmt.Errorf("resource name was missing from identifier")
	}

	return name, nil
}

func getInstanceIDFromProviderID(providerID, scaleSetName string) (string, error) {
	if providerID == "" {
		return providerID, fmt.Errorf("failed to get instanceID from providerID: providerID is empty")
	}

	instanceID, err := GetLastSegment(providerID, "/")
	if err != nil {
		klog.Warningf("failed to extract instanceID from providerID (%s): %v", providerID, err)
		return "", err
	}

	if strings.HasPrefix(strings.ToLower(instanceID), strings.ToLower(scaleSetName)) {
		instanceID, err = GetLastSegment(instanceID, "_")
		if err != nil {
			klog.Warningf("failed to get instanceID: %v", err)
			return "", err
		}
	}

	return instanceID, nil
}

func GetDiskLun(diskName, diskURI string, disks []*armcompute.DataDisk) (int32, error) {
	for _, disk := range disks {
		if disk.Lun != nil && (disk.Name != nil && diskName != "" && strings.EqualFold(*disk.Name, diskName)) ||
			(disk.Vhd != nil && disk.Vhd.URI != nil && diskURI != "" && strings.EqualFold(*disk.Vhd.URI, diskURI)) ||
			(disk.ManagedDisk != nil && strings.EqualFold(*disk.ManagedDisk.ID, diskURI)) {
			if disk.ToBeDetached != nil && *disk.ToBeDetached {
				klog.Warningf("azureDisk - find disk(ToBeDetached): lun %d name %s uri %s", *disk.Lun, diskName, diskURI)
			} else {
				// found the disk
				klog.Infof("azureDisk - find disk: lun %d name %s uri %s", *disk.Lun, diskName, diskURI)
				return *disk.Lun, nil
			}
		}
	}

	return -1, fmt.Errorf("failed to find lun of disk %s", diskName)
}

func GetInstanceIDAndScaleSetNameFromNodeName(nodeName string) (string, string, error) {
	nameLength := len(nodeName)
	if nameLength < 6 {
		return "", "", fmt.Errorf("not a vmss instance")
	}
	scaleSetName := fmt.Sprintf("%s", string(nodeName[:nameLength-6]))

	id, err := strconv.Atoi(string((nodeName)[nameLength-6:]))
	if err != nil {
		return "", "", fmt.Errorf("cannot parse instance id from node name")
	}
	instanceID := fmt.Sprintf("%d", id)

	return scaleSetName, instanceID, nil
}

func GetFullScaleSetNameFromProviderID(fullVMName string) (string, error) {
	parts := strings.Split(fullVMName, "/")
	scaleSetName := ""

	for index, part := range parts {
		if index >= (len(parts) - 2) {
			return scaleSetName, nil
		}
		scaleSetName = fmt.Sprintf("%s/%s", scaleSetName, part)
	}

	return "", fmt.Errorf("invalid providerID")
}

func GetInstanceIDAndFullScaleSetNameFromProviderID(providerID string) (string, string, error) {
	fullScaleSetName, err := GetFullScaleSetNameFromProviderID(providerID)
	if err != nil {
		return "", "", fmt.Errorf("failed to get full scaleSet name: %v", err)
	}

	instanceID, err := GetLastSegment(providerID, "/")
	if err != nil {
		return "", "", fmt.Errorf("failed to extract from providerID: %v", err)
	}

	scaleSetName, err := GetLastSegment(fullScaleSetName, "/")
	if err != nil {
		return "", "", fmt.Errorf("failed to get scaleSet name from full scaleSet name: %v", err)
	}

	// instanceID contains scaleSetName
	if strings.HasPrefix(strings.ToLower(instanceID), strings.ToLower(scaleSetName)) {
		instanceID, err = GetLastSegment(instanceID, "_")
		if err != nil {
			return "", "", fmt.Errorf("failed to get instanceID: %v", err)
		}
	}
	return instanceID, fullScaleSetName, nil
}

func (c *CloudProvisioner) GetNodeNameFromProviderID(ctx context.Context, providerID string) (string, error) {
	scaleSetName, err := GetFullScaleSetNameFromProviderID(providerID)
	if err != nil {
		return "", fmt.Errorf("failed to get full scaleSet name: %v", err)
	}

	instanceID, _, err := GetInstanceIDAndFullScaleSetNameFromProviderID(providerID)
	if err != nil {
		return "", err
	}

	vmss, found := c.cloud.VMSSVMCache.VmssGetter(scaleSetName)
	if !found {
		scaleSetName, err = GetLastSegment(scaleSetName, "/")
		if err != nil {
			return "", fmt.Errorf("failed to get scaleSetName from full scaleSetName: %v", err)
		}

		vmssVMClient := c.cloud.VMSSVMClient
		resVM, err := vmssVMClient.Get(ctx, c.cloud.ResourceGroup, scaleSetName, instanceID, nil)
		if err != nil {
			return "", fmt.Errorf("failed to finish the request: %v", err)
		}

		nodeName := resVM.VirtualMachineScaleSetVM.Properties.OSProfile.ComputerName

		return *nodeName, nil
	} else {
		nodeName := ""
		vmss.VMCache.Range(func(key, value interface{}) bool {
			vmEntry := value.(*azureutils.VMCacheEntry)
			if *vmEntry.InstanceID == instanceID {
				nodeName = *vmEntry.Name
				return false
			} else {
				return true
			}
		})

		if nodeName != "" {
			return nodeName, nil
		} else {
			return "", fmt.Errorf("node name not found")
		}
	}

}
