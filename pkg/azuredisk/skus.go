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

package azuredisk

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"os"
	"strconv"
	"strings"

	compute "github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2020-12-01/compute"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	MaxValueOfMaxSharesCapability        = "maxvalueofmaxshares"
	MaxBurstIopsCapability               = "maxburstiops"
	MaxIOpsCapability                    = "maxiops"
	MaxBandwidthMBpsCapability           = "maxbandwidthmbps"
	MaxBurstBandwidthMBpsCapability      = "maxburstbandwidthmbps"
	MaxSizeGiBCapability                 = "maxsizegib"
	UncachedDiskIOPSCapability           = "uncacheddiskiops"
	UncachedDiskBytesPerSecondCapability = "uncacheddiskbytespersecond"
	MaxDataDiskCountCapability           = "maxdatadiskcount"
	VCPUsCapability                      = "vcpus"
)

// ResourceSku describes an available  SKU.
type SkuInfo struct {
	// ResourceType - READ-ONLY; The type of resource the SKU applies to.
	ResourceType *string `json:"resourceType,omitempty"`
	// Name - READ-ONLY; The name of SKU.
	Name *string `json:"name,omitempty"`
	// Tier - READ-ONLY; Specifies the tier of virtual machines in a scale set.<br /><br /> Possible Values:<br /><br /> **Standard**<br /><br /> **Basic**
	Tier *string `json:"tier,omitempty"`
	// Size - READ-ONLY; The Size of the SKU.
	Size *string `json:"size,omitempty"`
	// Capabilities - READ-ONLY; A name value pair to describe the capability.
	Capabilities *[]compute.ResourceSkuCapabilities `json:"capabilities,omitempty"`
}

// NodeInfo stores VM/Node specific static information
// VM information is present in sku.json in below format
// {
// 	"resourceType": "virtualmachines",
// 	"name": "Standard_E16-4ds_v4",
// 	"tier": "Standard",
// 	"size": "E16-4ds_v4",
// 	"capabilities": [
// 	 {
// 	  "name": "vCPUs",
// 	  "value": "16"
// 	 },
// 	 {
// 	  "name": "MemoryGB",
// 	  "value": "128"
// 	 },
// 	 {
// 	  "name": "MaxDataDiskCount",
// 	  "value": "32"
// 	 },
// 	 {
// 	  "name": "UncachedDiskIOPS",
// 	  "value": "25600"
// 	 },
// 	 {
// 	  "name": "UncachedDiskBytesPerSecond",
// 	  "value": "402653184"
// 	 }
// 	]
// }
type NodeInfo struct {
	skuName          *string
	zone             *string
	region           *string
	maxDataDiskCount int
	vcpus            int
	maxBurstIops     int
	maxIops          int
	maxBwMbps        int
	maxBurstBwMbps   int
}

// DiskSkuInfo stores disk sku information
// disk sku information is present in sku.json in below format
// {
// 	"resourceType": "disks",
// 	"name": "Premium_LRS",
// 	"tier": "Premium",
// 	"size": "P4",
// 	"capabilities": [
// 	 {
// 	  "name": "MaxSizeGiB",
// 	  "value": "32"
// 	 {
// 	  "name": "MaxIOps",
// 	  "value": "120"
// 	 },
// 	 {
// 	  "name": "MaxBandwidthMBps",
// 	  "value": "25"
// 	 },
// 	 {
// 	  "name": "MaxValueOfMaxShares",
// 	  "value": "1"
// 	 },
// 	 {
// 	  "name": "MaxBurstIops",
// 	  "value": "3500"
// 	 },
// 	 {
// 	  "name": "MaxBurstBandwidthMBps",
// 	  "value": "170"
// 	 }
// 	]
//}
type DiskSkuInfo struct {
	storageAccountType *string
	storageTier        *string
	diskSize           *string
	maxAllowedShares   int
	maxBurstIops       int
	maxIops            int
	maxBwMbps          int
	maxBurstBwMbps     int
	maxSizeGiB         int
}

// PopulateNodeAndSkuInfo populates Node and Sku related
// information in memory
func PopulateNodeAndSkuInfo(d DriverCore) error {
	d.nodeInfo = &NodeInfo{}

	instances, ok := d.cloud.Instances()
	if !ok {
		return status.Error(codes.Internal, "Failed to get instances from cloud provider")
	}

	instanceType, err := instances.InstanceType(context.TODO(), types.NodeName(d.NodeID))
	if err != nil {
		klog.Errorf("Failed to get instance type from Azure cloud provider, nodeName: %v, error: %v", d.NodeID, err)
		return err
	}

	d.nodeInfo.skuName = &instanceType

	zone, err := d.cloud.GetZone(context.TODO())
	if err != nil {
		klog.Errorf("Failed to get zone from Azure cloud provider, nodeName: %v, error: %v", d.NodeID, err)
		return err
	}
	d.nodeInfo.zone = &zone.FailureDomain
	d.nodeInfo.region = &zone.Region

	err = populateSkuMap(d)
	if err != nil {
		klog.Errorf("Could populate sku information. Error: %v", err)
		return err
	}

	return nil
}

