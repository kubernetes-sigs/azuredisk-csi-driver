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
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2021-07-01/compute"
	"github.com/Azure/go-autorest/autorest/to"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/pborman/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/net"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	kubeutil "k8s.io/kubernetes/pkg/volume/util"
	"k8s.io/mount-utils"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	azdiskinformers "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/informers/externalversions"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/optimization"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/workflow"
	"sigs.k8s.io/cloud-provider-azure/pkg/azureclients"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	azureStackCloud                               = "AZURESTACKCLOUD"
	azurePublicCloudDefaultStorageAccountType     = compute.DiskStorageAccountTypesStandardSSDLRS
	azureStackCloudDefaultStorageAccountType      = compute.DiskStorageAccountTypesStandardLRS
	defaultAzureDataDiskCachingMode               = v1.AzureDataDiskCachingReadOnly
	defaultAzureDataDiskCachingModeForSharedDisks = v1.AzureDataDiskCachingNone

	// default IOPS Caps & Throughput Cap (MBps) per https://docs.microsoft.com/en-us/azure/virtual-machines/linux/disks-ultra-ssd
	// see https://docs.microsoft.com/en-us/rest/api/compute/disks/createorupdate#uri-parameters
	diskNameMinLength = 1
	// Resetting max length to 63 since the disk name is used in the label "volume-name"
	// of the kubernetes object and a label cannot have length greater than 63.
	// https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/
	diskNameMaxLengthForLabel = 63
	diskNameMaxLength         = 80
	// maxLength = 63 - (4 for ".vhd") = 59
	diskNameGenerateMaxLengthForLabel = 59
	// maxLength = 80 - (4 for ".vhd") = 76
	diskNameGenerateMaxLength = 76
)

type ClientOperationMode int

type ManagedDiskParameters struct {
	AccountType             string
	CachingMode             v1.AzureDataDiskCachingMode
	DeviceSettings          map[string]string
	DiskAccessID            string
	DiskEncryptionSetID     string
	DiskEncryptionType      string
	DiskIOPSReadWrite       string
	DiskMBPSReadWrite       string
	DiskName                string
	EnableAsyncAttach       *bool
	EnableBursting          *bool
	FsType                  string
	Incremental             bool
	Location                string
	LogicalSectorSize       int
	MaxShares               int
	NetworkAccessPolicy     string
	PerfProfile             string
	SubscriptionID          string
	ResourceGroup           string
	Tags                    map[string]string
	UserAgent               string
	VolumeContext           map[string]string
	WriteAcceleratorEnabled string
	Zoned                   string
}

const (
	Cached ClientOperationMode = iota
	Uncached
)

type CRIUpdateMode uint

const (
	UpdateCRI       CRIUpdateMode = 0b01
	UpdateCRIStatus CRIUpdateMode = 0b10
	UpdateAll       CRIUpdateMode = 0b11
)

func CreateLabelRequirements(label string, operator selection.Operator, values ...string) (*labels.Requirement, error) {
	req, err := labels.NewRequirement(label, operator, values)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("Unable to create Requirement to for label key : (%s) and label value: (%s)", label, values))
	}
	return req, nil
}

func IsAzureStackCloud(cloud string, disableAzureStackCloud bool) bool {
	return !disableAzureStackCloud && strings.EqualFold(cloud, azureStackCloud)
}

