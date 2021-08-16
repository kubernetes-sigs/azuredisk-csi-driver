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
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"unicode"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2020-12-01/compute"
	"github.com/pborman/uuid"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog"
	api "k8s.io/kubernetes/pkg/apis/core"
	"k8s.io/kubernetes/pkg/volume/util"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	azureStackCloud = "AZURESTACKCLOUD"

	azurePublicCloudDefaultStorageAccountType = compute.StandardSSDLRS
	azureStackCloudDefaultStorageAccountType  = compute.StandardLRS
	defaultAzureDataDiskCachingMode           = v1.AzureDataDiskCachingNone

	// default IOPS Caps & Throughput Cap (MBps) per https://docs.microsoft.com/en-us/azure/virtual-machines/linux/disks-ultra-ssd
	// see https://docs.microsoft.com/en-us/rest/api/compute/disks/createorupdate#uri-parameters
	diskNameMinLength = 1
	// Reseting max length to 63 since the disk name is used in the label "volume-name"
	// of the kubernetes object and a label cannot have length greater than 63.
	// https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/
	diskNameMaxLength = 63
	// maxLength = 63 - (4 for ".vhd") = 59
	diskNameGenerateMaxLength = 59

	// minimum disk size is 1GiB
	MinimumDiskSizeGiB = 1

	// DefaultAzureCredentialFileEnv is the default azure credentials file env variable
	DefaultAzureCredentialFileEnv = "AZURE_CREDENTIAL_FILE"
	// DefaultCredFilePathLinux is default creds file for linux machine
	DefaultCredFilePathLinux = "/etc/kubernetes/azure.json"
	// DefaultCredFilePathWindows is default creds file for windows machine
	DefaultCredFilePathWindows = "C:\\k\\azure.json"

	// String constants used by CloudProvisioner
	ResourceNotFound          = "ResourceNotFound"
	ErrDiskNotFound           = "not found"
	SourceSnapshot            = "snapshot"
	SourceVolume              = "volume"
	DriverName                = "disk.csi.azure.com"
	CSIDriverMetricPrefix     = "azuredisk_csi_driver"
	ResizeRequired            = "resizeRequired"
	RequestedSizeGiB          = "requestedsizegib"
	SourceDiskSearchMaxDepth  = 10
	CachingModeField          = "cachingmode"
	StorageAccountTypeField   = "storageaccounttype"
	StorageAccountField       = "storageaccount"
	SkuNameField              = "skuname"
	LocationField             = "location"
	ResourceGroupField        = "resourcegroup"
	DiskIOPSReadWriteField    = "diskiopsreadwrite"
	DiskMBPSReadWriteField    = "diskmbpsreadwrite"
	DiskNameField             = "diskname"
	DesIDField                = "diskencryptionsetid"
	TagsField                 = "tags"
	MaxSharesField            = "maxshares"
	MaxMountReplicaCountField = "maxmountreplicacount"
	IncrementalField          = "incremental"
	LogicalSectorSizeField    = "logicalsectorsize"
	PerfProfileField          = "perfprofile"
	FSTypeField               = "fstype"
	KindField                 = "kind"
	NetworkAccessPolicyField  = "networkaccesspolicy"
	DiskAccessIDField         = "diskaccessid"
	EnableBurstingField       = "enablebursting"
	TrueValue                 = "true"

	// CRDs specific constants
	PartitionLabel = "azdrivernodes.disk.csi.azure.com/partition"
	// 1. AzVolumeAttachmentFinalizer for AzVolumeAttachment objects handles deletion of AzVolumeAttachment CRIs
	// 2. AzVolumeAttachmentFinalizer for AzVolume prevents AzVolume CRI from being deleted before all AzVolumeAttachments attached to that volume is deleted as well
	AzVolumeAttachmentFinalizer = "disk.csi.azure.com/azvolumeattachment-finalizer"
	AzVolumeFinalizer           = "disk.csi.azure.com/azvolume-finalizer"
	// ControllerFinalizer is a finalizer added to the pod running Azuredisk driver controller
	// to prevent the pod deletion until clean up is completed
	ControllerFinalizer              = "disk.csi.azure.com/azuredisk-finalizer"
	VolumeAttachmentExistsAnnotation = "disk.csi.azure.com/volume-attachment"
	VolumeDetachRequestAnnotation    = "disk.csi.azure.com/volume-detach-request"
	VolumeDeleteRequestAnnotation    = "disk.csi.azure.com/volume-delete-request"
	NodeNameLabel                    = "node-name"
	VolumeNameLabel                  = "volume-name"
	RoleLabel                        = "requested-role"

	// ZRS specific constants
	WellKnownTopologyKey = "topology.kubernetes.io/zone"

	PVCNameKey      = "csi.storage.k8s.io/pvc/name"
	PVCNamespaceKey = "csi.storage.k8s.io/pvc/namespace"
	PVNameKey       = "csi.storage.k8s.io/pv/name"

	PVCNameTag      = "kubernetes.io-created-for-pvc-name"
	PVCNamespaceTag = "kubernetes.io-created-for-pvc-namespace"
	PVNameTag       = "kubernetes.io-created-for-pv-name"

	ControllerServiceAccountName      = "csi-azuredisk-controller-sa"
	ControllerClusterRoleName         = "azuredisk-external-provisioner-role"
	ControllerClusterRoleBindingName  = "azuredisk-csi-provisioner-binding"
	ReleaseNamespace                  = "kube-system"
	ControllerServiceAccountFinalizer = "disk.csi.azure.com/azuredisk-controller"
)

