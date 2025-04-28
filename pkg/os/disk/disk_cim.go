//go:build windows
// +build windows

/*
Copyright 2023 The Kubernetes Authors.

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

package disk

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"github.com/microsoft/wmi/pkg/base/query"
	"k8s.io/klog/v2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/os/cim"
)

var _ DiskAPI = &cimDiskAPI{}

type cimDiskAPI struct{}

func NewCIMDiskAPI() *cimDiskAPI {
	return &cimDiskAPI{}
}

// ListDiskLocations - constructs a map with the disk number as the key and the DiskLocation structure
// as the value. The DiskLocation struct has various fields like the Adapter, Bus, Target and LUNID.
func (*cimDiskAPI) ListDiskLocations() (map[uint32]Location, error) {
	// sample response
	// [{
	//    "number":  0,
	//    "location":  "PCI Slot 3 : Adapter 0 : Port 0 : Target 1 : LUN 0"
	// }, ...]
	disks, err := cim.ListDisks([]string{"Number", "Location", "PartitionStyle"})
	if err != nil {
		return nil, fmt.Errorf("could not query disk locations")
	}

	m := make(map[uint32]Location)
	for _, disk := range disks {
		num, err := disk.GetProperty("Number")
		if err != nil {
			return m, fmt.Errorf("failed to query disk number: %v, %w", disk, err)
		}

		location, err := disk.GetPropertyLocation()
		if err != nil {
			return m, fmt.Errorf("failed to query disk location: %v, %w", disk, err)
		}

		partitionStyle, err := disk.GetProperty("PartitionStyle")
		if err == nil {
			if partitionStyle.(int32) == int32(cim.PartitionStyleMBR) {
				klog.V(2).Infof("skipping MBR disk, number: %d, location: %s", num, location)
				continue
			}
		} else {
			klog.Warningf("failed to query partition style of disk %d: %v", num, err)
		}

		klog.V(5).Infof("disk number: %d, location: %s, partitionStyle: %d", num, location, partitionStyle)
		found := false
		s := strings.Split(location, ":")
		if len(s) >= 5 {
			var d Location
			for _, item := range s {
				item = strings.TrimSpace(item)
				itemSplit := strings.Split(item, " ")
				if len(itemSplit) == 2 {
					found = true
					switch strings.TrimSpace(itemSplit[0]) {
					case "Adapter":
						d.Adapter = strings.TrimSpace(itemSplit[1])
						if d.Adapter == "0" {
							klog.V(2).Infof("skipping adapter 0 disk, number: %d, location: %s", num, location)
							found = false
						}
					case "Target":
						d.Target = strings.TrimSpace(itemSplit[1])
					case "LUN":
						d.LUNID = strings.TrimSpace(itemSplit[1])
					default:
						klog.V(6).Infof("Got unknown field : %s=%s", itemSplit[0], itemSplit[1])
					}
				}
			}

			if found {
				m[uint32(num.(int32))] = d
			}
		}
	}
	return m, nil
}

func (*cimDiskAPI) Rescan() error {
	result, _, err := cim.InvokeCimMethod(cim.WMINamespaceStorage, "MSFT_StorageSetting", "UpdateHostStorageCache", nil)
	if err != nil {
		return fmt.Errorf("error updating host storage cache output. result: %d, err: %v", result, err)
	}
	return nil
}

func (*cimDiskAPI) IsDiskInitialized(diskNumber uint32) (bool, error) {
	var partitionStyle int32
	disk, err := cim.QueryDiskByNumber(diskNumber, []string{"PartitionStyle"})
	if err != nil {
		return false, fmt.Errorf("error checking initialized status of disk %d. %v", diskNumber, err)
	}

	retValue, err := disk.GetProperty("PartitionStyle")
	if err != nil {
		return false, fmt.Errorf("failed to query partition style of disk %d: %w", diskNumber, err)
	}

	partitionStyle = retValue.(int32)
	return partitionStyle != cim.PartitionStyleUnknown, nil
}

func (*cimDiskAPI) InitializeDisk(diskNumber uint32) error {
	disk, err := cim.QueryDiskByNumber(diskNumber, nil)
	if err != nil {
		return fmt.Errorf("failed to initializing disk %d. error: %w", diskNumber, err)
	}

	result, err := disk.InvokeMethodWithReturn("Initialize", int32(cim.PartitionStyleGPT))
	if result != 0 || err != nil {
		return fmt.Errorf("failed to initializing disk %d: result %d, error: %w", diskNumber, result, err)
	}

	return nil
}

func (*cimDiskAPI) BasicPartitionsExist(diskNumber uint32) (bool, error) {
	partitions, err := cim.ListPartitionsWithFilters(nil,
		query.NewWmiQueryFilter("DiskNumber", strconv.Itoa(int(diskNumber)), query.Equals),
		query.NewWmiQueryFilter("GptType", cim.GPTPartitionTypeMicrosoftReserved, query.NotEquals))
	if cim.IgnoreNotFound(err) != nil {
		return false, fmt.Errorf("error checking presence of partitions on disk %d:, %v", diskNumber, err)
	}

	return len(partitions) > 0, nil
}

func (*cimDiskAPI) CreateBasicPartition(diskNumber uint32) error {
	disk, err := cim.QueryDiskByNumber(diskNumber, nil)
	if err != nil {
		return err
	}

	result, err := disk.InvokeMethodWithReturn(
		"CreatePartition",
		nil,                           // Size
		true,                          // UseMaximumSize
		nil,                           // Offset
		nil,                           // Alignment
		nil,                           // DriveLetter
		false,                         // AssignDriveLetter
		nil,                           // MbrType,
		cim.GPTPartitionTypeBasicData, // GPT Type
		false,                         // IsHidden
		false,                         // IsActive,
	)
	// 42002 is returned by driver letter failed to assign after partition
	if (result != 0 && result != 42002) || err != nil {
		return fmt.Errorf("error creating partition on disk %d. result: %d, err: %v", diskNumber, result, err)
	}

	var status string
	result, err = disk.InvokeMethodWithReturn("Refresh", &status)
	if result != 0 || err != nil {
		return fmt.Errorf("error rescan disk (%d). result %d, error: %v", diskNumber, result, err)
	}

	partitions, err := cim.ListPartitionsWithFilters(nil,
		query.NewWmiQueryFilter("DiskNumber", strconv.Itoa(int(diskNumber)), query.Equals),
		query.NewWmiQueryFilter("GptType", cim.GPTPartitionTypeMicrosoftReserved, query.NotEquals))
	if err != nil {
		return fmt.Errorf("error query basic partition on disk %d:, %v", diskNumber, err)
	}

	if len(partitions) == 0 {
		return fmt.Errorf("failed to create basic partition on disk %d:, %v", diskNumber, err)
	}

	partition := partitions[0]
	result, err = partition.InvokeMethodWithReturn("Online", status)
	if result != 0 || err != nil {
		return fmt.Errorf("error bring partition %v on disk %d online. result: %d, status %s, err: %v", partition, diskNumber, result, status, err)
	}

	err = partition.Refresh()
	return err
}

func (c *cimDiskAPI) GetDiskNumberByName(page83ID string) (uint32, error) {
	return c.GetDiskNumberWithID(page83ID)
}

func (*cimDiskAPI) GetDiskNumber(disk syscall.Handle) (uint32, error) {
	var bytes uint32
	devNum := StorageDeviceNumber{}
	buflen := uint32(unsafe.Sizeof(devNum.DeviceType)) + uint32(unsafe.Sizeof(devNum.DeviceNumber)) + uint32(unsafe.Sizeof(devNum.PartitionNumber))

	err := syscall.DeviceIoControl(disk, IOCTL_STORAGE_GET_DEVICE_NUMBER, nil, 0, (*byte)(unsafe.Pointer(&devNum)), buflen, &bytes, nil)
	return devNum.DeviceNumber, err
}

func (*cimDiskAPI) GetDiskPage83ID(disk syscall.Handle) (string, error) {
	query := StoragePropertyQuery{}

	bufferSize := uint32(4 * 1024)
	buffer := make([]byte, 4*1024)
	var size uint32
	var n uint32
	var m uint16

	query.QueryType = PropertyStandardQuery
	query.PropertyID = StorageDeviceIDProperty

	querySize := uint32(unsafe.Sizeof(query.PropertyID)) + uint32(unsafe.Sizeof(query.QueryType)) + uint32(unsafe.Sizeof(query.Byte))
	querySize = uint32(unsafe.Sizeof(query))
	err := syscall.DeviceIoControl(disk, IOCTL_STORAGE_QUERY_PROPERTY, (*byte)(unsafe.Pointer(&query)), querySize, (*byte)(unsafe.Pointer(&buffer[0])), bufferSize, &size, nil)
	if err != nil {
		return "", fmt.Errorf("IOCTL_STORAGE_QUERY_PROPERTY failed: %v", err)
	}

	devIDDesc := (*StorageDeviceIDDescriptor)(unsafe.Pointer(&buffer[0]))

	pID := (*StorageIdentifier)(unsafe.Pointer(&devIDDesc.Identifiers[0]))

	page83ID := []byte{}
	byteSize := unsafe.Sizeof(byte(0))
	for n = 0; n < devIDDesc.NumberOfIdentifiers; n++ {
		if pID.Association == StorageIDAssocDevice && (pID.CodeSet == StorageIDCodeSetBinary || pID.CodeSet == StorageIDCodeSetASCII) {
			for m = 0; m < pID.IdentifierSize; m++ {
				page83ID = append(page83ID, *(*byte)(unsafe.Pointer(uintptr(unsafe.Pointer(&pID.Identifier[0])) + byteSize*uintptr(m))))
			}

			if pID.CodeSet == StorageIDCodeSetASCII {
				return string(page83ID), nil
			} else if pID.CodeSet == StorageIDCodeSetBinary {
				return hex.EncodeToString(page83ID), nil
			}
		}
		pID = (*StorageIdentifier)(unsafe.Pointer(uintptr(unsafe.Pointer(pID)) + byteSize*uintptr(pID.NextOffset)))
	}
	return "", nil
}

func (c *cimDiskAPI) GetDiskNumberWithID(page83ID string) (uint32, error) {
	disks, err := cim.ListDisks([]string{"Path", "SerialNumber"})
	if err != nil {
		return 0, err
	}

	for _, disk := range disks {
		path, err := disk.GetPropertyPath()
		if err != nil {
			return 0, fmt.Errorf("failed to query disk path: %v, %w", disk, err)
		}

		diskNumber, diskPage83ID, err := c.GetDiskNumberAndPage83ID(path)
		if err != nil {
			return 0, err
		}

		if diskPage83ID == page83ID {
			return diskNumber, nil
		}
	}

	return 0, fmt.Errorf("could not find disk with Page83 ID %s", page83ID)
}

func (c *cimDiskAPI) GetDiskNumberAndPage83ID(path string) (uint32, string, error) {
	h, err := syscall.Open(path, syscall.O_RDONLY, 0)
	defer syscall.Close(h)
	if err != nil {
		return 0, "", err
	}

	diskNumber, err := c.GetDiskNumber(h)
	if err != nil {
		return 0, "", err
	}

	page83ID, err := c.GetDiskPage83ID(h)
	if err != nil {
		return 0, "", err
	}

	return diskNumber, page83ID, nil
}

// ListDiskIDs - constructs a map with the disk number as the key and the DiskID structure
// as the value. The DiskID struct has a field for the page83 ID.
func (c *cimDiskAPI) ListDiskIDs() (map[uint32]IDs, error) {
	// sample response
	// [
	// {
	//     "Path":  "\\\\?\\scsi#disk\u0026ven_google\u0026prod_persistentdisk#4\u002621cb0360\u00260\u0026000100#{53f56307-b6bf-11d0-94f2-00a0c91efb8b}",
	//     "SerialNumber":  "                    "
	// },
	// {
	//     "Path":  "\\\\?\\scsi#disk\u0026ven_msft\u0026prod_virtual_disk#2\u00261f4adffe\u00260\u0026000001#{53f56307-b6bf-11d0-94f2-00a0c91efb8b}",
	//     "SerialNumber":  null
	// }, ]
	disks, err := cim.ListDisks([]string{"Path", "SerialNumber"})
	if err != nil {
		return nil, err
	}

	m := make(map[uint32]IDs)
	for _, disk := range disks {
		path, err := disk.GetPropertyPath()
		if err != nil {
			return m, fmt.Errorf("failed to query disk path: %v, %w", disk, err)
		}

		sn, err := disk.GetPropertySerialNumber()
		if err != nil {
			return m, fmt.Errorf("failed to query disk serial number: %v, %w", disk, err)
		}

		diskNumber, page83, err := c.GetDiskNumberAndPage83ID(path)
		if err != nil {
			return m, err
		}

		m[diskNumber] = IDs{
			Page83:       page83,
			SerialNumber: sn,
		}
	}
	return m, nil
}

func (*cimDiskAPI) GetDiskStats(diskNumber uint32) (int64, error) {
	// TODO: change to uint64 as it does not make sense to use int64 for size
	var size int64
	disk, err := cim.QueryDiskByNumber(diskNumber, []string{"Size"})
	if err != nil {
		return -1, err
	}

	sz, err := disk.GetProperty("Size")
	if err != nil {
		return -1, fmt.Errorf("failed to query size of disk %d. %v", diskNumber, err)
	}

	size, err = strconv.ParseInt(sz.(string), 10, 64)
	return size, err
}

func (*cimDiskAPI) SetDiskState(diskNumber uint32, isOnline bool) error {
	disk, err := cim.QueryDiskByNumber(diskNumber, []string{"IsOffline"})
	if err != nil {
		return err
	}

	offline, err := disk.GetPropertyIsOffline()
	if err != nil {
		return fmt.Errorf("error setting disk %d attach state. error: %v", diskNumber, err)
	}

	if isOnline == !offline {
		return nil
	}

	method := "Offline"
	if isOnline {
		method = "Online"
	}

	result, err := disk.InvokeMethodWithReturn(method)
	if result != 0 || err != nil {
		return fmt.Errorf("setting disk %d attach state %s: result %d, error: %w", diskNumber, method, result, err)
	}

	return nil
}

func (*cimDiskAPI) GetDiskState(diskNumber uint32) (bool, error) {
	disk, err := cim.QueryDiskByNumber(diskNumber, []string{"IsOffline"})
	if err != nil {
		return false, err
	}

	isOffline, err := disk.GetPropertyIsOffline()
	if err != nil {
		return false, fmt.Errorf("error parsing disk %d state. error: %v", diskNumber, err)
	}

	return !isOffline, nil
}

func (c *cimDiskAPI) PartitionDisk(diskNumber uint32) error {
	klog.V(6).Infof("Request: PartitionDisk with diskNumber=%d", diskNumber)

	initialized, err := c.IsDiskInitialized(diskNumber)
	if err != nil {
		klog.Errorf("IsDiskInitialized failed: %v", err)
		return err
	}
	if !initialized {
		klog.V(4).Infof("Initializing disk %d", diskNumber)
		err = c.InitializeDisk(diskNumber)
		if err != nil {
			klog.Errorf("failed InitializeDisk %v", err)
			return err
		}
	} else {
		klog.V(4).Infof("Disk %d already initialized", diskNumber)
	}

	klog.V(6).Infof("Checking if disk %d has basic partitions", diskNumber)
	partitioned, err := c.BasicPartitionsExist(diskNumber)
	if err != nil {
		klog.Errorf("failed check BasicPartitionsExist %v", err)
		return err
	}
	if !partitioned {
		klog.V(4).Infof("Creating basic partition on disk %d", diskNumber)
		err = c.CreateBasicPartition(diskNumber)
		if err != nil {
			klog.Errorf("failed CreateBasicPartition %v", err)
			return err
		}
	} else {
		klog.V(4).Infof("Disk %d already partitioned", diskNumber)
	}
	return nil
}
