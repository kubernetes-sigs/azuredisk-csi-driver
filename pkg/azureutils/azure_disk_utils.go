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
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"unicode"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2020-12-01/compute"
	"github.com/pborman/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog"
	"k8s.io/kubernetes/pkg/volume/util"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
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
	CleanUpAnnotation                = "disk.csi.azure.com/clean-up"
	VolumeDetachRequestAnnotation    = "disk.csi.azure.com/volume-detach-request"
	VolumeDeleteRequestAnnotation    = "disk.csi.azure.com/volume-delete-request"
	NodeNameLabel                    = "disk.csi.azure.com/node-name"
	VolumeNameLabel                  = "disk.csi.azure.com/volume-name"
	RoleLabel                        = "disk.csi.azure.com/requested-role"

	ControllerServiceAccountName      = "csi-azuredisk-controller-sa"
	ControllerClusterRoleName         = "azuredisk-external-provisioner-role"
	ControllerClusterRoleBindingName  = "azuredisk-csi-provisioner-binding"
	ReleaseNamespace                  = "kube-system"
	ControllerServiceAccountFinalizer = "disk.csi.azure.com/azuredisk-controller"
)

type ClientOperationMode int

const (
	Cached ClientOperationMode = iota
	Uncached
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

	if !consts.SupportedCachingModes.Has(string(cachingMode)) {
		return "", fmt.Errorf("azureDisk - %s is not supported cachingmode. Supported values are %s", cachingMode, consts.SupportedCachingModes.List())
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
		credFile, ok := os.LookupEnv(consts.DefaultAzureCredentialFileEnv)
		if ok && strings.TrimSpace(credFile) != "" {
			klog.V(2).Infof("%s env var set as %v", consts.DefaultAzureCredentialFileEnv, credFile)
		} else {
			if runtime.GOOS == "windows" {
				credFile = consts.DefaultCredFilePathWindows
			} else {
				credFile = consts.DefaultCredFilePathLinux
			}

			klog.V(2).Infof("use default %s env var: %v", consts.DefaultAzureCredentialFileEnv, credFile)
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
		return fmt.Errorf("invalid DiskURI: %v, correct format: %v", diskURI, consts.DiskURISupportedManaged)
	}
	return nil
}

func GetDiskNameFromAzureManagedDiskURI(diskURI string) (string, error) {
	matches := consts.ManagedDiskPathRE.FindStringSubmatch(diskURI)
	if len(matches) != 2 {
		return "", fmt.Errorf("could not get disk name from %s, correct format: %s", diskURI, consts.ManagedDiskPathRE)
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
		if strings.EqualFold(k, consts.CachingModeField) {
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

func GetAzVolume(ctx context.Context, cachedClient client.Client, azDiskClient azDiskClientSet.Interface, azVolumeName, namespace string, useCache bool) (*v1alpha1.AzVolume, error) {
	var azVolume *v1alpha1.AzVolume
	var err error
	if useCache {
		azVolume = &v1alpha1.AzVolume{}
		err = cachedClient.Get(ctx, types.NamespacedName{Name: azVolumeName, Namespace: namespace}, azVolume)
	} else {
		azVolume, err = azDiskClient.DiskV1alpha1().AzVolumes(namespace).Get(ctx, azVolumeName, metav1.GetOptions{})
	}
	return azVolume, err
}

func ListAzVolumes(ctx context.Context, cachedClient client.Client, azDiskClient azDiskClientSet.Interface, namespace string, useCache bool) (v1alpha1.AzVolumeList, error) {
	var azVolumeList *v1alpha1.AzVolumeList
	var err error
	if useCache {
		azVolumeList = &v1alpha1.AzVolumeList{}
		err = cachedClient.List(ctx, azVolumeList)
	} else {
		azVolumeList, err = azDiskClient.DiskV1alpha1().AzVolumes(namespace).List(ctx, metav1.ListOptions{})
	}
	return *azVolumeList, err
}

func GetAzVolumeAttachment(ctx context.Context, cachedClient client.Client, azDiskClient azDiskClientSet.Interface, azVolumeAttachmentName, namespace string, useCache bool) (*v1alpha1.AzVolumeAttachment, error) {
	var azVolumeAttachment *v1alpha1.AzVolumeAttachment
	var err error
	if useCache {
		azVolumeAttachment = &v1alpha1.AzVolumeAttachment{}
		err = cachedClient.Get(ctx, types.NamespacedName{Name: azVolumeAttachmentName, Namespace: namespace}, azVolumeAttachment)
	} else {
		azVolumeAttachment, err = azDiskClient.DiskV1alpha1().AzVolumeAttachments(namespace).Get(ctx, azVolumeAttachmentName, metav1.GetOptions{})
	}
	return azVolumeAttachment, err
}

func ListAzVolumeAttachments(ctx context.Context, cachedClient client.Client, azDiskClient azDiskClientSet.Interface, namespace string, useCache bool) (v1alpha1.AzVolumeAttachmentList, error) {
	var azVolumeAttachmentList *v1alpha1.AzVolumeAttachmentList
	var err error
	if useCache {
		azVolumeAttachmentList = &v1alpha1.AzVolumeAttachmentList{}
		err = cachedClient.List(ctx, azVolumeAttachmentList)
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

func UpdateCRIWithRetry(ctx context.Context, cachedClient client.Client, azDiskClient azDiskClientSet.Interface, obj interface{}, updateFunc func(interface{}) error) error {
	conditionFunc := func() error {
		var err error
		switch target := obj.(type) {
		case *v1alpha1.AzVolume:
			if cachedClient == nil {
				target, err = azDiskClient.DiskV1alpha1().AzVolumes(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
			} else {
				err = cachedClient.Get(ctx, types.NamespacedName{Namespace: target.Namespace, Name: target.Name}, target)
			}
			obj = target.DeepCopy()
		case *v1alpha1.AzVolumeAttachment:
			if cachedClient == nil {
				target, err = azDiskClient.DiskV1alpha1().AzVolumeAttachments(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
			} else {
				err = cachedClient.Get(ctx, types.NamespacedName{Namespace: target.Namespace, Name: target.Name}, target)
			}
			obj = target.DeepCopy()
		default:
			return status.Errorf(codes.Internal, "object (%v) not supported.", reflect.TypeOf(target))
		}

		klog.Infof("Initiating update with retry for %v (%s)", reflect.TypeOf(obj), obj.(client.Object).GetName())

		if err != nil {
			klog.Errorf("failed to get %v (%s): %v", reflect.TypeOf(obj), obj.(client.Object).GetName(), err)
			return err
		}

		if err = updateFunc(obj); err != nil {
			return err
		}

		switch target := obj.(type) {
		case *v1alpha1.AzVolume:
			if cachedClient == nil {
				_, err = azDiskClient.DiskV1alpha1().AzVolumes(target.Namespace).Update(ctx, target, metav1.UpdateOptions{})
			} else {
				err = cachedClient.Update(ctx, target)
			}
		case *v1alpha1.AzVolumeAttachment:
			if cachedClient == nil {
				_, err = azDiskClient.DiskV1alpha1().AzVolumeAttachments(target.Namespace).Update(ctx, target, metav1.UpdateOptions{})
			} else {
				err = cachedClient.Update(ctx, target)
			}
		}

		// log unrecoverable error
		if err != nil && !errors.IsConflict(err) {
			klog.Errorf("failed to update %v (%s): %v", reflect.TypeOf(obj), obj.(client.Object).GetName(), err)
		}

		return err
	}

	return retry.RetryOnConflict(
		wait.Backoff{
			Duration: consts.CRIUpdateRetryDuration,
			Factor:   consts.CRIUpdateRetryFactor,
			Steps:    consts.CRIUpdateRetryStep,
		},
		conditionFunc,
	)
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
