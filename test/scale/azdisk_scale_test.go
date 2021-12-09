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

package scale

import (
	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	testtypes "sigs.k8s.io/azuredisk-csi-driver/test/types"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/testutil"
)

var _ = ginkgo.Describe("Scale testing", func() {

	ginkgo.Context("[single-az]", func() {
		scaleTests(false)
	})

})

func scaleTests(isMultiZone bool) {
	f := framework.NewDefaultFramework("azuredisk")

	var (
		cs            clientset.Interface
		ns            *v1.Namespace
		testDriver    driver.DynamicPVTestDriver
		schedulerName = testutil.GetSchedulerForE2E()
	)

	ginkgo.BeforeEach(func() {
		cs = f.ClientSet
		ns = f.Namespace
		testDriver = driver.InitAzureDiskDriver()
	})

	ginkgo.It("Scale test scheduling and starting multiple pods with a persistent volume.", func() {
		testutil.SkipIfUsingInTreeVolumePlugin()
		volumes := []testtypes.VolumeDetails{}
		for j := 1; j <= 1; j++ {
			volume := testtypes.VolumeDetails{
				FSType:    "ext4",
				ClaimSize: "256Gi",
				VolumeMount: testtypes.VolumeMountDetails{
					NameGenerate:      "test-volume-",
					MountPathGenerate: "/mnt/test-",
				},
			}
			volumes = append(volumes, volume)
		}

		pod := testtypes.PodDetails{
			Cmd:       testutil.ConvertToPowershellorCmdCommandIfNecessary("while true; do echo $(date -u) >> /mnt/test-1/data; sleep 3600; done"),
			Volumes:   testutil.NormalizeVolumes(volumes, []string{}, isMultiZone),
			IsWindows: testconsts.IsWindowsCluster,
		}
		test := PodSchedulingWithPVScaleTest{}

		test.CSIDriver = testDriver
		test.Pod = pod
		test.Replicas = 1000
		test.StorageClassParameters = map[string]string{"skuName": "Premium_LRS", "maxShares": "1", "cachingmode": "None"}

		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It("Scale test scheduling and rescheduling multiple pod with a persistent volume request on failover.", func() {
		testutil.SkipIfUsingInTreeVolumePlugin()
		testutil.SkipIfNotUsingCSIDriverV2()

		volume := testtypes.VolumeDetails{
			FSType:    "ext3",
			ClaimSize: "10Gi",
			VolumeMount: testtypes.VolumeMountDetails{
				NameGenerate:      "test-volume-",
				MountPathGenerate: "/mnt/test-",
			},
			MountOptions: []string{
				"barrier=1",
				"acl",
			},
		}

		pod := testtypes.PodDetails{
			Cmd:       testutil.ConvertToPowershellorCmdCommandIfNecessary("while true; do echo $(date -u) >> /mnt/test-1/data; sleep 3600; done"),
			Volumes:   testutil.NormalizeVolumes([]testtypes.VolumeDetails{volume}, []string{}, isMultiZone),
			IsWindows: testconsts.IsWindowsCluster,
			UseCMD:    false,
		}

		test := PodSchedulingOnFailoverScaleTest{}
		test.CSIDriver = testDriver
		test.Pod = pod
		test.Replicas = 1
		test.PodCount = 1000
		test.StorageClassParameters = map[string]string{"skuName": "StandardSSD_LRS", "maxShares": "2", "cachingmode": "None"}

		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It("Scale test scheduling and starting multiple pod with multiple persistent volume requests.", func() {
		testutil.SkipIfUsingInTreeVolumePlugin()
		testutil.SkipIfNotUsingCSIDriverV2()

		volMountedToPod := 3
		volumes := []testtypes.VolumeDetails{}
		for j := 1; j <= volMountedToPod; j++ {
			volume := testtypes.VolumeDetails{
				FSType:    "ext3",
				ClaimSize: "10Gi",
				VolumeMount: testtypes.VolumeMountDetails{
					NameGenerate:      "test-volume-",
					MountPathGenerate: "/mnt/test-",
				},
			}
			volumes = append(volumes, volume)
		}

		pod := testtypes.PodDetails{
			Cmd:       testutil.ConvertToPowershellorCmdCommandIfNecessary("echo 'hello world' > /mnt/test-1/data && grep 'hello world' /mnt/test-1/data"),
			Volumes:   testutil.NormalizeVolumes(volumes, []string{}, isMultiZone),
			IsWindows: testconsts.IsWindowsCluster,
		}

		test := PodSchedulingWithPVScaleTest{}
		test.CSIDriver = testDriver
		test.Pod = pod
		test.Replicas = 350

		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It("Scale test scheduling and starting multiple pods with multiple persistent volume requests with replicas and reschedule on deletion.", func() {
		testutil.SkipIfUsingInTreeVolumePlugin()

		volMountedOnPod := 3
		volumes := []testtypes.VolumeDetails{}
		for j := 1; j <= volMountedOnPod; j++ {
			volume := testtypes.VolumeDetails{
				FSType:    "ext4",
				ClaimSize: "256Gi",
				VolumeMount: testtypes.VolumeMountDetails{
					NameGenerate:      "test-volume-",
					MountPathGenerate: "/mnt/test-",
				},
			}
			volumes = append(volumes, volume)
		}

		pod := testtypes.PodDetails{
			Cmd:       testutil.ConvertToPowershellorCmdCommandIfNecessary("while true; do echo $(date -u) >> /mnt/test-1/data; sleep 3600; done"),
			Volumes:   testutil.NormalizeVolumes(volumes, []string{}, isMultiZone),
			IsWindows: testconsts.IsWindowsCluster,
		}

		test := PodSchedulingOnFailoverScaleTest{}
		test.CSIDriver = testDriver
		test.Pod = pod
		test.Replicas = volMountedOnPod
		test.PodCount = 350
		test.StorageClassParameters = map[string]string{"skuName": "Premium_LRS", "maxShares": "2", "cachingmode": "None"}

		test.Run(cs, ns, schedulerName)
	})
}
