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

package azureutils

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2020-12-01/compute"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/pborman/uuid"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	"k8s.io/mount-utils"

	volumeUtil "k8s.io/kubernetes/pkg/volume/util"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	azclients "sigs.k8s.io/cloud-provider-azure/pkg/azureclients"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

const (
	azurePublicCloud = "AZUREPUBLICCLOUD"
	// default IOPS Caps & Throughput Cap (MBps) per https://docs.microsoft.com/en-us/azure/virtual-machines/linux/disks-ultra-ssd
	// see https://docs.microsoft.com/en-us/rest/api/compute/disks/createorupdate#uri-parameters
	diskNameMaxLengthV1         = 80
	diskNameGenerateMaxLengthV1 = 76 // maxLength = 80 - (4 for ".vhd") = 76
)

// GetCloudProvider get Azure Cloud Provider
func GetCloudProvider(kubeconfig, secretName, secretNamespace, userAgent string) (*azure.Cloud, error) {
	az := &azure.Cloud{
		InitSecretConfig: azure.InitSecretConfig{
			SecretName:      secretName,
			SecretNamespace: secretNamespace,
			CloudConfigKey:  "cloud-config",
		},
	}

	kubeClient, err := getKubeClient(kubeconfig)
	if err != nil {
		klog.Warningf("get kubeconfig(%s) failed with error: %v", kubeconfig, err)
		if !os.IsNotExist(err) && err != rest.ErrNotInCluster {
			return az, fmt.Errorf("failed to get KubeClient: %v", err)
		}
	}
	var (
		config     *azure.Config
		fromSecret bool
	)

	if kubeClient != nil {
		klog.V(2).Infof("reading cloud config from secret %s/%s", az.SecretNamespace, az.SecretName)
		az.KubeClient = kubeClient
		config, err = az.GetConfigFromSecret()
		if err == nil && config != nil {
			fromSecret = true
		}
		if err != nil {
			klog.Warningf("InitializeCloudFromSecret: failed to get cloud config from secret %s/%s: %v", az.SecretNamespace, az.SecretName, err)
		}
	}

	if config == nil {
		klog.V(2).Infof("could not read cloud config from secret %s/%s", az.SecretNamespace, az.SecretName)
		credFile, ok := os.LookupEnv(consts.DefaultAzureCredentialFileEnv)
		if ok && strings.TrimSpace(credFile) != "" {
			klog.V(2).Infof("%s env var set as %v", consts.DefaultAzureCredentialFileEnv, credFile)
		} else {
			if util.IsWindowsOS() {
				credFile = consts.DefaultCredFilePathWindows
			} else {
				credFile = consts.DefaultCredFilePathLinux
			}
			klog.V(2).Infof("use default %s env var: %v", consts.DefaultAzureCredentialFileEnv, credFile)
		}

		credFileConfig, err := os.Open(credFile)
		if err != nil {
			klog.Warningf("load azure config from file(%s) failed with %v", credFile, err)
		} else {
			defer credFileConfig.Close()
			klog.V(2).Infof("read cloud config from file: %s successfully", credFile)
			if config, err = azure.ParseConfig(credFileConfig); err != nil {
				klog.Warningf("parse config file(%s) failed with error: %v", credFile, err)
			}
		}
	}

	if config == nil {
		klog.V(2).Infof("no cloud config provided, error: %v, driver will run without cloud config", err)
	} else {
		// disable disk related rate limit
		config.DiskRateLimit = &azclients.RateLimitConfig{
			CloudProviderRateLimit: false,
		}
		config.SnapshotRateLimit = &azclients.RateLimitConfig{
			CloudProviderRateLimit: false,
		}
		config.UserAgent = userAgent
		if err = az.InitializeCloudFromConfig(config, fromSecret, false); err != nil {
			klog.Warningf("InitializeCloudFromConfig failed with error: %v", err)
		}
	}

	// reassign kubeClient
	if kubeClient != nil && az.KubeClient == nil {
		az.KubeClient = kubeClient
	}
	return az, nil
}