// gets the AzVolume cluster client
func GetAzDiskClient(config *rest.Config) (
	*azdisk.Clientset,
	error) {
	azDiskClient, err := azdisk.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return azDiskClient, nil
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

func GetFStype(attributes map[string]string) string {
	for k, v := range attributes {
		switch strings.ToLower(k) {
		case consts.FsTypeField:
			return strings.ToLower(v)
		}
	}
	return ""
}

func GetNodeMaxDiskCount(labels map[string]string) (int, error) {
	if labels == nil {
		return 0, fmt.Errorf("labels for the node are not provided")
	}
	instanceType, ok := labels[v1.LabelInstanceTypeStable]
	if !ok {
		return 0, fmt.Errorf("node instance type is not found")
	}
	vmsize := strings.ToUpper(instanceType)
	maxDataDiskCount, exists := MaxDataDiskCountMap[vmsize]
	if !exists {
		return 0, fmt.Errorf("disk count for the node instance type %s is not found", vmsize)
	}
	return int(maxDataDiskCount), nil
}

func GetMaxShares(attributes map[string]string) (int, error) {
	for k, v := range attributes {
		switch strings.ToLower(k) {
		case consts.MaxSharesField:
			maxShares, err := strconv.Atoi(v)
			if err != nil {
				return 0, fmt.Errorf("parse %s failed with error: %v", v, err)
			}
			if maxShares < 1 {
				return 0, fmt.Errorf("parse %s returned with invalid value: %d", v, maxShares)
			}
			return maxShares, nil
		}
	}
	return 1, nil // disk is not shared
}

func IsAsyncAttachEnabled(defaultValue bool, volumeContext map[string]string) bool {
	for k, v := range volumeContext {
		switch strings.ToLower(k) {
		case consts.EnableAsyncAttachField:
			if strings.EqualFold(v, consts.TrueValue) {
				return true
			}
			if strings.EqualFold(v, consts.FalseValue) {
				return false
			}
		}
	}
	return defaultValue
}

func GetSubscriptionIDFromURI(diskURI string) string {
	parts := strings.Split(diskURI, "/")
	for i, v := range parts {
		if strings.EqualFold(v, "subscriptions") && (i+1) < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

func ValidateDiskEncryptionType(encryptionType string) error {
	if encryptionType == "" {
		return nil
	}
	supportedTypes := compute.PossibleEncryptionTypeValues()
	for _, s := range supportedTypes {
		if encryptionType == string(s) {
			return nil
		}
	}
	return fmt.Errorf("DiskEncryptionType(%s) is not supported", encryptionType)
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
		supportedSkuNames = []compute.DiskStorageAccountTypes{compute.DiskStorageAccountTypesStandardLRS, compute.DiskStorageAccountTypesPremiumLRS}
	}
	for _, s := range supportedSkuNames {
		if sku == s {
			return sku, nil
		}
	}

	return "", fmt.Errorf("azureDisk - %s is not supported sku/storageaccounttype. Supported values are %s", storageAccountType, supportedSkuNames)
}

func NormalizeCachingMode(cachingMode v1.AzureDataDiskCachingMode, maxShares int) (v1.AzureDataDiskCachingMode, error) {
	if cachingMode == "" {
		if maxShares > 1 {
			return defaultAzureDataDiskCachingModeForSharedDisks, nil
		}
		return defaultAzureDataDiskCachingMode, nil
	}

	if !consts.SupportedCachingModes.Has(string(cachingMode)) {
		return "", fmt.Errorf("azureDisk - %s is not supported cachingmode. Supported values are %s", cachingMode, consts.SupportedCachingModes.List())
	}

	return cachingMode, nil
}

func NormalizeNetworkAccessPolicy(networkAccessPolicy string) (compute.NetworkAccessPolicy, error) {
	if networkAccessPolicy == "" {
		return compute.NetworkAccessPolicyAllowAll, nil
	}
	policy := compute.NetworkAccessPolicy(networkAccessPolicy)
	for _, s := range compute.PossibleNetworkAccessPolicyValues() {
		if policy == s {
			return policy, nil
		}
	}
	return "", fmt.Errorf("azureDisk - %s is not supported NetworkAccessPolicy. Supported values are %s", networkAccessPolicy, compute.PossibleNetworkAccessPolicyValues())
}

func ParseDiskParameters(parameters map[string]string) (ManagedDiskParameters, error) {
	var err error
	if parameters == nil {
		parameters = make(map[string]string)
	}

	diskParams := ManagedDiskParameters{
		DeviceSettings: make(map[string]string),
		Incremental:    true, //true by default
		Tags:           make(map[string]string),
		VolumeContext:  parameters,
	}
	for k, v := range parameters {
		switch strings.ToLower(k) {
		case consts.SkuNameField:
			diskParams.AccountType = v
		case consts.LocationField:
			diskParams.Location = v
		case consts.StorageAccountTypeField:
			diskParams.AccountType = v
		case consts.CachingModeField:
			diskParams.CachingMode = v1.AzureDataDiskCachingMode(v)
		case consts.SubscriptionIDField:
			diskParams.SubscriptionID = v
		case consts.ResourceGroupField:
			diskParams.ResourceGroup = v
		case consts.DiskIOPSReadWriteField:
			diskParams.DiskIOPSReadWrite = v
		case consts.DiskMBPSReadWriteField:
			diskParams.DiskMBPSReadWrite = v
		case consts.LogicalSectorSizeField:
			diskParams.LogicalSectorSize, err = strconv.Atoi(v)
			if err != nil {
				return diskParams, fmt.Errorf("parse %s failed with error: %v", v, err)
			}
		case consts.DiskNameField:
			diskParams.DiskName = v
		case consts.DesIDField:
			diskParams.DiskEncryptionSetID = v
		case consts.DiskEncryptionTypeField:
			diskParams.DiskEncryptionType = v
		case consts.TagsField:
			customTagsMap, err := util.ConvertTagsToMap(v)
			if err != nil {
				return diskParams, err
			}
			for k, v := range customTagsMap {
				diskParams.Tags[k] = v
			}
		case azure.WriteAcceleratorEnabled:
			diskParams.WriteAcceleratorEnabled = v
		case consts.MaxSharesField:
			diskParams.MaxShares, err = ParseMaxShares(v)
			if err != nil {
				return diskParams, err
			}
		case consts.MaxMountReplicaCountField:
			continue
		case consts.PvcNameKey:
			diskParams.Tags[consts.PvcNameTag] = v
		case consts.PvcNamespaceKey:
			diskParams.Tags[consts.PvcNamespaceTag] = v
		case consts.PvNameKey:
			diskParams.Tags[consts.PvNameTag] = v
		case consts.PerfProfileField:
			if !optimization.IsValidPerfProfile(v) {
				return diskParams, fmt.Errorf("perf profile %s is not supported, supported tuning modes are none and basic", v)
			}
			diskParams.PerfProfile = v
		case consts.NetworkAccessPolicyField:
			diskParams.NetworkAccessPolicy = v
		case consts.DiskAccessIDField:
			diskParams.DiskAccessID = v
		case consts.EnableBurstingField:
			if strings.EqualFold(v, consts.TrueValue) {
				diskParams.EnableBursting = to.BoolPtr(true)
			}
		case consts.UserAgentField:
			diskParams.UserAgent = v
		// The following parameters are not used by the cloud provisioner, but must be present in the VolumeContext
		// returned to the caller so that it is included in the parameters passed to Node{Publish|Stage}Volume.
		case consts.EnableAsyncAttachField:
			diskParams.VolumeContext[consts.EnableAsyncAttachField] = v
		case consts.IncrementalField:
			if v == "false" {
				diskParams.Incremental = false
			}
		case consts.ZonedField:
			// no op, only for backward compatibility with in-tree driver
		case consts.FsTypeField:
			diskParams.FsType = strings.ToLower(v)
		case consts.KindField:
			// fix csi migration issue: https://github.com/kubernetes/kubernetes/issues/103433
			diskParams.VolumeContext[consts.KindField] = string(v1.AzureManagedDisk)
		default:
			// accept all device settings params
			// device settings need to start with azureconstants.DeviceSettingsKeyPrefix
			if deviceSettings, err := optimization.GetDeviceSettingFromAttribute(k); err == nil {
				diskParams.DeviceSettings[filepath.Join(azureconstants.DummyBlockDevicePathLinux, deviceSettings)] = v
			} else {
				return diskParams, fmt.Errorf("invalid parameter %s in storage class", k)
			}
		}
	}
	return diskParams, nil
}

func ParseMaxShares(maxSharesValue string) (int, error) {
	maxShares, err := strconv.Atoi(maxSharesValue)
	if err != nil {
		return 0, fmt.Errorf("parse %s failed with error: %v", maxSharesValue, err)
	}
	if maxShares < 1 {
		return 0, fmt.Errorf("parse %s returned with invalid value: %d", maxSharesValue, maxShares)
	}
	return maxShares, nil
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
func CreateValidDiskName(volumeName string, usedForLabel bool) string {
	var maxDiskNameLength, maxGeneratedDiskNameLength int
	diskName := volumeName
	if usedForLabel {
		maxDiskNameLength = diskNameMaxLengthForLabel
		maxGeneratedDiskNameLength = diskNameGenerateMaxLengthForLabel
	} else {
		maxDiskNameLength = diskNameMaxLength
		maxGeneratedDiskNameLength = diskNameGenerateMaxLength
	}
	if len(diskName) > maxDiskNameLength {
		diskName = diskName[0:maxDiskNameLength]
		klog.Warningf("since the maximum volume name length is %d, so it is truncated as (%q)", diskNameMaxLength, diskName)
	}
	if !checkDiskName(diskName) || len(diskName) < diskNameMinLength {
		// todo: get cluster name
		diskName = kubeutil.GenerateVolumeName("pvc-disk", uuid.NewUUID().String(), maxGeneratedDiskNameLength)
		klog.Warningf("the requested volume name (%q) is invalid, so it is regenerated as (%q)", volumeName, diskName)
	}

	return diskName
}

// GetCloudProviderFromClient get Azure Cloud Provider
func GetCloudProviderFromClient(kubeClient *clientset.Clientset, secretName, secretNamespace, userAgent string, allowEmptyCloudConfig bool) (*azure.Cloud, error) {
	var config *azure.Config
	var fromSecret bool
	var err error
	az := &azure.Cloud{
		InitSecretConfig: azure.InitSecretConfig{
			SecretName:      secretName,
			SecretNamespace: secretNamespace,
			CloudConfigKey:  "cloud-config",
		},
	}
	if kubeClient != nil {
		klog.V(2).Infof("reading cloud config from secret %s/%s", az.SecretNamespace, az.SecretName)
		az.KubeClient = kubeClient
		config, err = az.GetConfigFromSecret()
		if err == nil && config != nil {
			fromSecret = true
		}
		if err != nil {
			klog.V(2).Infof("InitializeCloudFromSecret: failed to get cloud config from secret %s/%s: %v", az.SecretNamespace, az.SecretName, err)
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
		if allowEmptyCloudConfig {
			klog.V(2).Infof("no cloud config provided, error: %v, driver will run without cloud config", err)
		} else {
			return nil, fmt.Errorf("no cloud config provided, error: %v", err)
		}
	} else {
		// disable disk related rate limit
		config.DiskRateLimit = &azureclients.RateLimitConfig{
			CloudProviderRateLimit: false,
		}
		config.SnapshotRateLimit = &azureclients.RateLimitConfig{
			CloudProviderRateLimit: false,
		}
		config.UserAgent = userAgent

		// Create a new cloud provider
		az, err = azure.NewCloudWithoutFeatureGatesFromConfig(config, fromSecret, false)
		if err != nil {
			err = fmt.Errorf("failed to create cloud: %v", err)
			klog.Errorf(err.Error())
			return nil, err
		}
	}

	// reassign kubeClient
	if kubeClient != nil && az.KubeClient == nil {
		az.KubeClient = kubeClient
	}

	return az, nil
}

// GetCloudProviderFromConfig get Azure Cloud Provider
func GetCloudProvider(kubeConfig, secretName, secretNamespace, userAgent string, allowEmptyCloudConfig bool) (*azure.Cloud, error) {
	kubeClient, err := GetKubeClient(kubeConfig)
	if err != nil {
		klog.Warningf("get kubeconfig(%s) failed with error: %v", kubeConfig, err)
		if !os.IsNotExist(err) && !errors.Is(err, rest.ErrNotInCluster) {
			return nil, fmt.Errorf("failed to get KubeClient: %v", err)
		}
	}
	return GetCloudProviderFromClient(kubeClient, secretName, secretNamespace, userAgent, allowEmptyCloudConfig)
}

// GetKubeConfig gets config object from config file
func GetKubeConfig(kubeconfig string) (config *rest.Config, err error) {
	if kubeconfig != "" {
		if config, err = clientcmd.BuildConfigFromFlags("", kubeconfig); err != nil {
			return nil, err
		}
	} else {
		if config, err = rest.InClusterConfig(); err != nil {
			return nil, err
		}
	}
	return config, err
}

func GetKubeClient(kubeconfig string) (*clientset.Clientset, error) {
	config, err := GetKubeConfig(kubeconfig)
	if err != nil {
		return nil, err
	}

	return clientset.NewForConfig(config)
}

func IsValidDiskURI(diskURI string) error {
	if strings.Index(strings.ToLower(diskURI), "/subscriptions/") != 0 {
		return fmt.Errorf("invalid DiskURI: %v, correct format: %v", diskURI, consts.DiskURISupportedManaged)
	}
	return nil
}

func GetDiskName(diskURI string) (string, error) {
	matches := consts.ManagedDiskPathRE.FindStringSubmatch(diskURI)
	if len(matches) != 2 {
		return "", fmt.Errorf("could not get disk name from %s, correct format: %s", diskURI, consts.ManagedDiskPathRE)
	}
	return matches[1], nil
}

// GetResourceGroupFromURI returns resource grouped from URI
func GetResourceGroupFromURI(diskURI string) (string, error) {
	fields := strings.Split(diskURI, "/")
	if len(fields) != 9 || strings.ToLower(fields[3]) != "resourcegroups" {
		return "", fmt.Errorf("invalid disk URI: %s", diskURI)
	}
	return fields[4], nil
}

func GetCachingMode(attributes map[string]string) (compute.CachingTypes, error) {
	var (
		cachingMode v1.AzureDataDiskCachingMode
		maxShares   int
		err         error
	)

	for k, v := range attributes {
		if strings.EqualFold(k, consts.CachingModeField) {
			cachingMode = v1.AzureDataDiskCachingMode(v)
			break
		}
		// Check if disk is shared
		if strings.EqualFold(k, consts.MaxSharesField) {
			maxShares, err = strconv.Atoi(v)
			if err != nil || maxShares < 1 {
				maxShares = 1
			}
		}
	}

	cachingMode, err = NormalizeCachingMode(cachingMode, maxShares)
	return compute.CachingTypes(cachingMode), err
}

// isARMResourceID check whether resourceID is an ARM ResourceID
func IsARMResourceID(resourceID string) bool {
	id := strings.ToLower(resourceID)
	return strings.Contains(id, "/subscriptions/")
}

func GetValidCreationData(subscriptionID, resourceGroup, sourceResourceID, sourceType string) (compute.CreationData, error) {
	if sourceResourceID == "" {
		return compute.CreationData{
			CreateOption: compute.DiskCreateOptionEmpty,
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
			CreateOption: compute.DiskCreateOptionEmpty,
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
		CreateOption:     compute.DiskCreateOptionCopy,
		SourceResourceID: &sourceResourceID,
	}, nil
}

func IsCorruptedDir(dir string) bool {
	_, pathErr := mount.PathExists(dir)
	return pathErr != nil && mount.IsCorruptedMnt(pathErr)
}

// isAvailabilityZone returns true if the zone is in format of <region>-<zone-id>.
func IsValidAvailabilityZone(zone, region string) bool {
	return strings.HasPrefix(zone, fmt.Sprintf("%s-", region))
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

func IsValidVolumeCapabilities(volCaps []*csi.VolumeCapability, maxShares int) bool {
	if ok := IsValidAccessModes(volCaps); !ok {
		return false
	}
	for _, c := range volCaps {
		blockVolume := c.GetBlock()
		mountVolume := c.GetMount()
		accessMode := c.GetAccessMode().GetMode()

		if (blockVolume == nil && mountVolume == nil) ||
			(blockVolume != nil && mountVolume != nil) {
			return false
		}
		if mountVolume != nil && (accessMode == csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER ||
			accessMode == csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY ||
			accessMode == csi.VolumeCapability_AccessMode_MULTI_NODE_SINGLE_WRITER) {
			return false
		}
		if maxShares < 2 && (accessMode == csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER ||
			accessMode == csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY ||
			accessMode == csi.VolumeCapability_AccessMode_MULTI_NODE_SINGLE_WRITER) {
			return false
		}
	}
	return true
}

func IsValidAccessModes(volCaps []*csi.VolumeCapability) bool {
	hasSupport := func(cap *csi.VolumeCapability) bool {
		for _, c := range consts.VolumeCaps {
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

func IsMultiNodeAzVolumeCapabilityAccessMode(accessMode azdiskv1beta2.VolumeCapabilityAccessMode) bool {
	return accessMode == azdiskv1beta2.VolumeCapabilityAccessModeMultiNodeMultiWriter ||
		accessMode == azdiskv1beta2.VolumeCapabilityAccessModeMultiNodeSingleWriter ||
		accessMode == azdiskv1beta2.VolumeCapabilityAccessModeMultiNodeReaderOnly
}

func HasMultiNodeAzVolumeCapabilityAccessMode(volCaps []azdiskv1beta2.VolumeCapability) bool {
	for _, volCap := range volCaps {
		if IsMultiNodeAzVolumeCapabilityAccessMode(volCap.AccessMode) {
			return true
		}
	}

	return false
}

func IsMultiNodePersistentVolume(pv v1.PersistentVolume) bool {
	for _, accessMode := range pv.Spec.AccessModes {
		if accessMode == v1.ReadWriteMany || accessMode == v1.ReadOnlyMany {
			return true
		}
	}

	return false
}

func GetAzVolumeAttachmentName(volumeName string, nodeName string) string {
	return fmt.Sprintf("%s-%s-attachment", strings.ToLower(volumeName), strings.ToLower(nodeName))
}

func GetMaxSharesAndMaxMountReplicaCount(parameters map[string]string, isMultiNodeVolume bool) (maxShares, maxMountReplicaCount int) {
	maxShares = 1
	maxMountReplicaCount = -1

	for param, value := range parameters {
		if strings.EqualFold(param, consts.MaxSharesField) {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				klog.Warningf("failed to parse maxShares value (%s) to int, defaulting to 1: %v", value, err)
			} else {
				maxShares = parsed
			}
		} else if strings.EqualFold(param, consts.MaxMountReplicaCountField) {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				klog.Warningf("failed to parse maxMountReplica value (%s) to int, defaulting to 0: %v", value, err)
			} else {
				maxMountReplicaCount = parsed
			}
		}
	}

	if maxShares <= 0 {
		klog.Warningf("maxShares cannot be set smaller than 1... Defaulting current maxShares (%d) value to 1", maxShares)
		maxShares = 1
	}

	if isMultiNodeVolume {
		if maxMountReplicaCount > 0 {
			klog.Warning("maxMountReplicaCount is ignored for volumes that can be mounted to multiple nodes... Defaulting current maxMountReplicaCount (%d) to 0", maxMountReplicaCount)
		}

		maxMountReplicaCount = 0

		return
	}

	if maxShares-1 < maxMountReplicaCount {
		klog.Warningf("maxMountReplicaCount cannot be set larger than maxShares - 1... Defaulting current maxMountReplicaCount (%d) value to (%d)", maxMountReplicaCount, maxShares-1)
		maxMountReplicaCount = maxShares - 1
	} else if maxMountReplicaCount < 0 {
		maxMountReplicaCount = maxShares - 1
	}

	return
}

func GetAzVolumePhase(phase v1.PersistentVolumePhase) azdiskv1beta2.AzVolumePhase {
	return azdiskv1beta2.AzVolumePhase(phase)
}

func GetAzVolume(ctx context.Context, cachedClient client.Client, azDiskClient azdisk.Interface, azVolumeName, namespace string, useCache bool) (*azdiskv1beta2.AzVolume, error) {
	var azVolume *azdiskv1beta2.AzVolume
	var err error
	if useCache {
		azVolume = &azdiskv1beta2.AzVolume{}
		err = cachedClient.Get(ctx, types.NamespacedName{Name: azVolumeName, Namespace: namespace}, azVolume)
	} else {
		azVolume, err = azDiskClient.DiskV1beta2().AzVolumes(namespace).Get(ctx, azVolumeName, metav1.GetOptions{})
	}
	return azVolume, err
}

func ListAzVolumes(ctx context.Context, cachedClient client.Client, azDiskClient azdisk.Interface, namespace string, useCache bool) (azdiskv1beta2.AzVolumeList, error) {
	var azVolumeList *azdiskv1beta2.AzVolumeList
	var err error
	if useCache {
		azVolumeList = &azdiskv1beta2.AzVolumeList{}
		err = cachedClient.List(ctx, azVolumeList)
	} else {
		azVolumeList, err = azDiskClient.DiskV1beta2().AzVolumes(namespace).List(ctx, metav1.ListOptions{})
	}
	return *azVolumeList, err
}

func GetAzVolumeAttachment(ctx context.Context, cachedClient client.Client, azDiskClient azdisk.Interface, azVolumeAttachmentName, namespace string, useCache bool) (*azdiskv1beta2.AzVolumeAttachment, error) {
	var azVolumeAttachment *azdiskv1beta2.AzVolumeAttachment
	var err error
	if useCache {
		azVolumeAttachment = &azdiskv1beta2.AzVolumeAttachment{}
		err = cachedClient.Get(ctx, types.NamespacedName{Name: azVolumeAttachmentName, Namespace: namespace}, azVolumeAttachment)
	} else {
		azVolumeAttachment, err = azDiskClient.DiskV1beta2().AzVolumeAttachments(namespace).Get(ctx, azVolumeAttachmentName, metav1.GetOptions{})
	}
	return azVolumeAttachment, err
}

func ListAzVolumeAttachments(ctx context.Context, cachedClient client.Client, azDiskClient azdisk.Interface, namespace string, useCache bool) (azdiskv1beta2.AzVolumeAttachmentList, error) {
	var azVolumeAttachmentList *azdiskv1beta2.AzVolumeAttachmentList
	var err error
	if useCache {
		azVolumeAttachmentList = &azdiskv1beta2.AzVolumeAttachmentList{}
		err = cachedClient.List(ctx, azVolumeAttachmentList)
	} else {
		azVolumeAttachmentList, err = azDiskClient.DiskV1beta2().AzVolumeAttachments(namespace).List(ctx, metav1.ListOptions{})
	}
	return *azVolumeAttachmentList, err
}

func GetAzVolumeAttachmentState(volumeAttachmentStatus storagev1.VolumeAttachmentStatus) azdiskv1beta2.AzVolumeAttachmentAttachmentState {
	if volumeAttachmentStatus.Attached {
		return azdiskv1beta2.Attached
	} else if volumeAttachmentStatus.AttachError != nil {
		return azdiskv1beta2.AttachmentFailed
	} else if volumeAttachmentStatus.DetachError != nil {
		return azdiskv1beta2.DetachmentFailed
	} else {
		return azdiskv1beta2.AttachmentPending
	}
}

type UpdateCRIFunc func(client.Object) error

func UpdateCRIWithRetry(ctx context.Context, informerFactory azdiskinformers.SharedInformerFactory, cachedClient client.Client, azDiskClient azdisk.Interface, obj client.Object, updateFunc UpdateCRIFunc, maxNetRetry int, updateMode CRIUpdateMode) error {
	var err error
	objName := obj.GetName()
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	conditionFunc := func() error {
		var err error
		var copyForUpdate client.Object
		var objForUpdate client.Object
		switch target := obj.(type) {
		case *azdiskv1beta2.AzVolume:
			updateTarget := &azdiskv1beta2.AzVolume{}
			if informerFactory != nil {
				updateTarget, err = informerFactory.Disk().V1beta2().AzVolumes().Lister().AzVolumes(target.Namespace).Get(objName)
			} else if cachedClient != nil {
				err = cachedClient.Get(ctx, types.NamespacedName{Namespace: target.Namespace, Name: objName}, updateTarget)
			} else {
				updateTarget, err = azDiskClient.DiskV1beta2().AzVolumes(target.Namespace).Get(ctx, objName, metav1.GetOptions{})
			}
			if err == nil {
				objForUpdate = updateTarget
				copyForUpdate = updateTarget.DeepCopy()
			}
		case *azdiskv1beta2.AzVolumeAttachment:
			updateTarget := &azdiskv1beta2.AzVolumeAttachment{}
			if informerFactory != nil {
				updateTarget, err = informerFactory.Disk().V1beta2().AzVolumeAttachments().Lister().AzVolumeAttachments(target.Namespace).Get(objName)
			} else if cachedClient != nil {
				err = cachedClient.Get(ctx, types.NamespacedName{Namespace: target.Namespace, Name: objName}, updateTarget)
			} else {
				updateTarget, err = azDiskClient.DiskV1beta2().AzVolumeAttachments(target.Namespace).Get(ctx, objName, metav1.GetOptions{})
			}
			if err == nil {
				objForUpdate = updateTarget
				copyForUpdate = updateTarget.DeepCopy()
			}
		case *storagev1.VolumeAttachment:
			updateTarget := &storagev1.VolumeAttachment{}
			if cachedClient != nil {
				err = cachedClient.Get(ctx, types.NamespacedName{Namespace: target.Namespace, Name: objName}, updateTarget)
			} else {
				return status.Errorf(codes.Internal, "cannot update VolumeAttachment object if controller runtime client is not provided.")
			}
			if err == nil {
				objForUpdate = updateTarget
				copyForUpdate = updateTarget.DeepCopy()
			}
		default:
			return status.Errorf(codes.Internal, "object (%v) not supported.", reflect.TypeOf(target))
		}

		if err != nil {
			err = status.Errorf(codes.Internal, "failed to get object: %v", err)
			return err
		}

		if err = updateFunc(copyForUpdate); err != nil {
			return err
		}

		// if updateFunc doesn't change the object, don't bother making an update request
		if reflect.DeepEqual(objForUpdate, copyForUpdate) {
			return nil
		}

		switch target := copyForUpdate.(type) {
		case *azdiskv1beta2.AzVolume:
			if (updateMode&UpdateCRIStatus) != 0 && !reflect.DeepEqual(objForUpdate.(*azdiskv1beta2.AzVolume).Status, target.Status) {
				if _, err = azDiskClient.DiskV1beta2().AzVolumes(target.Namespace).UpdateStatus(ctx, target, metav1.UpdateOptions{}); err != nil {
					return err
				}
			}
			if (updateMode & UpdateCRI) != 0 {
				_, err = azDiskClient.DiskV1beta2().AzVolumes(target.Namespace).Update(ctx, target, metav1.UpdateOptions{})
			}
		case *azdiskv1beta2.AzVolumeAttachment:
			if (updateMode&UpdateCRIStatus) != 0 && !reflect.DeepEqual(objForUpdate.(*azdiskv1beta2.AzVolumeAttachment).Status, target.Status) {
				if _, err = azDiskClient.DiskV1beta2().AzVolumeAttachments(target.Namespace).UpdateStatus(ctx, target, metav1.UpdateOptions{}); err != nil {
					return err
				}
			}
			if (updateMode & UpdateCRI) != 0 {
				_, err = azDiskClient.DiskV1beta2().AzVolumeAttachments(target.Namespace).Update(ctx, target, metav1.UpdateOptions{})
			}

		case *storagev1.VolumeAttachment:
			if (updateMode&UpdateCRIStatus) != 0 && !reflect.DeepEqual(objForUpdate.(*storagev1.VolumeAttachment).Status, target.Status) {
				if err = cachedClient.Status().Update(ctx, target); err != nil {
					return err
				}
			}
			if (updateMode & UpdateCRI) != 0 {
				err = cachedClient.Update(ctx, target)
			}
		}
		return err
	}

	curRetry := 0
	maxRetry := maxNetRetry
	isRetriable := func(err error) bool {
		if k8serrors.IsConflict(err) {
			return true
		}
		if isNetError(err) {
			defer func() { curRetry++ }()
			return curRetry < maxRetry
		}
		return false
	}

	err = retry.OnError(
		wait.Backoff{
			Duration: consts.CRIUpdateRetryDuration,
			Factor:   consts.CRIUpdateRetryFactor,
			Steps:    consts.CRIUpdateRetryStep,
			Cap:      consts.DefaultBackoffCap,
		},
		isRetriable,
		conditionFunc,
	)

	// if encountered net error from api server unavailability, exit process
	if isNetError(err) {
		ExitOnNetError(err, maxRetry > 0 && curRetry >= maxRetry)
	}
	return err
}

func AppendToUpdateCRIFunc(updateFunc, newFunc UpdateCRIFunc) UpdateCRIFunc {
	if updateFunc != nil {
		innerFunc := updateFunc
		return func(obj client.Object) error {
			if err := innerFunc(obj); err != nil {
				return err
			}
			return newFunc(obj)
		}
	}
	return newFunc
}

func isFatalNetError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return isNetError(err)
}

func isNetError(err error) bool {
	return net.IsConnectionRefused(err) || net.IsConnectionReset(err) || net.IsTimeout(err) || net.IsProbableEOF(err)
}

func ExitOnNetError(err error, force bool) {
	if isFatalNetError(err) || (err != nil && force) {
		klog.Fatalf("encountered unrecoverable network error: %v \nexiting process...", err)
		os.Exit(1)
	}
}

// InsertDiskProperties: insert disk properties to map
func InsertDiskProperties(disk *compute.Disk, publishConext map[string]string) {
	if disk == nil || publishConext == nil {
		return
	}

	if disk.Sku != nil {
		publishConext[consts.SkuNameField] = string(disk.Sku.Name)
	}
	prop := disk.DiskProperties
	if prop != nil {
		publishConext[consts.NetworkAccessPolicyField] = string(prop.NetworkAccessPolicy)
		if prop.DiskIOPSReadWrite != nil {
			publishConext[consts.DiskIOPSReadWriteField] = strconv.Itoa(int(*prop.DiskIOPSReadWrite))
		}
		if prop.DiskMBpsReadWrite != nil {
			publishConext[consts.DiskMBPSReadWriteField] = strconv.Itoa(int(*prop.DiskMBpsReadWrite))
		}
		if prop.CreationData != nil && prop.CreationData.LogicalSectorSize != nil {
			publishConext[consts.LogicalSectorSizeField] = strconv.Itoa(int(*prop.CreationData.LogicalSectorSize))
		}
		if prop.Encryption != nil &&
			prop.Encryption.DiskEncryptionSetID != nil {
			publishConext[consts.DesIDField] = *prop.Encryption.DiskEncryptionSetID
		}
		if prop.MaxShares != nil {
			publishConext[consts.MaxSharesField] = strconv.Itoa(int(*prop.MaxShares))
		}
	}
}

func checkDiskName(diskName string) bool {
	length := len(diskName)

	for i, v := range diskName {
		if !(unicode.IsLetter(v) || unicode.IsDigit(v) || v == '_' || v == '.' || v == '-') ||
			(i == 0 && !(unicode.IsLetter(v) || unicode.IsDigit(v))) ||
			(i == length-1 && !(unicode.IsLetter(v) || unicode.IsDigit(v) || v == '_')) {
			return false
		}
	}

	return true
}

func SleepIfThrottled(err error, sleepSec int) {
	if strings.Contains(strings.ToLower(err.Error()), strings.ToLower(consts.TooManyRequests)) || strings.Contains(strings.ToLower(err.Error()), consts.ClientThrottled) {
		klog.Warningf("sleep %d more seconds, waiting for throttling complete", sleepSec)
		time.Sleep(time.Duration(sleepSec) * time.Second)
	}
}

// AddToMap requires arguments to be passed in <map, key1, value1, key2, value2, ...> format
func AddToMap(mmap map[string]string, entries ...string) map[string]string {
	if mmap == nil {
		mmap = map[string]string{}
	}
	// if odd number of entries are given, do not update and return instantly
	if len(entries)%2 == 1 {
		panic("AddToMap requires entries to be in key, value pair.")
	}
	for i := 0; i < len(entries); i = i + 2 {
		mmap[entries[i]] = entries[i+1]
	}
	return mmap
}

func GetFromMap(mmap map[string]string, key string) (value string, exists bool) {
	if mmap == nil {
		return
	}
	value, exists = mmap[key]
	return
}

func MapContains(mmap map[string]string, key string) bool {
	if mmap != nil {
		_, ok := mmap[key]
		return ok
	}
	return false
}
