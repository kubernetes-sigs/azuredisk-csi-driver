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

package optimization

import consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"

// This is the interface for DeviceHelper
type Interface interface {
	DiskSupportsPerfOptimization(diskPerfProfile, diskAccountType string) bool
	OptimizeDiskPerformance(nodeInfo *NodeInfo,
		devicePath, perfProfile, accountType, diskSizeGibStr, diskIopsStr, diskBwMbpsStr string, deviceSettingsFromCtx map[string]string) error
}

// Compile-time check to ensure all Mounter DeviceHelper satisfy
// the DeviceHelper interface.
var _ Interface = &DeviceHelper{blockDeviceRootPath: consts.BlockDeviceRootPathLinux}

func NewSafeDeviceHelper() *SafeDeviceHelper {
	return &SafeDeviceHelper{
		Interface: &DeviceHelper{blockDeviceRootPath: consts.BlockDeviceRootPathLinux},
	}
}

// IdentityServer is the server API for Identity service.
type SafeDeviceHelper struct {
	Interface
}

func (dh *SafeDeviceHelper) DeviceSupportsPerfOptimization(diskPerfProfile, diskAccountType string) bool {
	return dh.Interface.DiskSupportsPerfOptimization(diskPerfProfile, diskAccountType)
}

func (dh *SafeDeviceHelper) OptimizeDiskPerformance(nodeInfo *NodeInfo,
	devicePath, perfProfile, accountType, diskSizeGibStr, diskIopsStr, diskBwMbpsStr string, deviceSettingsFromCtx map[string]string) error {
	return dh.Interface.OptimizeDiskPerformance(nodeInfo, devicePath, perfProfile, accountType, diskSizeGibStr, diskIopsStr, diskBwMbpsStr, deviceSettingsFromCtx)
}
