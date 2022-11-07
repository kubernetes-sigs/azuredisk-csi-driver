/*
Copyright 2019 The Kubernetes Authors.

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

package testutil

import (
	"github.com/onsi/ginkgo/v2"
	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
)

func SkipIfTestingInWindowsCluster() {
	if testconsts.IsWindowsCluster {
		ginkgo.Skip("test case not supported by Windows clusters")
	}
}

func SkipIfUsingInTreeVolumePlugin() {
	if testconsts.IsUsingInTreeVolumePlugin {
		ginkgo.Skip("test case is only available for CSI drivers")
	}
}

func SkipIfNotUsingCSIDriverV2() {
	if !testconsts.IsUsingCSIDriverV2 {
		ginkgo.Skip("test case is only available for CSI driver version v2")
	}
}

func SkipIfOnAzureStackCloud() {
	if testconsts.IsAzureStackCloud {
		ginkgo.Skip("test case not supported on Azure Stack Cloud")
	}
}

func GetSchedulerForE2E() string {
	if !testconsts.IsUsingCSIDriverV2 {
		return "default-scheduler"
	}
	if testconsts.IsUsingOnlyDefaultScheduler {
		return "default-scheduler"
	}
	return "csi-azuredisk-scheduler-extender"
}

func IsUsingAzureDiskScheduler(schedulerName string) bool {
	return schedulerName == "csi-azuredisk-scheduler-extender"
}

func IsZRSSupported(location string) bool {
	return location == "westus2" || location == "westeurope" || location == "northeurope" || location == "francecentral"
}

func SkipIfNotZRSSupported(location string) {
	if !(IsZRSSupported(location)) {
		ginkgo.Skip("test case not supported on regions without ZRS")
	}
}

func SkipIfNotDynamicallyResizeSupported(location string) {
	supportsDynamicResize := false
	for _, zone := range testconsts.DynamicResizeZones {
		if location == zone {
			supportsDynamicResize = true
			break
		}
	}

	if !supportsDynamicResize {
		ginkgo.Skip("test case not supported no regions without dynamic resize support")
	}
}

func GetFSType(IsWindowsCluster bool) string {
	if IsWindowsCluster {
		return "ntfs"
	}
	return "ext4"
}
