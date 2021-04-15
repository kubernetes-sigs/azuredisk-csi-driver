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
	"regexp"
	"runtime"
	"strings"
	"unicode"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2020-12-01/compute"
	"github.com/pborman/uuid"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog"
	api "k8s.io/kubernetes/pkg/apis/core"
	"k8s.io/kubernetes/pkg/volume/util"

	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

const (
	azureStackCloud = "AZURESTACKCLOUD"

	azurePublicCloudDefaultStorageAccountType = compute.StandardSSDLRS
	azureStackCloudDefaultStorageAccountType  = compute.StandardLRS
	defaultAzureDataDiskCachingMode           = v1.AzureDataDiskCachingReadOnly

	// default IOPS Caps & Throughput Cap (MBps) per https://docs.microsoft.com/en-us/azure/virtual-machines/linux/disks-ultra-ssd
	// see https://docs.microsoft.com/en-us/rest/api/compute/disks/createorupdate#uri-parameters
	diskNameMinLength = 1
	diskNameMaxLength = 80
	// maxLength = 80 - (4 for ".vhd") = 76
	diskNameGenerateMaxLength = 76

	// minimum disk size is 1GiB
	MinimumDiskSizeGiB = 1

	// DefaultAzureCredentialFileEnv is the default azure credentials file env variable
	DefaultAzureCredentialFileEnv = "AZURE_CREDENTIAL_FILE"
	// DefaultCredFilePathLinux is default creds file for linux machine
	DefaultCredFilePathLinux = "/etc/kubernetes/azure.json"
	// DefaultCredFilePathWindows is default creds file for windows machine
	DefaultCredFilePathWindows = "C:\\k\\azure.json"

	// String constants used by CloudProvisioner
	ResourceNotFound         = "ResourceNotFound"
	ErrDiskNotFound          = "not found"
	SourceSnapshot           = "snapshot"
	SourceVolume             = "volume"
	DriverName               = "disk.csi.azure.com"
	CSIDriverMetricPrefix    = "azuredisk_csi_driver"
	CreatedForPVNameKey      = "kubernetes.io-created-for-pv-name"
	ResizeRequired           = "resizeRequired"
	SourceDiskSearchMaxDepth = 10
	CachingModeField         = "cachingmode"
	StorageAccountTypeField  = "storageaccounttype"
	StorageAccountField      = "storageaccount"
	SkuNameField             = "skuname"
	LocationField            = "location"
	ResourceGroupField       = "resourcegroup"
	DiskIOPSReadWriteField   = "diskiopsreadwrite"
	DiskMBPSReadWriteField   = "diskmbpsreadwrite"
	DiskNameField            = "diskname"
	DesIDField               = "diskencryptionsetid"
	TagsField                = "tags"
	MaxSharesField           = "maxshares"
	IncrementalField         = "incremental"
	LogicalSectorSizeField   = "logicalsectorsize"
	FSTypeField              = "fstype"
	KindField                = "kind"
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
func GetAzureCloudProvider(kubeClient clientset.Interface) (*azure.Cloud, error) {
	az := &azure.Cloud{}
	if kubeClient != nil {
		klog.V(2).Infof("reading cloud config from secret")
		az.KubeClient = kubeClient
		az.InitializeCloudFromSecret()
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
		if az, err = azure.NewCloudWithoutFeatureGates(f); err != nil {
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
		return fmt.Errorf("Inavlid DiskURI: %v, correct format: %v", diskURI, diskURISupportedManaged)
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
