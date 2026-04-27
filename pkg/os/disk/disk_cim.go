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
	"errors"
	"fmt"
	"math"
	"strings"
	"syscall"
	"unsafe"

	"k8s.io/klog/v2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/os/wmi"
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
	m := make(map[uint32]Location)
	err := wmi.WithCOMThread(func() error {
		return wmi.WithScope(func(scope *wmi.Scope) error {
			// "location":  "PCI Slot 3 : Adapter 0 : Port 0 : Target 1 : LUN 0"
			disks, err := wmi.ListDisks(scope, []string{"Number", "Location", "PartitionStyle"})
			if err != nil {
				return fmt.Errorf("failed to list disks: %w", err)
			}

			err = wmi.ForEach(disks, func(disk *wmi.COMDispatchObject) error {
				num, err := wmi.GetDiskNumber(disk)
				if err != nil {
					return fmt.Errorf("failed to query disk number: %v, %w", disk, err)
				}

				location, err := wmi.GetDiskLocation(disk)
				if err != nil {
					return fmt.Errorf("failed to query disk location: %v, %w", disk, err)
				}

				partitionStyle, err := wmi.GetDiskPartitionStyle(disk)
				if err == nil {
					if partitionStyle == wmi.PartitionStyleMBR {
						klog.V(2).Infof("skipping MBR disk, number: %d, location: %s", num, location)
						return nil
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
						m[num] = d
					}
				}
				return nil
			})
			return err
		})
	})
	return m, err
}

func (*cimDiskAPI) Rescan() error {
	return wmi.WithCOMThread(func() error {
		err := wmi.RescanDisks()
		if err != nil {
			return fmt.Errorf("error updating host storage cache output. err: %w", err)
		}
		return nil
	})
}

func (*cimDiskAPI) IsDiskInitialized(diskNumber uint32) (bool, error) {
	var partitionStyle uint16
	err := wmi.WithCOMThread(func() error {
		return wmi.WithScope(func(scope *wmi.Scope) error {
			disk, err := wmi.QueryDiskByNumber(scope, diskNumber, wmi.DiskSelectorListForPartitionStyle)
			if err != nil {
				return fmt.Errorf("error checking initialized status of disk %d: %w", diskNumber, err)
			}

			partitionStyle, err = wmi.GetDiskPartitionStyle(disk)
			if err != nil {
				return fmt.Errorf("failed to query partition style of disk %d: %w", diskNumber, err)
			}

			return nil
		})
	})
	return partitionStyle != wmi.PartitionStyleUnknown, err
}

func (*cimDiskAPI) InitializeDisk(diskNumber uint32) error {
	return wmi.WithCOMThread(func() error {
		return wmi.WithScope(func(scope *wmi.Scope) error {
			disk, err := wmi.QueryDiskByNumber(scope, diskNumber, nil)
			if err != nil {
				return fmt.Errorf("failed to initializing disk %d. error: %w", diskNumber, err)
			}

			err = wmi.InitializeDisk(disk, wmi.PartitionStyleGPT)
			if err != nil {
				return fmt.Errorf("failed to initializing disk %d: error: %w", diskNumber, err)
			}

			return nil
		})
	})
}

func (*cimDiskAPI) BasicPartitionsExist(diskNumber uint32) (bool, error) {
	var exist bool
	err := wmi.WithCOMThread(func() error {
		return wmi.WithScope(func(scope *wmi.Scope) error {
			partitions, err := wmi.ListPartitionsWithFilters(scope, nil, wmi.FilterForPartitionOnDisk(diskNumber), wmi.FilterForPartitionsOfTypeNormal())
			if err != nil {
				return fmt.Errorf("error checking presence of partitions on disk %d: %w", diskNumber, err)
			}

			exist = len(partitions) > 0
			return nil
		})
	})
	return exist, err
}

