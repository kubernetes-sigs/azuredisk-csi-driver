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
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2019-03-01/compute"
	"github.com/Azure/go-autorest/autorest/to"
	"github.com/container-storage-interface/spec/lib/go/csi"

	csicommon "github.com/kubernetes-sigs/azuredisk-csi-driver/pkg/csi-common"
	kwait "k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog"
	"k8s.io/kubernetes/pkg/util/mount"
	"k8s.io/legacy-cloud-providers/azure"
)

const (
	errDiskNotFound = "not found"
	// default IOPS Caps & Throughput Cap (MBps) per https://docs.microsoft.com/en-us/azure/virtual-machines/linux/disks-ultra-ssd
	defaultDiskIOPSReadWrite = 500
	defaultDiskMBpsReadWrite = 100
)

var (
	managedDiskPathRE       = regexp.MustCompile(`.*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/disks/(.+)`)
	unmanagedDiskPathRE     = regexp.MustCompile(`http(?:.*)://(?:.*)/vhds/(.+)`)
	diskSnapshotPathRE      = regexp.MustCompile(`.*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/snapshots/(.+)`)
	diskURISupportedManaged = []string{"/subscriptions/{sub-id}/resourcegroups/{group-name}/providers/microsoft.compute/disks/{disk-id}"}
	diskURISupportedBlob    = []string{"https://{account-name}.blob.core.windows.net/{container-name}/{disk-name}.vhd"}

	defaultBackOff = kwait.Backoff{
		Steps:    20,
		Duration: 2 * time.Second,
		Factor:   1.5,
		Jitter:   0.0,
	}
)

// Driver implements all interfaces of CSI drivers
type Driver struct {
	csicommon.CSIDriver
	cloud   *azure.Cloud
	mounter *mount.SafeFormatAndMount
}

// NewDriver Creates a NewCSIDriver object. Assumes vendor version is equal to driver version &
// does not support optional driver plugin info manifest field. Refer to CSI spec for more details.
func NewDriver(nodeID string) *Driver {
	driver := Driver{}
	driver.Name = driverName
	driver.Version = driverVersion
	driver.NodeID = nodeID
	return &driver
}

// Run driver initialization
func (d *Driver) Run(endpoint string) {
	versionMeta, err := GetVersionYAML()
	if err != nil {
		klog.Fatalf("%v", err)
	}
	klog.Infof("\nDRIVER INFORMATION:\n-------------------\n%s\n\nStreaming logs below:", versionMeta)
	cloud, err := GetCloudProvider()
	if err != nil {
		klog.Fatalln("failed to get Azure Cloud Provider")
	}
	d.cloud = cloud

	d.mounter = &mount.SafeFormatAndMount{
		Interface: mount.New(""),
		Exec:      mount.NewOsExec(),
	}

	d.AddControllerServiceCapabilities(
		[]csi.ControllerServiceCapability_RPC_Type{
			csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
			csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
			csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT,
			csi.ControllerServiceCapability_RPC_LIST_SNAPSHOTS,
		})
	d.AddVolumeCapabilityAccessModes([]csi.VolumeCapability_AccessMode_Mode{csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER})
	d.AddNodeServiceCapabilities([]csi.NodeServiceCapability_RPC_Type{
		csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
	})

	s := csicommon.NewNonBlockingGRPCServer()
	// Driver d act as IdentityServer, ControllerServer and NodeServer
	s.Start(endpoint, d, d, d)
	s.Wait()
}

func isManagedDisk(diskURI string) bool {
	if len(diskURI) > 4 && strings.ToLower(diskURI[:4]) == "http" {
		return false
	}
	return true
}

func getDiskName(diskURI string) (string, error) {
	diskPathRE := managedDiskPathRE
	if !isManagedDisk(diskURI) {
		diskPathRE = unmanagedDiskPathRE
	}

	matches := diskPathRE.FindStringSubmatch(diskURI)
	if len(matches) != 2 {
		return "", fmt.Errorf("could not get disk name from %s, correct format: %s", diskURI, diskPathRE)
	}
	return matches[1], nil
}

func getSnapshotName(snapshotURI string) (string, error) {
	matches := diskSnapshotPathRE.FindStringSubmatch(snapshotURI)
	if len(matches) != 2 {
		return "", fmt.Errorf("could not get snapshot name from %s, correct format: %s", snapshotURI, diskSnapshotPathRE)
	}
	return matches[1], nil
}

func getResourceGroupFromURI(diskURI string) (string, error) {
	fields := strings.Split(diskURI, "/")
	if len(fields) != 9 || strings.ToLower(fields[3]) != "resourcegroups" {
		return "", fmt.Errorf("invalid disk URI: %s", diskURI)
	}
	return fields[4], nil
}

func (d *Driver) checkDiskExists(ctx context.Context, diskURI string) error {
	diskName, err := getDiskName(diskURI)
	if err != nil {
		return err
	}

	resourceGroup, err := getResourceGroupFromURI(diskURI)
	if err != nil {
		return err
	}

	_, err = d.cloud.DisksClient.Get(ctx, resourceGroup, diskName)
	if err != nil {
		return err
	}

	return nil
}

func isValidDiskURI(diskURI string) error {
	if isManagedDisk(diskURI) {
		if strings.Index(diskURI, "/subscriptions/") != 0 {
			return fmt.Errorf("Inavlid DiskURI: %v, correct format: %v", diskURI, diskURISupportedManaged)
		}
	} else {
		if strings.Index(diskURI, "https://") != 0 {
			return fmt.Errorf("Inavlid DiskURI: %v, correct format: %v", diskURI, diskURISupportedBlob)
		}
	}

	return nil
}

