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
	"fmt"

	"github.com/onsi/ginkgo/v2"
	v1 "k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	admissionapi "k8s.io/pod-security-admission/api"
	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/resources"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/testutil"
)

var _ = ginkgo.Describe("Scale testing", func() {

	ginkgo.Context("[single-az]", func() {
		scaleTests(false)
	})

})

func scaleTests(isMultiZone bool) {
	f := framework.NewDefaultFramework("azuredisk")

	// Apply the minmally restrictive baseline Pod Security Standard profile to namespaces
	// created by the Kubernetes end-to-end test framework to enable testing with a nil
	// Pod security context.
	f.NamespacePodSecurityEnforceLevel = admissionapi.LevelBaseline

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

	ginkgo.It("Scale test scheduling and starting multiple pods with persistent volumes.", func() {
		testutil.SkipIfUsingInTreeVolumePlugin()
		volumes := []resources.VolumeDetails{}
		for j := 1; j <= *volMountedToPod; j++ {
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

		azDiskClient, err := azdisk.NewForConfig(f.ClientConfig())
		if err != nil {
			ginkgo.Fail(fmt.Sprintf("Failed to create disk client. Error: %v", err))
		}

		pod := resources.PodDetails{
			Cmd:          testutil.ConvertToPowershellorCmdCommandIfNecessary("while true; do echo $(date -u) >> /mnt/test-1/data; sleep 3600; done"),
			Volumes:      resources.NormalizeVolumes(volumes, []string{}, isMultiZone),
			IsWindows:    testconsts.IsWindowsCluster,
			WinServerVer: testconsts.WinServerVer,
		}
		test := PodSchedulingWithPVScaleTest{}

		test.CSIDriver = testDriver
		test.Pod = pod
		test.Replicas = *testerReplicas
		test.MaxShares = *testerMaxShares
		test.StorageClassParameters = map[string]string{consts.SkuNameField: "Premium_LRS", "maxShares": fmt.Sprint(*testerMaxShares), "cachingmode": "None"}
		test.AzDiskClient = azDiskClient

		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It("Scale test scheduling and rescheduling multiple pods with persistent volumes on failover.", func() {
		testutil.SkipIfUsingInTreeVolumePlugin()
		volumes := []resources.VolumeDetails{}
		for j := 1; j <= *volMountedToPod; j++ {
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

		azDiskClient, err := azdisk.NewForConfig(f.ClientConfig())
		if err != nil {
			ginkgo.Fail(fmt.Sprintf("Failed to create disk client. Error: %v", err))
		}

		pod := resources.PodDetails{
			Cmd:          testutil.ConvertToPowershellorCmdCommandIfNecessary("while true; do echo $(date -u) >> /mnt/test-1/data; sleep 3600; done"),
			Volumes:      resources.NormalizeVolumes(volumes, []string{}, isMultiZone),
			IsWindows:    testconsts.IsWindowsCluster,
			WinServerVer: testconsts.WinServerVer,
		}
		test := PodSchedulingOnFailoverScaleTest{}

		test.CSIDriver = testDriver
		test.Pod = pod
		test.Replicas = *testerReplicas
		test.StorageClassParameters = map[string]string{consts.SkuNameField: "Premium_LRS", "maxShares": "2", "cachingmode": "None"}
		test.AzDiskClient = azDiskClient

		test.Run(cs, ns, schedulerName)
	})
}