func (*cimDiskAPI) CreateBasicPartition(diskNumber uint32) error {
	return wmi.WithCOMThread(func() error {
		return wmi.WithScope(func(scope *wmi.Scope) error {
			disk, err := wmi.QueryDiskByNumber(scope, diskNumber, nil)
			if err != nil {
				return err
			}

			err = wmi.CreatePartition(
				disk,
				nil,                           // Size
				true,                          // UseMaximumSize
				nil,                           // Offset
				nil,                           // Alignment
				nil,                           // DriveLetter
				false,                         // AssignDriveLetter
				nil,                           // MbrType,
				wmi.GPTPartitionTypeBasicData, // GPT Type
				false,                         // IsHidden
				false,                         // IsActive,
			)
			if err != nil {
				var werr *wmi.WMIError
				if !errors.As(err, &werr) || werr.Code != wmi.ErrorCodeCreatePartitionAccessPathAlreadyInUse {
					return fmt.Errorf("error creating partition on disk %d. err: %w", diskNumber, err)
				}
			}

			_, err = wmi.RefreshDisk(disk)
			if err != nil {
				return fmt.Errorf("error rescan disk (%d). error: %w", diskNumber, err)
			}

			partitions, err := wmi.ListPartitionsWithFilters(scope, nil, wmi.FilterForPartitionOnDisk(diskNumber), wmi.FilterForPartitionsOfTypeNormal())
			if err != nil {
				return fmt.Errorf("error query basic partition on disk %d: %w", diskNumber, err)
			}

			if len(partitions) == 0 {
				return fmt.Errorf("no partitions found on disk %d after creation", diskNumber)
			}

			partition := partitions[0]
			status, err := wmi.SetPartitionState(partition, true)
			if err != nil {
				return fmt.Errorf("error bring partition %v on disk %d online. status %s, err: %w", partition, diskNumber, status, err)
			}

			return nil
		})
	})
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
	var diskNumberResult uint32
	err := wmi.WithCOMThread(func() error {
		return wmi.WithScope(func(scope *wmi.Scope) error {
			disks, err := wmi.ListDisks(scope, wmi.DiskSelectorListForPathAndSerialNumber)
			if err != nil {
				return fmt.Errorf("failed to list disks: %w", err)
			}

			found := false
			err = wmi.ForEach(disks, func(disk *wmi.COMDispatchObject) error {
				path, err := wmi.GetDiskPath(disk)
				if err != nil {
					return fmt.Errorf("failed to query disk path: %v, %w", disk, err)
				}

				diskNumber, diskPage83ID, err := c.GetDiskNumberAndPage83ID(path)
				if err != nil {
					return err
				}

				if diskPage83ID == page83ID {
					diskNumberResult = diskNumber
					found = true
					return wmi.ErrStopIteration
				}
				return nil
			})
			if err != nil {
				return err
			}

			if !found {
				return fmt.Errorf("could not find disk with Page83 ID %s: %w", page83ID, wmi.ErrNotFound)
			}
			return nil
		})
	})
	return diskNumberResult, err
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
	m := make(map[uint32]IDs)
	err := wmi.WithCOMThread(func() error {
		return wmi.WithScope(func(scope *wmi.Scope) error {
			disks, err := wmi.ListDisks(scope, wmi.DiskSelectorListForPathAndSerialNumber)
			if err != nil {
				return fmt.Errorf("failed to list disks: %w", err)
			}

			err = wmi.ForEach(disks, func(disk *wmi.COMDispatchObject) error {
				path, err := wmi.GetDiskPath(disk)
				if err != nil {
					return fmt.Errorf("failed to query disk path: %v, %w", disk, err)
				}

				sn, err := wmi.GetDiskSerialNumber(disk)
				if err != nil {
					return fmt.Errorf("failed to query disk serial number: %v, %w", disk, err)
				}

				diskNumber, page83, err := c.GetDiskNumberAndPage83ID(path)
				if err != nil {
					return err
				}

				m[diskNumber] = IDs{
					Page83:       page83,
					SerialNumber: sn,
				}
				return nil
			})
			return err
		})
	})
	return m, err
}

func (*cimDiskAPI) GetDiskStats(diskNumber uint32) (size int64, err error) {
	// TODO: change to uint64 as it does not make sense to use int64 for size
	size = -1
	err = wmi.WithCOMThread(func() error {
		return wmi.WithScope(func(scope *wmi.Scope) error {
			disk, err := wmi.QueryDiskByNumber(scope, diskNumber, wmi.DiskSelectorListForSize)
			if err != nil {
				return err
			}

			sz, err := wmi.GetDiskSize(disk)
			if err != nil {
				return fmt.Errorf("failed to query size of disk %d. %w", diskNumber, err)
			}

			if sz > math.MaxInt64 {
				return fmt.Errorf("disk %d size %d exceeds max int64", diskNumber, sz)
			}
			size = int64(sz)
			return nil
		})
	})
	return size, err
}

func (*cimDiskAPI) SetDiskState(diskNumber uint32, isOnline bool) error {
	return wmi.WithCOMThread(func() error {
		return wmi.WithScope(func(scope *wmi.Scope) error {
			disk, err := wmi.QueryDiskByNumber(scope, diskNumber, wmi.DiskSelectorListForIsOffline)
			if err != nil {
				return err
			}

			isOffline, err := wmi.IsDiskOffline(disk)
			if err != nil {
				return fmt.Errorf("error setting disk %d attach state. error: %w", diskNumber, err)
			}

			if isOnline == !isOffline {
				klog.V(2).Infof("Disk %d is already in the desired state", diskNumber)
				return nil
			}

			_, err = wmi.SetDiskState(disk, isOnline)
			if err != nil {
				return fmt.Errorf("setting disk %d attach state (isOnline: %v): error: %w", diskNumber, isOnline, err)
			}

			return nil
		})
	})
}

func (*cimDiskAPI) GetDiskState(diskNumber uint32) (bool, error) {
	var isOffline bool
	err := wmi.WithCOMThread(func() error {
		return wmi.WithScope(func(scope *wmi.Scope) error {
			disk, err := wmi.QueryDiskByNumber(scope, diskNumber, wmi.DiskSelectorListForIsOffline)
			if err != nil {
				return err
			}

			isOffline, err = wmi.IsDiskOffline(disk)
			if err != nil {
				return fmt.Errorf("error parsing disk %d state. error: %w", diskNumber, err)
			}

			return nil
		})
	})
	return !isOffline, err
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