// ManagedDiskOptions specifies the options of managed disks.
type ManagedDiskOptions struct {
	// The name of the disk.
	DiskName string
	// The size in GB.
	SizeGB int
	// The name of PVC.
	PVCName string
	// The name of resource group.
	ResourceGroup string
	// The AvailabilityZone to create the disk.
	AvailabilityZone string
	// The tags of the disk.
	Tags map[string]string
	// The SKU of storage account.
	StorageAccountType compute.DiskStorageAccountTypes
	// IOPS Caps for UltraSSD disk
	DiskIOPSReadWrite string
	// Throughput Cap (MBps) for UltraSSD disk
	DiskMBpsReadWrite string
	// if SourceResourceID is not empty, then it's a disk copy operation(for snapshot)
	SourceResourceID string
}

//CreateManagedDisk : create managed disk
func (d *Driver) CreateManagedDisk(ctx context.Context, options *ManagedDiskOptions) (string, error) {
	var err error
	klog.V(4).Infof("azureDisk - creating new managed Name:%s StorageAccountType:%s Size:%v", options.DiskName, options.StorageAccountType, options.SizeGB)

	var createZones *[]string
	if len(options.AvailabilityZone) > 0 {
		zoneList := []string{d.cloud.GetZoneID(options.AvailabilityZone)}
		createZones = &zoneList
	}

	// insert original tags to newTags
	newTags := make(map[string]*string)
	azureDDTag := "kubernetes-azure-dd"
	newTags["created-by"] = &azureDDTag
	if options.Tags != nil {
		for k, v := range options.Tags {
			// Azure won't allow / (forward slash) in tags
			newKey := strings.Replace(k, "/", "-", -1)
			newValue := strings.Replace(v, "/", "-", -1)
			newTags[newKey] = &newValue
		}
	}

	diskSizeGB := int32(options.SizeGB)
	diskSku := compute.DiskStorageAccountTypes(options.StorageAccountType)
	creationData := compute.CreationData{CreateOption: compute.Empty}
	if options.SourceResourceID != "" {
		creationData = compute.CreationData{
			CreateOption:     compute.Copy,
			SourceResourceID: &options.SourceResourceID,
		}
	}

	diskProperties := compute.DiskProperties{
		DiskSizeGB:   &diskSizeGB,
		CreationData: &creationData,
	}

	if diskSku == compute.UltraSSDLRS {
		diskIOPSReadWrite := int64(defaultDiskIOPSReadWrite)
		if options.DiskIOPSReadWrite != "" {
			v, err := strconv.Atoi(options.DiskIOPSReadWrite)
			if err != nil {
				return "", fmt.Errorf("AzureDisk - failed to parse DiskIOPSReadWrite: %v", err)
			}
			diskIOPSReadWrite = int64(v)
		}
		diskProperties.DiskIOPSReadWrite = to.Int64Ptr(diskIOPSReadWrite)

		diskMBpsReadWrite := int32(defaultDiskMBpsReadWrite)
		if options.DiskMBpsReadWrite != "" {
			v, err := strconv.Atoi(options.DiskMBpsReadWrite)
			if err != nil {
				return "", fmt.Errorf("AzureDisk - failed to parse DiskMBpsReadWrite: %v", err)
			}
			diskMBpsReadWrite = int32(v)
		}
		diskProperties.DiskMBpsReadWrite = to.Int32Ptr(diskMBpsReadWrite)
	} else {
		if options.DiskIOPSReadWrite != "" {
			return "", fmt.Errorf("AzureDisk - DiskIOPSReadWrite parameter is only applicable in UltraSSD_LRS disk type")
		}
		if options.DiskMBpsReadWrite != "" {
			return "", fmt.Errorf("AzureDisk - DiskMBpsReadWrite parameter is only applicable in UltraSSD_LRS disk type")
		}
	}

	model := compute.Disk{
		Location: &d.cloud.Location,
		Tags:     newTags,
		Zones:    createZones,
		Sku: &compute.DiskSku{
			Name: diskSku,
		},
		DiskProperties: &diskProperties,
	}

	if options.ResourceGroup == "" {
		options.ResourceGroup = d.cloud.ResourceGroup
	}

	_, err = d.cloud.DisksClient.CreateOrUpdate(ctx, options.ResourceGroup, options.DiskName, model)
	if err != nil {
		return "", err
	}

	diskID := ""

	err = kwait.ExponentialBackoff(defaultBackOff, func() (bool, error) {
		provisionState, id, err := d.cloud.GetDisk(options.ResourceGroup, options.DiskName)
		diskID = id
		// We are waiting for provisioningState==Succeeded
		// We don't want to hand-off managed disks to k8s while they are
		//still being provisioned, this is to avoid some race conditions
		if err != nil {
			return false, err
		}
		if strings.ToLower(provisionState) == "succeeded" {
			return true, nil
		}
		return false, nil
	})

	if err != nil {
		klog.V(2).Infof("azureDisk - created new MD Name:%s StorageAccountType:%s Size:%v but was unable to confirm provisioningState in poll process", options.DiskName, options.StorageAccountType, options.SizeGB)
	} else {
		klog.V(2).Infof("azureDisk - created new MD Name:%s StorageAccountType:%s Size:%v", options.DiskName, options.StorageAccountType, options.SizeGB)
	}

	return diskID, nil
}
