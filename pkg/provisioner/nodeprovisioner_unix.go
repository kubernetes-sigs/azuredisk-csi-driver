// +build linux darwin

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

package provisioner

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"k8s.io/klog/v2"
	"k8s.io/mount-utils"
	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
)

const (
	scsiPath = "/sys/class/scsi_host/"
)

// GetDevicePathWithMountPath returns the device path for the specified mount point/
func (p *NodeProvisioner) GetDevicePathWithMountPath(mountPath string) (string, error) {
	args := []string{"-o", "source", "--noheadings", "--mountpoint", mountPath}
	output, err := p.mounter.Exec.Command("findmnt", args...).Output()

	if err != nil {
		return "", fmt.Errorf("could not determine device path(%s), error: %v", mountPath, err)
	}

	devicePath := strings.TrimSpace(string(output))
	if len(devicePath) == 0 {
		return "", fmt.Errorf("could not get valid device for mount path: %q", mountPath)
	}

	return devicePath, nil
}

// FormatAndMount formats the volume and mounts it at the specified path.
func (p *NodeProvisioner) FormatAndMount(source, target, fstype string, options []string) error {
	return p.mounter.FormatAndMount(source, target, fstype, options)
}

// PreparePublishPath readies the mount point for mount.
func (p *NodeProvisioner) PreparePublishPath(path string) error {
	return nil
}

// CleanupMountPoint unmounts the given path and deletes the remaining directory if successful.
func (p *NodeProvisioner) CleanupMountPoint(path string, extensiveCheck bool) error {
	return mount.CleanupMountPoint(path, p.mounter, extensiveCheck)
}

// Resize resizes the filesystem of the specified volume.
func (p *NodeProvisioner) Resize(source, target string) error {
	resizer := mount.NewResizeFs(p.mounter.Exec)

	if _, err := resizer.Resize(source, target); err != nil {
		return err
	}

	return nil
}

// GetBlockSizeBytes returns the block size, in bytes, of the block device at the specified path.
func (p *NodeProvisioner) GetBlockSizeBytes(devicePath string) (int64, error) {
	output, err := p.mounter.Exec.Command("blockdev", "--getsize64", devicePath).Output()
	if err != nil {
		return -1, fmt.Errorf("error when getting size of block volume at path %s: output: %s, err: %v", devicePath, string(output), err)
	}

	strOut := strings.TrimSpace(string(output))
	gotSizeBytes, err := strconv.ParseInt(strOut, 10, 64)
	if err != nil {
		return -1, fmt.Errorf("failed to parse size %s into a valid size", strOut)
	}

	return gotSizeBytes, nil
}

// readyMountPoint readies the mount point for mount.
func (p *NodeProvisioner) readyMountPoint(target string) error {
	if err := volumehelper.MakeDir(target); err != nil {
		klog.Errorf("mkdir failed on target: %s (%v)", target, err)
		return err
	}

	return nil
}

func (p *NodeProvisioner) rescanScsiHost() {
	if dirs, err := p.ioHandler.ReadDir(scsiPath); err == nil {
		for _, f := range dirs {
			name := scsiPath + f.Name() + "/scan"
			data := []byte("- - -")
			if err = p.ioHandler.WriteFile(name, data, 0666); err != nil {
				klog.Warningf("failed to rescan scsi host %s", name)
			}
		}
	} else {
		klog.Warningf("failed to read %s, err %v", scsiPath, err)
	}
}

// exclude those used by azure as resource and OS root in /dev/disk/azure, /dev/disk/azure/scsi0
// "/dev/disk/azure/scsi0" dir is populated in Standard_DC4s/DC2s on Ubuntu 18.04
func (p *NodeProvisioner) listAzureDiskPath() []string {
	var azureDiskList []string
	azureResourcePaths := []string{"/dev/disk/azure/", "/dev/disk/azure/scsi0/"}

	for _, azureDiskPath := range azureResourcePaths {
		if dirs, err := p.ioHandler.ReadDir(azureDiskPath); err == nil {
			for _, f := range dirs {
				name := f.Name()
				diskPath := filepath.Join(azureDiskPath, name)
				if link, linkErr := p.ioHandler.Readlink(diskPath); linkErr == nil {
					sd := link[(strings.LastIndex(link, "/") + 1):]
					azureDiskList = append(azureDiskList, sd)
				}
			}
		}
	}

	klog.V(12).Infof("Azure sys disks paths: %v", azureDiskList)

	return azureDiskList
}

func (p *NodeProvisioner) findDiskByLun(lun int) (string, error) {
	azureDisks := p.listAzureDiskPath()

	return p.findDiskByLunWithConstraint(lun, azureDisks)
}

