//go:build !linux
// +build !linux

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

import "fmt"

type DeviceHelper struct {
	blockDeviceRootPath string
}

func (deviceHelper *DeviceHelper) DiskSupportsPerfOptimization(diskPerfProfile, diskAccountType string) bool {
	// return false on unsupported platforms
	return false
}

func (deviceHelper *DeviceHelper) OptimizeDiskPerformance(nodeInfo *NodeInfo,
	devicePath, perfProfile, accountType, diskSizeGibStr, diskIopsStr, diskBwMbpsStr string) (err error) {
	return fmt.Errorf("OptimizeDiskPerformance not implemented")
}
