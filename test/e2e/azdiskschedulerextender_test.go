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
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/testsuites"
)

const (
	namespace     = consts.AzureDiskCrdNamespace
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
		skipIfUsingInTreeVolumePlugin()
		skipIfNotUsingCSIDriverV2()

		pod := testsuites.PodDetails{
			Cmd:       convertToPowershellorCmdCommandIfNecessary("echo 'hello world'"),
			Volumes:   []testsuites.VolumeDetails{},
			IsWindows: isWindowsCluster,
		}
		test := testsuites.AzDiskSchedulerExtenderSimplePodSchedulingTest{
			CSIDriver: testDriver,
			Pod:       pod,
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It("Should schedule and start a pod with a persistent volume requests. [Windows]", func() {
		skipIfUsingInTreeVolumePlugin()
		skipIfNotUsingCSIDriverV2()

		t := dynamicProvisioningTestSuite{}
		pod := testsuites.PodDetails{
			Cmd: convertToPowershellorCmdCommandIfNecessary("echo 'hello world' > /mnt/test-1/data && grep 'hello world' /mnt/test-1/data"),
			Volumes: t.normalizeVolumes([]testsuites.VolumeDetails{
				{
					FSType:    "ext3",
					ClaimSize: "10Gi",
					VolumeMount: testsuites.VolumeMountDetails{
						NameGenerate:      "test-volume-",
						MountPathGenerate: "/mnt/test-",
					},
				},
			}, isMultiZone),
			IsWindows: isWindowsCluster,
		}

		test := testsuites.AzDiskSchedulerExtenderPodSchedulingWithPVTest{
			CSIDriver:              testDriver,
			Pod:                    pod,
			StorageClassParameters: map[string]string{"skuName": "StandardSSD_LRS"},
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It("Should schedule and reschedule a pod with a persistent volume request on failover. [Windows]", func() {
		skipIfUsingInTreeVolumePlugin()
		skipIfNotUsingCSIDriverV2()

		t := dynamicProvisioningTestSuite{}

		volume := testsuites.VolumeDetails{
			FSType:    "ext3",
			ClaimSize: "10Gi",
			VolumeMount: testsuites.VolumeMountDetails{
				NameGenerate:      "test-volume-",
				MountPathGenerate: "/mnt/test-",
			},
		}
		pod := testsuites.PodDetails{
			Cmd: convertToPowershellorCmdCommandIfNecessary("while true; do echo $(date -u) >> /mnt/test-1/data; sleep 3600; done"),
			Volumes: t.normalizeVolumes([]testsuites.VolumeDetails{
				{
					ClaimSize: volume.ClaimSize,
					MountOptions: []string{
						"barrier=1",
						"acl",
					},
					VolumeMount: volume.VolumeMount,
				},
			}, isMultiZone),
			IsWindows: isWindowsCluster,
			UseCMD:    false,
		}

		test := testsuites.AzDiskSchedulerExtenderPodSchedulingOnFailover{
			CSIDriver:              testDriver,
			Pod:                    pod,
			StorageClassParameters: map[string]string{"skuName": "StandardSSD_LRS"},
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It("Should schedule and start a pod with multiple persistent volume requests. [Windows]", func() {
		skipIfUsingInTreeVolumePlugin()
		skipIfNotUsingCSIDriverV2()
		volumes := []testsuites.VolumeDetails{}
		t := dynamicProvisioningTestSuite{}

		for i := 1; i <= 3; i++ {
			volume := testsuites.VolumeDetails{
				FSType:    "ext3",
				ClaimSize: "10Gi",
				VolumeMount: testsuites.VolumeMountDetails{
					NameGenerate:      "test-volume-",
					MountPathGenerate: "/mnt/test-",
				},
			}
			volumes = append(volumes, volume)
		}

		pod := testsuites.PodDetails{
			Cmd:       convertToPowershellorCmdCommandIfNecessary("echo 'hello world' > /mnt/test-1/data && grep 'hello world' /mnt/test-1/data"),
			Volumes:   t.normalizeVolumes(volumes, isMultiZone),
			IsWindows: isWindowsCluster,
		}
		test := testsuites.AzDiskSchedulerExtenderPodSchedulingWithMultiplePVTest{
			CSIDriver: testDriver,
			Pod:       pod,
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It("Should schedule and start a pod with multiple persistent volume requests and reschedule on failover.", func() {
		skipIfUsingInTreeVolumePlugin()
		skuName := "StandardSSD_LRS"
		if isMultiZone {
			skipIfNotZRSSupported()
			skuName = "StandardSSD_ZRS"
		}

		volumes := []testsuites.VolumeDetails{}
		t := dynamicProvisioningTestSuite{}
		azDiskClient, err := azDiskClientSet.NewForConfig(f.ClientConfig())
		if err != nil {
			ginkgo.Fail(fmt.Sprintf("Failed to create disk client. Error: %v", err))
		}
		for i := 1; i <= 3; i++ {
			volume := testsuites.VolumeDetails{
				FSType:    "ext3",
				ClaimSize: "10Gi",
				VolumeMount: testsuites.VolumeMountDetails{
					NameGenerate:      "test-volume-",
					MountPathGenerate: "/mnt/test-",
				},
			}
			volumes = append(volumes, volume)
		}

		pod := testsuites.PodDetails{
			Cmd:       convertToPowershellorCmdCommandIfNecessary("while true; do echo $(date -u) >> /mnt/test-1/data; sleep 3600; done"),
			Volumes:   t.normalizeVolumes(volumes, isMultiZone),
			IsWindows: isWindowsCluster,
		}
		test := testsuites.AzDiskSchedulerExtenderPodSchedulingOnFailoverMultiplePV{
			CSIDriver:              testDriver,
			Pod:                    pod,
			Replicas:               1,
			StorageClassParameters: map[string]string{"skuName": skuName},
			AzDiskClient:           azDiskClient,
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It("Should schedule and start a pod with multiple persistent volume requests with replicas and reschedule on deletion.", func() {
		skipIfUsingInTreeVolumePlugin()
		skuName := "Premium_LRS"
		if isMultiZone {
			skipIfNotZRSSupported()
			skuName = "Premium_ZRS"
		}

		volumes := []testsuites.VolumeDetails{}
		t := dynamicProvisioningTestSuite{}
		azDiskClient, err := azDiskClientSet.NewForConfig(f.ClientConfig())
		if err != nil {
			ginkgo.Fail(fmt.Sprintf("Failed to create disk client. Error: %v", err))
		}
		for i := 1; i <= 2; i++ {
			volume := testsuites.VolumeDetails{
				FSType:    "ext4",
				ClaimSize: "256Gi",
				VolumeMount: testsuites.VolumeMountDetails{
					NameGenerate:      "test-volume-",
					MountPathGenerate: "/mnt/test-",
				},
			}
			volumes = append(volumes, volume)
		}

		pod := testsuites.PodDetails{
			Cmd:       convertToPowershellorCmdCommandIfNecessary("while true; do echo $(date -u) >> /mnt/test-1/data; sleep 3600; done"),
			Volumes:   t.normalizeVolumes(volumes, isMultiZone),
			IsWindows: isWindowsCluster,
		}
		test := testsuites.AzDiskSchedulerExtenderPodSchedulingOnFailoverMultiplePV{
			CSIDriver:              testDriver,
			Pod:                    pod,
			Replicas:               1,
			StorageClassParameters: map[string]string{"skuName": skuName, "maxShares": "2", "cachingmode": "None"},
			AzDiskClient:           azDiskClient,
		}
		test.Run(cs, ns, schedulerName)
	})
}