// finds a device mounted to "current" node
func (p *NodeProvisioner) findDiskByLunWithConstraint(lun int, azureDisks []string) (string, error) {
	var err error
	sysPath := "/sys/bus/scsi/devices"

	if dirs, err := p.ioHandler.ReadDir(sysPath); err == nil {
		for _, f := range dirs {
			name := f.Name()
			// look for path like /sys/bus/scsi/devices/3:0:0:1
			arr := strings.Split(name, ":")

			if len(arr) < 4 {
				continue
			}

			if len(azureDisks) == 0 {
				klog.V(4).Infof("/dev/disk/azure is not populated, now try to parse %v directly", name)
				target, err := strconv.Atoi(arr[0])
				if err != nil {
					klog.Errorf("failed to parse target from %v (%v), err %v", arr[0], name, err)
					continue
				}
				// as observed, targets 0-3 are used by OS disks. Skip them
				if target <= 3 {
					continue
				}
			}

			// extract LUN from the path.
			// LUN is the last index of the array, i.e. 1 in /sys/bus/scsi/devices/3:0:0:1
			l, err := strconv.Atoi(arr[3])

			if err != nil {
				// unknown path format, continue to read the next one
				klog.V(4).Infof("azure disk - failed to parse lun from %v (%v), err %v", arr[3], name, err)
				continue
			}

			if lun == l {
				// find the matching LUN
				// read vendor and model to ensure it is a VHD disk
				vendorPath := filepath.Join(sysPath, name, "vendor")
				vendorBytes, err := p.ioHandler.ReadFile(vendorPath)
				if err != nil {
					klog.Errorf("failed to read device vendor, err: %v", err)
					continue
				}

				vendor := strings.TrimSpace(string(vendorBytes))

				if strings.ToUpper(vendor) != "MSFT" {
					klog.V(4).Infof("vendor doesn't match VHD, got %s", vendor)
					continue
				}

				modelPath := filepath.Join(sysPath, name, "model")
				modelBytes, err := p.ioHandler.ReadFile(modelPath)

				if err != nil {
					klog.Errorf("failed to read device model, err: %v", err)
					continue
				}

				model := strings.TrimSpace(string(modelBytes))

				if strings.ToUpper(model) != "VIRTUAL DISK" {
					klog.V(4).Infof("model doesn't match VHD, got %s", model)
					continue
				}

				// find a disk, validate name
				dir := filepath.Join(sysPath, name, "block")

				if dev, err := p.ioHandler.ReadDir(dir); err == nil {
					found := false
					devName := dev[0].Name()

					for _, diskName := range azureDisks {
						klog.V(12).Infof("azureDisk - validating disk %q with sys disk %q", devName, diskName)

						if devName == diskName {
							found = true
							break
						}
					}

					if !found {
						devLinkPaths := []string{"/dev/disk/azure/scsi1/", "/dev/disk/by-id/"}

						for _, devLinkPath := range devLinkPaths {
							diskPath, err := p.getDiskLinkByDevName(devLinkPath, devName)

							if err == nil {
								klog.V(4).Infof("azureDisk - found %s by %s under %s", diskPath, devName, devLinkPath)

								return diskPath, nil
							}

							klog.Warningf("azureDisk - getDiskLinkByDevName by %s under %s failed, error: %v", devName, devLinkPath, err)
						}

						return "/dev/" + devName, nil
					}
				}
			}
		}
	}
	return "", err
}

// getDiskLinkByDevName get disk link by device name from devLinkPath, e.g. /dev/disk/azure/, /dev/disk/by-id/
func (p *NodeProvisioner) getDiskLinkByDevName(devLinkPath, devName string) (string, error) {
	dirs, err := p.ioHandler.ReadDir(devLinkPath)
	klog.V(12).Infof("azureDisk - begin to find %s from %s", devName, devLinkPath)

	if err == nil {
		for _, f := range dirs {
			diskPath := devLinkPath + f.Name()
			klog.V(12).Infof("azureDisk - begin to Readlink: %s", diskPath)
			link, linkErr := p.ioHandler.Readlink(diskPath)

			if linkErr != nil {
				klog.Warningf("azureDisk - read link (%s) error: %v", diskPath, linkErr)
				continue
			}

			if strings.HasSuffix(link, devName) {
				return diskPath, nil
			}
		}

		return "", fmt.Errorf("device name(%s) is not found under %s", devName, devLinkPath)
	}

	return "", fmt.Errorf("read %s error: %v", devLinkPath, err)
}
