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

package e2e

import (
	"fmt"

	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/testsuites"
	"sigs.k8s.io/azuredisk-csi-driver/test/resources"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/testutil"
)

const (
	namespace     = consts.DefaultAzureDiskCrdNamespace
	schedulerName = "csi-azuredisk-scheduler-extender"
)

var _ = ginkgo.Describe("AzDiskSchedulerExtender", func() {

	ginkgo.Context("[single-az]", func() {
		schedulerExtenderTests(false)
	})

	ginkgo.Context("[multi-az]", func() {
		schedulerExtenderTests(true)
	})
})

func schedulerExtenderTests(isMultiZone bool) {
	f := framework.NewDefaultFramework("azuredisk")

	var (
		cs         clientset.Interface
		ns         *v1.Namespace
		testDriver driver.DynamicPVTestDriver
	)

	ginkgo.BeforeEach(func() {
		cs = f.ClientSet
		ns = f.Namespace
		testDriver = driver.InitAzureDiskDriver()
	})

	ginkgo.It("Should schedule and start a pod with no persistent volume requests. [Windows]", func() {
		testutil.SkipIfUsingInTreeVolumePlugin()
		testutil.SkipIfNotUsingCSIDriverV2()

		pod := resources.PodDetails{
			Cmd:          testutil.ConvertToPowershellorCmdCommandIfNecessary("echo 'hello world'"),
			Volumes:      []resources.VolumeDetails{},
			IsWindows:    testconsts.IsWindowsCluster,
			WinServerVer: testconsts.WinServerVer,
		}
		test := testsuites.AzDiskSchedulerExtenderSimplePodSchedulingTest{
			CSIDriver: testDriver,
			Pod:       pod,
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It("Should schedule and start a pod with a persistent volume requests. [Windows]", func() {
		testutil.SkipIfUsingInTreeVolumePlugin()
		testutil.SkipIfNotUsingCSIDriverV2()

		pod := resources.PodDetails{
			Cmd: testutil.ConvertToPowershellorCmdCommandIfNecessary("echo 'hello world' > /mnt/test-1/data && grep 'hello world' /mnt/test-1/data"),
			Volumes: resources.NormalizeVolumes([]resources.VolumeDetails{
				{
					FSType:    "ext3",
					ClaimSize: "10Gi",
					VolumeMount: resources.VolumeMountDetails{
						NameGenerate:      "test-volume-",
						MountPathGenerate: "/mnt/test-",
					},
				},
			}, []string{}, isMultiZone),
			IsWindows:    testconsts.IsWindowsCluster,
			WinServerVer: testconsts.WinServerVer,
		}

		test := testsuites.AzDiskSchedulerExtenderPodSchedulingWithPVTest{
			CSIDriver:              testDriver,
			Pod:                    pod,
			StorageClassParameters: map[string]string{consts.SkuNameField: "StandardSSD_LRS"},
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It("Should schedule and reschedule a pod with a persistent volume request on failover. [Windows]", func() {
		testutil.SkipIfUsingInTreeVolumePlugin()
		testutil.SkipIfNotUsingCSIDriverV2()

		volume := resources.VolumeDetails{
			FSType:    "ext3",
			ClaimSize: "10Gi",
			VolumeMount: resources.VolumeMountDetails{
				NameGenerate:      "test-volume-",
				MountPathGenerate: "/mnt/test-",
			},
		}
		pod := resources.PodDetails{
			Cmd: testutil.ConvertToPowershellorCmdCommandIfNecessary("while true; do echo $(date -u) >> /mnt/test-1/data; sleep 3600; done"),
			Volumes: resources.NormalizeVolumes([]resources.VolumeDetails{
				{
					ClaimSize: volume.ClaimSize,
					MountOptions: []string{
						"barrier=1",
						"acl",
					},
					VolumeMount: volume.VolumeMount,
				},
			}, []string{}, isMultiZone),
			IsWindows:    testconsts.IsWindowsCluster,
			WinServerVer: testconsts.WinServerVer,
			UseCMD:       false,
		}

		test := testsuites.AzDiskSchedulerExtenderPodSchedulingOnFailover{
			CSIDriver:              testDriver,
			Pod:                    pod,
			StorageClassParameters: map[string]string{consts.SkuNameField: "StandardSSD_LRS"},
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It("Should schedule and start a pod with multiple persistent volume requests. [Windows]", func() {
		testutil.SkipIfUsingInTreeVolumePlugin()
		testutil.SkipIfNotUsingCSIDriverV2()
		volumes := []resources.VolumeDetails{}

		for i := 1; i <= 3; i++ {
			volume := resources.VolumeDetails{
				FSType:    "ext3",
				ClaimSize: "10Gi",
				VolumeMount: resources.VolumeMountDetails{
					NameGenerate:      "test-volume-",
					MountPathGenerate: "/mnt/test-",
				},
			}
			volumes = append(volumes, volume)
		}

		pod := resources.PodDetails{
			Cmd:          testutil.ConvertToPowershellorCmdCommandIfNecessary("echo 'hello world' > /mnt/test-1/data && grep 'hello world' /mnt/test-1/data"),
			Volumes:      resources.NormalizeVolumes(volumes, []string{}, isMultiZone),
			IsWindows:    testconsts.IsWindowsCluster,
			WinServerVer: testconsts.WinServerVer,
		}
		test := testsuites.AzDiskSchedulerExtenderPodSchedulingWithMultiplePVTest{
			CSIDriver: testDriver,
			Pod:       pod,
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It("Should schedule and start a pod with multiple persistent volume requests and reschedule on failover.", func() {
		testutil.SkipIfUsingInTreeVolumePlugin()
		skuName := "StandardSSD_LRS"
		if isMultiZone {
			testutil.SkipIfNotZRSSupported(location)
			skuName = "StandardSSD_ZRS"
		}

		volumes := []resources.VolumeDetails{}
		azDiskClient, err := azDiskClientSet.NewForConfig(f.ClientConfig())
		if err != nil {
			ginkgo.Fail(fmt.Sprintf("Failed to create disk client. Error: %v", err))
		}
		for i := 1; i <= 3; i++ {
			volume := resources.VolumeDetails{
				FSType:    "ext3",
				ClaimSize: "10Gi",
				VolumeMount: resources.VolumeMountDetails{
					NameGenerate:      "test-volume-",
					MountPathGenerate: "/mnt/test-",
				},
			}
			volumes = append(volumes, volume)
		}

		pod := resources.PodDetails{
			Cmd:          testutil.ConvertToPowershellorCmdCommandIfNecessary("while true; do echo $(date -u) >> /mnt/test-1/data; sleep 3600; done"),
			Volumes:      resources.NormalizeVolumes(volumes, []string{}, isMultiZone),
			IsWindows:    testconsts.IsWindowsCluster,
			WinServerVer: testconsts.WinServerVer,
		}
		test := testsuites.AzDiskSchedulerExtenderPodSchedulingOnFailoverMultiplePV{
			CSIDriver:              testDriver,
			Pod:                    pod,
			Replicas:               1,
			StorageClassParameters: map[string]string{consts.SkuNameField: skuName},
			AzDiskClient:           azDiskClient,
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It("Should schedule and start a pod with multiple persistent volume requests with replicas and reschedule on deletion.", func() {
		testutil.SkipIfUsingInTreeVolumePlugin()
		skuName := "Premium_LRS"
		if isMultiZone {
			testutil.SkipIfNotZRSSupported(location)
			skuName = "Premium_ZRS"
		}

		volumes := []resources.VolumeDetails{}
		azDiskClient, err := azDiskClientSet.NewForConfig(f.ClientConfig())
		if err != nil {
			ginkgo.Fail(fmt.Sprintf("Failed to create disk client. Error: %v", err))
		}
		for i := 1; i <= 2; i++ {
			volume := resources.VolumeDetails{
				FSType:    "ext4",
				ClaimSize: "256Gi",
				VolumeMount: resources.VolumeMountDetails{
					NameGenerate:      "test-volume-",
					MountPathGenerate: "/mnt/test-",
				},
			}
			volumes = append(volumes, volume)
		}

		pod := resources.PodDetails{
			Cmd:          testutil.ConvertToPowershellorCmdCommandIfNecessary("while true; do echo $(date -u) >> /mnt/test-1/data; sleep 3600; done"),
			Volumes:      resources.NormalizeVolumes(volumes, []string{}, isMultiZone),
			IsWindows:    testconsts.IsWindowsCluster,
			WinServerVer: testconsts.WinServerVer,
		}
		test := testsuites.AzDiskSchedulerExtenderPodSchedulingOnFailoverMultiplePV{
			CSIDriver:              testDriver,
			Pod:                    pod,
			Replicas:               1,
			StorageClassParameters: map[string]string{consts.SkuNameField: skuName, "maxShares": "2", "cachingmode": "None"},
			AzDiskClient:           azDiskClient,
		}
		test.Run(cs, ns, schedulerName)
	})
}
