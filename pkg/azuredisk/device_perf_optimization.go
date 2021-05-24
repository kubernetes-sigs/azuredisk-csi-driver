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

// This is the interface for DeviceHelper
type Interface interface {
	DiskSupportsPerfOptimization(diskPerfProfile string, diskAccountType string) bool
	OptimizeDiskPerformance(nodeInfo *NodeInfo, diskSkus map[string]map[string]DiskSkuInfo, devicePath string,
		perfProfile string, accountType string, diskSizeGibStr string, diskIopsStr string, diskBwMbpsStr string) error
}

// Compile-time check to ensure all Mounter DeviceHelper satisfy
// the DeviceHelper interface.
var _ Interface = &DeviceHelper{}

func NewSafeDeviceHelper() *SafeDeviceHelper {
	return &SafeDeviceHelper{
		Interface: &DeviceHelper{},
	}
}

// IdentityServer is the server API for Identity service.
type SafeDeviceHelper struct {
	Interface
}

func (dh *SafeDeviceHelper) DeviceSupportsPerfOptimization(diskPerfProfile string, diskAccountType string) bool {
	return dh.Interface.DiskSupportsPerfOptimization(diskPerfProfile, diskAccountType)
}

func (dh *SafeDeviceHelper) OptimizeDiskPerformance(nodeInfo *NodeInfo, diskSkus map[string]map[string]DiskSkuInfo, devicePath string,
	perfProfile string, accountType string, diskSizeGibStr string, diskIopsStr string, diskBwMbpsStr string) error {
	return dh.Interface.OptimizeDiskPerformance(nodeInfo, diskSkus, devicePath, perfProfile, accountType, diskSizeGibStr, diskIopsStr, diskBwMbpsStr)
}
