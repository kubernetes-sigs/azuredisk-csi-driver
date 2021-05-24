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
	"fmt"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
)

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
	SkuName          string
	Zone             string
	Region           string
	MaxDataDiskCount int
	VCpus            int
	MaxBurstIops     int
	MaxIops          int
	MaxBwMbps        int
	MaxBurstBwMbps   int
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
	StorageAccountType string
	StorageTier        string
	DiskSize           string
	MaxAllowedShares   int
	MaxBurstIops       int
	MaxIops            int
	MaxBwMbps          int
	MaxBurstBwMbps     int
	MaxSizeGiB         int
}

// PopulateNodeAndSkuInfo populates Node and Sku related information in memory
func PopulateNodeAndSkuInfo(d DriverCore) error {
	klog.V(2).Infof("PopulateNodeAndSkuInfo: Starting to populate node and disk sku information.")

	instances, ok := d.cloud.Instances()
	if !ok {
		return status.Error(codes.Internal, "PopulateNodeAndSkuInfo: Failed to get instances from cloud provider")
	}

	instanceType, err := instances.InstanceType(context.TODO(), types.NodeName(d.NodeID))
	if err != nil {
		return fmt.Errorf("PopulateNodeAndSkuInfo: Failed to get instance type from Azure cloud provider, nodeName: %v, error: %v", d.NodeID, err)
	}

	zone, err := d.cloud.GetZone(context.TODO())
	if err != nil {
		return fmt.Errorf("PopulateNodeAndSkuInfo: Failed to get zone from Azure cloud provider, nodeName: %v, error: %v", d.NodeID, err)
	}

	return populateNodeAndSkuInfoInternal(d, instanceType, zone.FailureDomain, zone.Region)
}

// populateNodeAndSkuInfoInternal populates Node and Sku related in memory
func populateNodeAndSkuInfoInternal(d DriverCore, instance string, zone string, region string) error {
	d.nodeInfo = &NodeInfo{}
	d.nodeInfo.SkuName = instance
	d.nodeInfo.Zone = zone
	d.nodeInfo.Region = region

	err := populateSkuMap(d)
	if err != nil {
		return fmt.Errorf("PopulateNodeAndSkuInfo: Could populate sku information. Error: %v", err)
	}

	klog.V(2).Infof("PopulateNodeAndSkuInfo: Populated node and disk sku information. NodeInfo=%v", d.nodeInfo)

	return nil
}

// populateSkuMap populates the sku map from the sku json file
func populateSkuMap(d DriverCore) (err error) {
	d.diskSkuInfoMap = DiskSkuMap

	nodeSkuNameLower := strings.ToLower(d.nodeInfo.SkuName)
	if vmSku, ok := NodeInfoMap[nodeSkuNameLower]; ok {
		d.nodeInfo.MaxBurstBwMbps = vmSku.MaxBurstBwMbps
		d.nodeInfo.MaxBurstIops = vmSku.MaxBurstIops
		d.nodeInfo.MaxBwMbps = vmSku.MaxBwMbps
		d.nodeInfo.MaxIops = vmSku.MaxIops
		d.nodeInfo.MaxDataDiskCount = vmSku.MaxDataDiskCount
		d.nodeInfo.VCpus = vmSku.VCpus
	} else {
		return fmt.Errorf("populateSkuMap: Could not find SKU %s in the sku map", nodeSkuNameLower)
	}

	return nil
}

// GetRandomIOLatencyInSec gets the estimated random IP latency for a small write for a disk size
// These latencies are manually calculated and stored
// ToDo: Make this estimation dynamic
func (sku *DiskSkuInfo) GetRandomIOLatencyInSec() float64 {
	if sku.MaxSizeGiB <= 4096 {
		return 0.0022
	}

	if sku.MaxSizeGiB <= 8192 {
		return 0.0028
	}

	if sku.MaxSizeGiB <= 16384 {
		return 0.0034
	}

	return 0.004
}

// GetSequentialOLatencyInSec gets the estimated sequential IO latency for a disk size
// These latencies are manually calculated and stored
// ToDo: Make this estimation dynamic
func (sku *DiskSkuInfo) GetSequentialOLatencyInSec() float64 {
	if sku.MaxSizeGiB <= 4096 {
		return 0.0033
	}

	if sku.MaxSizeGiB <= 8192 {
		return 0.0041
	}

	if sku.MaxSizeGiB <= 16384 {
		return 0.0046
	}

	return 0.0052
}
