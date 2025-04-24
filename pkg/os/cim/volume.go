//go:build windows
// +build windows

/*
Copyright 2025 The Kubernetes Authors.

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

package cim

import (
	"fmt"
	"strconv"

	"github.com/microsoft/wmi/pkg/base/query"
	"github.com/microsoft/wmi/pkg/errors"
	cim "github.com/microsoft/wmi/pkg/wmiinstance"
	"github.com/microsoft/wmi/server2019/root/microsoft/windows/storage"
)

// QueryVolumeByUniqueID retrieves a specific volume by its unique identifier,
// returning the first volume that matches the given volume ID.
//
// The equivalent WMI query is:
//
//	SELECT [selectors] FROM MSFT_Volume
//
// Refer to https://learn.microsoft.com/en-us/windows-hardware/drivers/storage/msft-volume
// for the WMI class definition.
func QueryVolumeByUniqueID(volumeID string, selectorList []string) (*storage.MSFT_Volume, error) {
	var selectors []string
	selectors = append(selectors, selectorList...)
	selectors = append(selectors, "UniqueId")
	volumeQuery := query.NewWmiQueryWithSelectList("MSFT_Volume", selectors)
	instances, err := QueryInstances(WMINamespaceStorage, volumeQuery)
	if err != nil {
		return nil, err
	}

	for _, instance := range instances {
		volume, err := storage.NewMSFT_VolumeEx1(instance)
		if err != nil {
			return nil, fmt.Errorf("failed to query volume (%s). error: %w", volumeID, err)
		}

		uniqueID, err := volume.GetPropertyUniqueId()
		if err != nil {
			return nil, fmt.Errorf("failed to query volume unique ID (%s). error: %w", volumeID, err)
		}

		if uniqueID == volumeID {
			return volume, nil
		}
	}

	return nil, errors.NotFound
}

// ListVolumes retrieves all available volumes on the system.
//
// The equivalent WMI query is:
//
//	SELECT [selectors] FROM MSFT_Volume
//
// Refer to https://learn.microsoft.com/en-us/windows-hardware/drivers/storage/msft-volume
// for the WMI class definition.
func ListVolumes(selectorList []string) ([]*storage.MSFT_Volume, error) {
	diskQuery := query.NewWmiQueryWithSelectList("MSFT_Volume", selectorList)
	instances, err := QueryInstances(WMINamespaceStorage, diskQuery)
	if IgnoreNotFound(err) != nil {
		return nil, err
	}

	var volumes []*storage.MSFT_Volume
	for _, instance := range instances {
		volume, err := storage.NewMSFT_VolumeEx1(instance)
		if err != nil {
			return nil, fmt.Errorf("failed to query volume %v. error: %v", instance, err)
		}

		volumes = append(volumes, volume)
	}

	return volumes, nil
}

// ListPartitionsOnDisk retrieves all partitions or a partition with the specified number on a disk.
//
// The equivalent WMI query is:
//
//	SELECT [selectors] FROM MSFT_Partition
//	  WHERE DiskNumber = '<diskNumber>'
//	    AND PartitionNumber = '<partitionNumber>'
//
// Refer to https://learn.microsoft.com/en-us/windows-hardware/drivers/storage/msft-partition
// for the WMI class definition.
func ListPartitionsOnDisk(diskNumber, partitionNumber uint32, selectorList []string) ([]*storage.MSFT_Partition, error) {
	filters := []*query.WmiQueryFilter{
		query.NewWmiQueryFilter("DiskNumber", strconv.Itoa(int(diskNumber)), query.Equals),
	}
	if partitionNumber > 0 {
		filters = append(filters, query.NewWmiQueryFilter("PartitionNumber", strconv.Itoa(int(partitionNumber)), query.Equals))
	}
	return ListPartitionsWithFilters(selectorList, filters...)
}

// ListPartitionsWithFilters retrieves all partitions matching with the conditions specified by query filters.
//
// The equivalent WMI query is:
//
//	SELECT [selectors] FROM MSFT_Partition
//	  WHERE ...
//
// Refer to https://learn.microsoft.com/en-us/windows-hardware/drivers/storage/msft-partition
// for the WMI class definition.
func ListPartitionsWithFilters(selectorList []string, filters ...*query.WmiQueryFilter) ([]*storage.MSFT_Partition, error) {
	partitionQuery := query.NewWmiQueryWithSelectList("MSFT_Partition", selectorList)
	partitionQuery.Filters = append(partitionQuery.Filters, filters...)
	instances, err := QueryInstances(WMINamespaceStorage, partitionQuery)
	if IgnoreNotFound(err) != nil {
		return nil, err
	}

	var partitions []*storage.MSFT_Partition
	for _, instance := range instances {
		part, err := storage.NewMSFT_PartitionEx1(instance)
		if err != nil {
			return nil, fmt.Errorf("failed to query partition %v. error: %v", instance, err)
		}

		partitions = append(partitions, part)
	}

	return partitions, nil
}

// ListPartitionToVolumeMappings builds a mapping between partition and volume with partition Object ID as the key.
//
// The equivalent WMI query is:
//
//		SELECT [selectors] FROM MSFT_PartitionToVolume
//
//	 Partition                                                               | Volume
//	 ---------                                                               | ------
//	 MSFT_Partition (ObjectId = "{1}\\WIN-8E2EVAQ9QSB\ROOT/Microsoft/Win...) | MSFT_Volume (ObjectId = "{1}\\WIN-8E2EVAQ9QS...
//
// Refer to https://learn.microsoft.com/en-us/windows-hardware/drivers/storage/msft-partitiontovolume
// for the WMI class definition.
func ListPartitionToVolumeMappings() (map[string]string, error) {
	return ListWMIInstanceMappings(WMINamespaceStorage, "MSFT_PartitionToVolume", nil,
		mappingObjectRefIndexer("Partition", "MSFT_Partition", "ObjectId"),
		mappingObjectRefIndexer("Volume", "MSFT_Volume", "ObjectId"),
	)
}

// ListVolumeToPartitionMappings builds a mapping between volume and partition with volume Object ID as the key.
//
// The equivalent WMI query is:
//
//		SELECT [selectors] FROM MSFT_PartitionToVolume
//
//	 Partition                                                               | Volume
//	 ---------                                                               | ------
//	 MSFT_Partition (ObjectId = "{1}\\WIN-8E2EVAQ9QSB\ROOT/Microsoft/Win...) | MSFT_Volume (ObjectId = "{1}\\WIN-8E2EVAQ9QS...
//
// Refer to https://learn.microsoft.com/en-us/windows-hardware/drivers/storage/msft-partitiontovolume
// for the WMI class definition.
func ListVolumeToPartitionMappings() (map[string]string, error) {
	return ListWMIInstanceMappings(WMINamespaceStorage, "MSFT_PartitionToVolume", nil,
		mappingObjectRefIndexer("Volume", "MSFT_Volume", "ObjectId"),
		mappingObjectRefIndexer("Partition", "MSFT_Partition", "ObjectId"),
	)
}

// FindPartitionsByVolume finds all partitions associated with the given volumes
// using partition-to-volume mapping.
func FindPartitionsByVolume(partitions []*storage.MSFT_Partition, volumes []*storage.MSFT_Volume) ([]*storage.MSFT_Partition, error) {
	var partitionInstances []*cim.WmiInstance
	for _, part := range partitions {
		partitionInstances = append(partitionInstances, part.WmiInstance)
	}

	var volumeInstances []*cim.WmiInstance
	for _, volume := range volumes {
		volumeInstances = append(volumeInstances, volume.WmiInstance)
	}

	partitionToVolumeMappings, err := ListPartitionToVolumeMappings()
	if err != nil {
		return nil, err
	}

	filtered, err := FindInstancesByObjectIDMapping(partitionInstances, volumeInstances, partitionToVolumeMappings)
	if err != nil {
		return nil, err
	}

	var result []*storage.MSFT_Partition
	for _, instance := range filtered {
		part, err := storage.NewMSFT_PartitionEx1(instance)
		if err != nil {
			return nil, fmt.Errorf("failed to query partition %v. error: %v", instance, err)
		}

		result = append(result, part)
	}

	return result, nil
}

// FindVolumesByPartition finds all volumes associated with the given partitions
// using volume-to-partition mapping.
func FindVolumesByPartition(volumes []*storage.MSFT_Volume, partitions []*storage.MSFT_Partition) ([]*storage.MSFT_Volume, error) {
	var volumeInstances []*cim.WmiInstance
	for _, volume := range volumes {
		volumeInstances = append(volumeInstances, volume.WmiInstance)
	}

	var partitionInstances []*cim.WmiInstance
	for _, part := range partitions {
		partitionInstances = append(partitionInstances, part.WmiInstance)
	}

	volumeToPartitionMappings, err := ListVolumeToPartitionMappings()
	if err != nil {
		return nil, err
	}

	filtered, err := FindInstancesByObjectIDMapping(volumeInstances, partitionInstances, volumeToPartitionMappings)
	if err != nil {
		return nil, err
	}

	var result []*storage.MSFT_Volume
	for _, instance := range filtered {
		volume, err := storage.NewMSFT_VolumeEx1(instance)
		if err != nil {
			return nil, fmt.Errorf("failed to query volume %v. error: %v", instance, err)
		}

		result = append(result, volume)
	}

	return result, nil
}

// GetPartitionByVolumeUniqueID retrieves a specific partition from a volume identified by its unique ID.
func GetPartitionByVolumeUniqueID(volumeID string, partitionSelectorList []string) (*storage.MSFT_Partition, error) {
	volume, err := QueryVolumeByUniqueID(volumeID, []string{"ObjectId"})
	if err != nil {
		return nil, err
	}

	partitions, err := ListPartitionsWithFilters(partitionSelectorList)
	if err != nil {
		return nil, err
	}

	result, err := FindPartitionsByVolume(partitions, []*storage.MSFT_Volume{volume})
	if err != nil {
		return nil, err
	}

	return result[0], nil
}

// GetVolumeByDriveLetter retrieves a volume associated with a specific drive letter.
func GetVolumeByDriveLetter(driveLetter string, partitionSelectorList []string) (*storage.MSFT_Volume, error) {
	var selectorsForPart []string
	selectorsForPart = append(selectorsForPart, partitionSelectorList...)
	selectorsForPart = append(selectorsForPart, "ObjectId")
	partitions, err := ListPartitionsWithFilters(selectorsForPart, query.NewWmiQueryFilter("DriveLetter", driveLetter, query.Equals))
	if err != nil {
		return nil, err
	}

	volumes, err := ListVolumes(partitionSelectorList)
	if err != nil {
		return nil, err
	}

	result, err := FindVolumesByPartition(volumes, partitions)
	if err != nil {
		return nil, err
	}

	if len(result) == 0 {
		return nil, errors.NotFound
	}

	return result[0], nil
}

// GetPartitionDiskNumber retrieves the disk number associated with a given partition.
//
// Refer to https://learn.microsoft.com/en-us/windows-hardware/drivers/storage/msft-partition
// for the WMI class definitions.
func GetPartitionDiskNumber(part *storage.MSFT_Partition) (uint32, error) {
	diskNumber, err := part.GetProperty("DiskNumber")
	if err != nil {
		return 0, err
	}

	return uint32(diskNumber.(int32)), nil
}
