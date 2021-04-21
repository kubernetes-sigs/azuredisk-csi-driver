// /*
// Copyright 2021 The Kubernetes Authors.

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at

//     http://www.apache.org/licenses/LICENSE-2.0

// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// */

package e2e

import (
	"fmt"

	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	v1alpha1ClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/typed/azuredisk/v1alpha1"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/testsuites"
)

const (
	namespace     = "azure-disk-csi"
	schedulerName = "azdiskschedulerextender"
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
		cs                   clientset.Interface
		azDiskClientV1alpha1 v1alpha1ClientSet.DiskV1alpha1Interface
		ns                   *v1.Namespace
		testDriver           driver.DynamicPVTestDriver
	)

	ginkgo.BeforeEach(func() {
		cs = f.ClientSet
		ns = f.Namespace
		azDiskClient, err := azDiskClientSet.NewForConfig(f.ClientConfig())
		if err != nil {
			ginkgo.Fail(fmt.Sprintf("Failed to create disk client. Error: %v", err))
		}
		azDiskClientV1alpha1 = azDiskClient.DiskV1alpha1()
		testDriver = driver.InitAzureDiskDriver()
	})

	ginkgo.It("Should schedule and start a pod with no persistent volume requests.", func() {
		skipIfUsingInTreeVolumePlugin()
		skipIfNotUsingCSIDriverV2()

		pod := testsuites.PodDetails{
			Cmd:     convertToPowershellorCmdCommandIfNecessary("echo 'hello world'"),
			Volumes: []testsuites.VolumeDetails{},
		}
		test := testsuites.AzDiskSchedulerExtenderSimplePodSchedulingTest{
			CSIDriver:       testDriver,
			Pod:             pod,
			AzDiskClientSet: azDiskClientV1alpha1,
			AzNamespace:     namespace,
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It("Should schedule and start a pod with a persistent volume requests.", func() {
		skipIfUsingInTreeVolumePlugin()
		skipIfNotUsingCSIDriverV2()

		t := dynamicProvisioningTestSuite{}
		pod := testsuites.PodDetails{
			Cmd: convertToPowershellorCmdCommandIfNecessary("echo 'hello world' > /mnt/test-1/data && grep 'hello world' /mnt/test-1/data"),
			Volumes: t.normalizeVolumes([]testsuites.VolumeDetails{
				{
					FSType:    "ext4",
					ClaimSize: "10Gi",
					VolumeMount: testsuites.VolumeMountDetails{
						NameGenerate:      "test-volume-",
						MountPathGenerate: "/mnt/test-",
					},
				},
			}, isMultiZone),
		}

		test := testsuites.AzDiskSchedulerExtenderPodSchedulingWithPVTest{
			CSIDriver:              testDriver,
			Pod:                    pod,
			AzDiskClientSet:        azDiskClientV1alpha1,
			AzNamespace:            namespace,
			StorageClassParameters: map[string]string{"skuName": "StandardSSD_LRS"},
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It("Should schedule and rescheduler a pod with a persistent volume request on failover.", func() {
		skipIfUsingInTreeVolumePlugin()
		skipIfNotUsingCSIDriverV2()

		t := dynamicProvisioningTestSuite{}

		volume := testsuites.VolumeDetails{
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
			Volume:                 volume,
			AzDiskClientSet:        azDiskClientV1alpha1,
			AzNamespace:            namespace,
			StorageClassParameters: map[string]string{"skuName": "StandardSSD_LRS"},
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It("Should schedule and start a pod with multiple persistent volume requests.", func() {
		skipIfUsingInTreeVolumePlugin()
		skipIfNotUsingCSIDriverV2()
		volumes := []testsuites.VolumeDetails{}
		t := dynamicProvisioningTestSuite{}

		for i := 1; i <= 3; i++ {
			volume := testsuites.VolumeDetails{
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
			CSIDriver:       testDriver,
			Pod:             pod,
			AzDiskClientSet: azDiskClientV1alpha1,
			AzNamespace:     namespace,
		}
		test.Run(cs, ns, schedulerName)
	})
}
