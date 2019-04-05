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
	"strings"

	"k8s.io/kubernetes/pkg/cloudprovider/providers/azure"
	"k8s.io/kubernetes/pkg/util/mount"

	"github.com/container-storage-interface/spec/lib/go/csi"
	csicommon "github.com/kubernetes-sigs/azuredisk-csi-driver/pkg/csi-common"
	"k8s.io/klog"
)

const (
	errDiskNotFound = "not found"
)

var (
	managedDiskPathRE       = regexp.MustCompile(`.*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/disks/(.+)`)
	unmanagedDiskPathRE     = regexp.MustCompile(`http(?:.*)://(?:.*)/vhds/(.+)`)
	diskURISupportedManaged = []string{"/subscriptions/{sub-id}/resourcegroups/{group-name}/providers/microsoft.compute/disks/{disk-id}"}
	diskURISupportedBlob    = []string{"https://{account-name}.blob.core.windows.net/{container-name}/{disk-name}.vhd"}
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
			//csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT,
			//csi.ControllerServiceCapability_RPC_LIST_SNAPSHOTS,
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

func getResourceGroupFromDiskURI(diskURI string) (string, error) {
	fields := strings.Split(diskURI, "/")
	if len(fields) != 9 || fields[3] != "resourceGroups" {
		return "", fmt.Errorf("invalid disk URI: %s", diskURI)
	}
	return fields[4], nil
}

func (d *Driver) checkDiskExists(ctx context.Context, diskURI string) error {
	diskName, err := getDiskName(diskURI)
	if err != nil {
		return err
	}

	resourceGroup, err := getResourceGroupFromDiskURI(diskURI)
	if err != nil {
		return err
	}

	_, err = d.cloud.DisksClient.Get(ctx, resourceGroup, diskName)
	if err != nil {
		return err
	}

	return nil
}

func (d *Driver) validateDiskURI(diskURI string) error {
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