// populateSkuMap populates the sku map from the sku json file
func populateSkuMap(d DriverCore) (err error) {
	var skus []SkuInfo
	diskSkusFullJSON, err := os.Open(d.skusFilePath)
	if err != nil {
		klog.Errorf("Could read file. Error: %v, FilePath: %s", err, d.skusFilePath)
		return err
	}
	defer diskSkusFullJSON.Close()

	byteValue, _ := ioutil.ReadAll(diskSkusFullJSON)

	err = json.Unmarshal([]byte(byteValue), &skus)
	if err != nil {
		klog.Errorf("Could unmarshal json file. Error: %v, FilePath: %s", err, d.skusFilePath)
		return err
	}

	d.diskSkuInfoMap = map[string]map[string]DiskSkuInfo{}
	for _, sku := range skus {
		resType := strings.ToLower(*sku.ResourceType)
		if resType == "disks" {
			account := strings.ToLower(*sku.Name)
			diskSize := strings.ToLower(*sku.Size)
			if _, ok := d.diskSkuInfoMap[account]; !ok {
				d.diskSkuInfoMap[account] = map[string]DiskSkuInfo{}
			}
			d.diskSkuInfoMap[account][diskSize], err = sku.GetDiskCapabilities()
			if err != nil {
				klog.Errorf("Failed to get disk capabilities for disk %s %s %s. Error: %v", sku.Name, sku.Size, sku.Tier, err)
				return err
			}
		} else if resType == "virtualmachines" {
			if strings.EqualFold(*d.nodeInfo.skuName, *sku.Name) {
				err = sku.PopulateNodeCapabilities(d.nodeInfo)
				if err != nil {
					klog.Errorf("Failed to populate node capabilities. Error: %v", err)
					return err
				}
			}
		}
	}
	return nil
}

// populateNodeCapabilities populates node capabilities from SkuInfo
func (sku *SkuInfo) PopulateNodeCapabilities(nodeInfo *NodeInfo) (err error) {

	if sku.Capabilities != nil {
		for _, capability := range *sku.Capabilities {
			err = nil
			if capability.Name != nil {
				switch strings.ToLower(*capability.Name) {
				case UncachedDiskIOPSCapability:
					nodeInfo.maxIops, err = strconv.Atoi(*capability.Value)
				case UncachedDiskBytesPerSecondCapability:
					bw, err := strconv.Atoi(*capability.Value)
					nodeInfo.maxBwMbps = bw / (1024 * 1024)
					if err != nil {
						klog.Errorf("Failed to parse node capability %s. Error: %v", capability.Name, err)
						return err
					}
				case MaxDataDiskCountCapability:
					nodeInfo.maxDataDiskCount, err = strconv.Atoi(*capability.Value)
				case VCPUsCapability:
					nodeInfo.vcpus, err = strconv.Atoi(*capability.Value)
				default:
					continue
				}
			}

			if err != nil {
				klog.Errorf("Failed to parse node capability %s. Error: %v", capability.Name, err)
				return err
			}
		}
	}

	// If node doesn't support burst capabilities.
	// Set the burst limits as regular limits
	if nodeInfo.maxBurstIops < nodeInfo.maxIops {
		nodeInfo.maxBurstIops = nodeInfo.maxIops
	}

	if nodeInfo.maxBurstBwMbps < nodeInfo.maxBwMbps {
		nodeInfo.maxBurstBwMbps = nodeInfo.maxBwMbps
	}

	return nil
}

// getDiskCapabilities gets disk capabilities from SkuInfo
func (sku *SkuInfo) GetDiskCapabilities() (diskSku DiskSkuInfo, err error) {
	diskSku = DiskSkuInfo{}
	diskSku.storageAccountType = sku.Name
	diskSku.storageTier = sku.Tier
	diskSku.diskSize = sku.Size

	if sku.Capabilities != nil {
		for _, capability := range *sku.Capabilities {
			err = nil
			if capability.Name != nil {
				switch strings.ToLower(*capability.Name) {
				case MaxValueOfMaxSharesCapability:
					diskSku.maxAllowedShares, err = strconv.Atoi(*capability.Value)
				case MaxBurstIopsCapability:
					diskSku.maxBurstIops, err = strconv.Atoi(*capability.Value)
				case MaxIOpsCapability:
					diskSku.maxIops, err = strconv.Atoi(*capability.Value)
				case MaxBandwidthMBpsCapability:
					diskSku.maxBwMbps, err = strconv.Atoi(*capability.Value)
				case MaxBurstBandwidthMBpsCapability:
					diskSku.maxBurstBwMbps, err = strconv.Atoi(*capability.Value)
				case MaxSizeGiBCapability:
					diskSku.maxSizeGiB, err = strconv.Atoi(*capability.Value)
				default:
					continue
				}
			}

			if err != nil {
				klog.Errorf("Failed to parse disk capability %s. Error: %v", capability.Name, err)
				return diskSku, err
			}

		}
	}

	// If disk doesn't support burst capabilities.
	// Set the burst limits as regular limits
	if diskSku.maxBurstIops < diskSku.maxIops {
		diskSku.maxBurstIops = diskSku.maxIops
	}

	if diskSku.maxBurstBwMbps < diskSku.maxBwMbps {
		diskSku.maxBurstBwMbps = diskSku.maxBwMbps
	}

	return diskSku, nil
}

func (sku *DiskSkuInfo) GetRandomIOLatencyInSec() float64 {

	if sku.maxSizeGiB <= 4096 {

		return 0.0022
	}

	if sku.maxSizeGiB <= 8192 {

		return 0.0028
	}

	if sku.maxSizeGiB <= 16384 {

		return 0.0034
	}

	return 0.004

}

func (sku *DiskSkuInfo) GetSequentialOLatencyInSec() float64 {

	if sku.maxSizeGiB <= 4096 {

		return 0.0033
	}

	if sku.maxSizeGiB <= 8192 {

		return 0.0041
	}

	if sku.maxSizeGiB <= 16384 {

		return 0.0046
	}

	return 0.0052
}