var (
	supportedCachingModes = sets.NewString(
		string(api.AzureDataDiskCachingNone),
		string(api.AzureDataDiskCachingReadOnly),
		string(api.AzureDataDiskCachingReadWrite))

	managedDiskPathRE       = regexp.MustCompile(`(?i).*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/disks/(.+)`)
	diskURISupportedManaged = []string{"/subscriptions/{sub-id}/resourcegroups/{group-name}/providers/microsoft.compute/disks/{disk-id}"}
	diskSnapshotPathRE      = regexp.MustCompile(`(?i).*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/snapshots/(.+)`)
)

func IsAzureStackCloud(cloud string, disableAzureStackCloud bool) bool {
	return !disableAzureStackCloud && strings.EqualFold(cloud, azureStackCloud)
}

// gets the AzVolume cluster client
func GetAzDiskClient(config *rest.Config) (
	*azDiskClientSet.Clientset,
	error) {

	azDiskClient, err := azDiskClientSet.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return azDiskClient, nil
}

func NormalizeAzureStorageAccountType(storageAccountType, cloud string, disableAzureStackCloud bool) (compute.DiskStorageAccountTypes, error) {
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

func NormalizeAzureDataDiskCachingMode(cachingMode v1.AzureDataDiskCachingMode) (v1.AzureDataDiskCachingMode, error) {
	if cachingMode == "" {
		return defaultAzureDataDiskCachingMode, nil
	}

	if !supportedCachingModes.Has(string(cachingMode)) {
		return "", fmt.Errorf("azureDisk - %s is not supported cachingmode. Supported values are %s", cachingMode, supportedCachingModes.List())
	}

	return cachingMode, nil
}

func NormalizeNetworkAccessPolicy(networkAccessPolicy string) (compute.NetworkAccessPolicy, error) {
	if networkAccessPolicy == "" {
		return compute.AllowAll, nil
	}
	policy := compute.NetworkAccessPolicy(networkAccessPolicy)
	for _, s := range compute.PossibleNetworkAccessPolicyValues() {
		if policy == s {
			return policy, nil
		}
	}
	return "", fmt.Errorf("azureDisk - %s is not supported NetworkAccessPolicy. Supported values are %s", networkAccessPolicy, compute.PossibleNetworkAccessPolicyValues())
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
func GetValidDiskName(volumeName string) string {
	diskName := volumeName
	if len(diskName) > diskNameMaxLength {
		diskName = diskName[0:diskNameMaxLength]
		klog.Warningf("since the maximum volume name length is %d, so it is truncated as (%q)", diskNameMaxLength, diskName)
	}
	if !checkDiskName(diskName) || len(diskName) < diskNameMinLength {
		// todo: get cluster name
		diskName = util.GenerateVolumeName("pvc-disk", uuid.NewUUID().String(), diskNameGenerateMaxLength)
		klog.Warningf("the requested volume name (%q) is invalid, so it is regenerated as (%q)", volumeName, diskName)
	}

	return diskName
}

// GetCloudProvider get Azure Cloud Provider
func GetAzureCloudProvider(kubeClient clientset.Interface, secretName string, secretNamespace string, userAgent string) (*azure.Cloud, error) {
	az := &azure.Cloud{
		InitSecretConfig: azure.InitSecretConfig{
			SecretName:      secretName,
			SecretNamespace: secretNamespace,
			CloudConfigKey:  "cloud-config",
		},
	}
	if kubeClient != nil {
		klog.V(2).Infof("reading cloud config from secret")
		az.KubeClient = kubeClient
		if err := az.InitializeCloudFromSecret(); err != nil {
			klog.V(2).Infof("InitializeCloudFromSecret failed with error: %v", err)
		}
	}

	if az.TenantID == "" || az.SubscriptionID == "" || az.ResourceGroup == "" {
		klog.V(2).Infof("could not read cloud config from secret")
		credFile, ok := os.LookupEnv(DefaultAzureCredentialFileEnv)
		if ok && strings.TrimSpace(credFile) != "" {
			klog.V(2).Infof("%s env var set as %v", DefaultAzureCredentialFileEnv, credFile)
		} else {
			if runtime.GOOS == "windows" {
				credFile = DefaultCredFilePathWindows
			} else {
				credFile = DefaultCredFilePathLinux
			}

			klog.V(2).Infof("use default %s env var: %v", DefaultAzureCredentialFileEnv, credFile)
		}

		f, err := os.Open(credFile)
		if err != nil {
			klog.Errorf("Failed to load config from file: %s", credFile)
			return nil, fmt.Errorf("Failed to load config from file: %s, cloud not get azure cloud provider", credFile)
		}
		defer f.Close()

		klog.V(2).Infof("read cloud config from file: %s successfully", credFile)
		if az, err = azure.NewCloudWithoutFeatureGates(f, false); err != nil {
			return az, err
		}
	}

	// reassign kubeClient
	if kubeClient != nil && az.KubeClient == nil {
		az.KubeClient = kubeClient
	}
	return az, nil
}

func IsValidDiskURI(diskURI string) error {
	if strings.Index(strings.ToLower(diskURI), "/subscriptions/") != 0 {
		return fmt.Errorf("Invalid DiskURI: %v, correct format: %v", diskURI, diskURISupportedManaged)
	}
	return nil
}

func GetDiskNameFromAzureManagedDiskURI(diskURI string) (string, error) {
	matches := managedDiskPathRE.FindStringSubmatch(diskURI)
	if len(matches) != 2 {
		return "", fmt.Errorf("could not get disk name from %s, correct format: %s", diskURI, managedDiskPathRE)
	}
	return matches[1], nil
}

// GetResourceGroupFromAzureManagedDiskURI returns resource groupd from URI
func GetResourceGroupFromAzureManagedDiskURI(diskURI string) (string, error) {
	fields := strings.Split(diskURI, "/")
	if len(fields) != 9 || strings.ToLower(fields[3]) != "resourcegroups" {
		return "", fmt.Errorf("invalid disk URI: %s", diskURI)
	}
	return fields[4], nil
}

func GetCachingMode(attributes map[string]string) (compute.CachingTypes, error) {
	var (
		cachingMode v1.AzureDataDiskCachingMode
		err         error
	)

	for k, v := range attributes {
		if strings.EqualFold(k, CachingModeField) {
			cachingMode = v1.AzureDataDiskCachingMode(v)
			break
		}
	}

	cachingMode, err = NormalizeAzureDataDiskCachingMode(cachingMode)
	return compute.CachingTypes(cachingMode), err
}

// isARMResourceID check whether resourceID is an ARM ResourceID
func IsARMResourceID(resourceID string) bool {
	id := strings.ToLower(resourceID)
	return strings.Contains(id, "/subscriptions/")
}

// isAvailabilityZone returns true if the zone is in format of <region>-<zone-id>.
func IsValidAvailabilityZone(zone, region string) bool {
	return strings.HasPrefix(zone, fmt.Sprintf("%s-", region))
}

func GetAzVolumeAttachmentName(volumeName string, nodeName string) string {
	return fmt.Sprintf("%s-%s-attachment", strings.ToLower(volumeName), strings.ToLower(nodeName))
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

func GetMaxSharesAndMaxMountReplicaCount(parameters map[string]string) (int, int) {
	maxShares := 1
	maxMountReplicaCount := -1
	for param, value := range parameters {
		if strings.EqualFold(param, MaxSharesField) {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				klog.Warningf("failed to parse maxShares value (%s) to int, defaulting to 1: %v", value, err)
			} else {
				maxShares = parsed
			}
		} else if strings.EqualFold(param, MaxMountReplicaCountField) {
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
	if maxShares-1 < maxMountReplicaCount {
		klog.Warningf("maxMountReplicaCount cannot be set larger than maxShares - 1... Defaulting current maxMountReplicaCount (%d) value to (%d)", maxMountReplicaCount, maxShares-1)
		maxMountReplicaCount = maxShares - 1
	} else if maxMountReplicaCount < 0 {
		maxMountReplicaCount = maxShares - 1
	}

	return maxShares, maxMountReplicaCount
}

func GetAzVolumePhase(phase v1.PersistentVolumePhase) v1alpha1.AzVolumePhase {
	return v1alpha1.AzVolumePhase(phase)
}

func GetAzVolume(ctx context.Context, client client.Client, azDiskClient azDiskClientSet.Interface, azVolumeName, namespace string, useCache bool) (*v1alpha1.AzVolume, error) {
	var azVolume *v1alpha1.AzVolume
	var err error
	if useCache {
		azVolume = &v1alpha1.AzVolume{}
		err = client.Get(ctx, types.NamespacedName{Name: azVolumeName, Namespace: namespace}, azVolume)
	} else {
		azVolume, err = azDiskClient.DiskV1alpha1().AzVolumes(namespace).Get(ctx, azVolumeName, metav1.GetOptions{})
	}
	return azVolume, err
}

func ListAzVolumes(ctx context.Context, client client.Client, azDiskClient azDiskClientSet.Interface, namespace string, useCache bool) (v1alpha1.AzVolumeList, error) {
	var azVolumeList *v1alpha1.AzVolumeList
	var err error
	if useCache {
		azVolumeList = &v1alpha1.AzVolumeList{}
		err = client.List(ctx, azVolumeList)
	} else {
		azVolumeList, err = azDiskClient.DiskV1alpha1().AzVolumes(namespace).List(ctx, metav1.ListOptions{})
	}
	return *azVolumeList, err
}

func GetAzVolumeAttachment(ctx context.Context, client client.Client, azDiskClient azDiskClientSet.Interface, azVolumeAttachmentName, namespace string, useCache bool) (*v1alpha1.AzVolumeAttachment, error) {
	var azVolumeAttachment *v1alpha1.AzVolumeAttachment
	var err error
	if useCache {
		azVolumeAttachment = &v1alpha1.AzVolumeAttachment{}
		err = client.Get(ctx, types.NamespacedName{Name: azVolumeAttachmentName, Namespace: namespace}, azVolumeAttachment)
	} else {
		azVolumeAttachment, err = azDiskClient.DiskV1alpha1().AzVolumeAttachments(namespace).Get(ctx, azVolumeAttachmentName, metav1.GetOptions{})
	}
	return azVolumeAttachment, err
}

func ListAzVolumeAttachments(ctx context.Context, client client.Client, azDiskClient azDiskClientSet.Interface, namespace string, useCache bool) (v1alpha1.AzVolumeAttachmentList, error) {
	var azVolumeAttachmentList *v1alpha1.AzVolumeAttachmentList
	var err error
	if useCache {
		azVolumeAttachmentList = &v1alpha1.AzVolumeAttachmentList{}
		err = client.List(ctx, azVolumeAttachmentList)
	} else {
		azVolumeAttachmentList, err = azDiskClient.DiskV1alpha1().AzVolumeAttachments(namespace).List(ctx, metav1.ListOptions{})
	}
	return *azVolumeAttachmentList, err
}

func GetAzVolumeAttachmentState(volumeAttachmentStatus storagev1.VolumeAttachmentStatus) v1alpha1.AzVolumeAttachmentAttachmentState {
	if volumeAttachmentStatus.Attached {
		return v1alpha1.Attached
	} else if volumeAttachmentStatus.AttachError != nil {
		return v1alpha1.AttachmentFailed
	} else if volumeAttachmentStatus.DetachError != nil {
		return v1alpha1.DetachmentFailed
	} else {
		return v1alpha1.AttachmentPending
	}
}