// GetDiskLUN : deviceInfo could be a LUN number or a device path, e.g. /dev/disk/azure/scsi1/lun2
func GetDiskLUN(deviceInfo string) (int32, error) {
	var diskLUN string
	if len(deviceInfo) <= 2 {
		diskLUN = deviceInfo
	} else {
		// extract the LUN num from a device path
		matches := consts.LunPathRE.FindStringSubmatch(deviceInfo)
		if len(matches) == 2 {
			diskLUN = matches[1]
		} else {
			return -1, fmt.Errorf("cannot parse deviceInfo: %s", deviceInfo)
		}
	}

	lun, err := strconv.Atoi(diskLUN)
	if err != nil {
		return -1, err
	}
	return int32(lun), nil
}

func GetDiskName(diskURI string) (string, error) {
	matches := consts.ManagedDiskPathRE.FindStringSubmatch(diskURI)
	if len(matches) != 2 {
		return "", fmt.Errorf("could not get disk name from %s, correct format: %s", diskURI, consts.ManagedDiskPathRE)
	}
	return matches[1], nil
}

// Disk name must begin with a letter or number, end with a letter, number or underscore,
// and may contain only letters, numbers, underscores, periods, or hyphens.
// See https://docs.microsoft.com/en-us/rest/api/compute/disks/createorupdate#uri-parameters
//
//
// Snapshot name must begin with a letter or number, end with a letter, number or underscore,
// and may contain only letters, numbers, underscores, periods, or hyphens.
// See https://docs.microsoft.com/en-us/rest/api/compute/snapshots/createorupdate#uri-parameters
//
// Since the naming rule of disk is same with snapshot's, here we use the same function to handle disks and snapshots.
func CreateValidDiskName(volumeName string) string {
	diskName := volumeName
	if len(diskName) > diskNameMaxLengthV1 {
		diskName = diskName[0:diskNameMaxLengthV1]
		klog.Warningf("since the maximum volume name length is %d, so it is truncated as (%q)", diskNameMaxLengthV1, diskName)
	}
	if !checkDiskName(diskName) || len(diskName) < diskNameMinLength {
		// todo: get cluster name
		diskName = volumeUtil.GenerateVolumeName("pvc-disk", uuid.NewUUID().String(), diskNameGenerateMaxLengthV1)
		klog.Warningf("the requested volume name (%q) is invalid, so it is regenerated as (%q)", volumeName, diskName)
	}

	return diskName
}

func GetResourceGroupFromURI(diskURI string) (string, error) {
	fields := strings.Split(diskURI, "/")
	if len(fields) != 9 || strings.ToLower(fields[3]) != "resourcegroups" {
		return "", fmt.Errorf("invalid disk URI: %s", diskURI)
	}
	return fields[4], nil
}

func GetValidCreationData(subscriptionID, resourceGroup, sourceResourceID, sourceType string) (compute.CreationData, error) {
	if sourceResourceID == "" {
		return compute.CreationData{
			CreateOption: compute.Empty,
		}, nil
	}

	switch sourceType {
	case consts.SourceSnapshot:
		if match := consts.DiskSnapshotPathRE.FindString(sourceResourceID); match == "" {
			sourceResourceID = fmt.Sprintf(consts.DiskSnapshotPath, subscriptionID, resourceGroup, sourceResourceID)
		}

	case consts.SourceVolume:
		if match := consts.ManagedDiskPathRE.FindString(sourceResourceID); match == "" {
			sourceResourceID = fmt.Sprintf(consts.ManagedDiskPath, subscriptionID, resourceGroup, sourceResourceID)
		}
	default:
		return compute.CreationData{
			CreateOption: compute.Empty,
		}, nil
	}

	splits := strings.Split(sourceResourceID, "/")
	if len(splits) > 9 {
		if sourceType == consts.SourceSnapshot {
			return compute.CreationData{}, fmt.Errorf("sourceResourceID(%s) is invalid, correct format: %s", sourceResourceID, consts.DiskSnapshotPathRE)
		}

		return compute.CreationData{}, fmt.Errorf("sourceResourceID(%s) is invalid, correct format: %s", sourceResourceID, consts.ManagedDiskPathRE)
	}
	return compute.CreationData{
		CreateOption:     compute.Copy,
		SourceResourceID: &sourceResourceID,
	}, nil
}

func IsCorruptedDir(dir string) bool {
	_, pathErr := mount.PathExists(dir)
	fmt.Printf("IsCorruptedDir(%s) returned with error: %v", dir, pathErr)
	return pathErr != nil && mount.IsCorruptedMnt(pathErr)
}

func IsValidVolumeCapabilities(volCaps []*csi.VolumeCapability) bool {
	hasSupport := func(cap *csi.VolumeCapability) bool {
		for _, c := range consts.VolumeCaps {
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

func NormalizeCachingMode(cachingMode v1.AzureDataDiskCachingMode) (v1.AzureDataDiskCachingMode, error) {
	if cachingMode == "" {
		return defaultAzureDataDiskCachingMode, nil
	}

	if !consts.SupportedCachingModes.Has(string(cachingMode)) {
		return "", fmt.Errorf("azureDisk - %s is not supported cachingmode. Supported values are %s", cachingMode, consts.SupportedCachingModes.List())
	}

	return cachingMode, nil
}

func NormalizeStorageAccountType(storageAccountType, cloud string, disableAzureStackCloud bool) (compute.DiskStorageAccountTypes, error) {
	if storageAccountType == "" {
		if IsAzureStackCloud(cloud, disableAzureStackCloud) {
			return azureStackCloudDefaultStorageAccountType, nil
		}
		return azurePublicCloudDefaultStorageAccountType, nil
	}

	sku := compute.DiskStorageAccountTypes(storageAccountType)
	supportedSkuNames := compute.PossibleDiskStorageAccountTypesValues()
	if IsAzureStackCloud(cloud, disableAzureStackCloud) {
		supportedSkuNames = []compute.DiskStorageAccountTypes{compute.StandardLRS, compute.PremiumLRS}
	}
	for _, s := range supportedSkuNames {
		if sku == s {
			return sku, nil
		}
	}

	return "", fmt.Errorf("azureDisk - %s is not supported sku/storageaccounttype. Supported values are %s", storageAccountType, supportedSkuNames)
}

// PickAvailabilityZone selects 1 zone given topology requirement.
// if not found or topology requirement is not zone format, empty string is returned.
func PickAvailabilityZone(requirement *csi.TopologyRequirement, region, topologyKey string) string {
	if requirement == nil {
		return ""
	}
	for _, topology := range requirement.GetPreferred() {
		if zone, exists := topology.GetSegments()[consts.WellKnownTopologyKey]; exists {
			if IsValidAvailabilityZone(zone, region) {
				return zone
			}
		}
		if zone, exists := topology.GetSegments()[topologyKey]; exists {
			if IsValidAvailabilityZone(zone, region) {
				return zone
			}
		}
	}
	for _, topology := range requirement.GetRequisite() {
		if zone, exists := topology.GetSegments()[consts.WellKnownTopologyKey]; exists {
			if IsValidAvailabilityZone(zone, region) {
				return zone
			}
		}
		if zone, exists := topology.GetSegments()[topologyKey]; exists {
			if IsValidAvailabilityZone(zone, region) {
				return zone
			}
		}
	}
	return ""
}

func getKubeClient(kubeconfig string) (*kubernetes.Clientset, error) {
	var (
		config *rest.Config
		err    error
	)
	if kubeconfig != "" {
		if config, err = clientcmd.BuildConfigFromFlags("", kubeconfig); err != nil {
			return nil, err
		}
	} else {
		if config, err = rest.InClusterConfig(); err != nil {
			return nil, err
		}
	}

	return kubernetes.NewForConfig(config)
}
