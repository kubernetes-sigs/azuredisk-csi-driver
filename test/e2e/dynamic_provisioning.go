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

package e2e

import (
	"fmt"

	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/testsuites"

	. "github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
)

var _ = Describe("[azuredisk-csi-e2e] Dynamic Provisioning", func() {
	t := dynamicProvisioningTestSuite{}

	Context("[single-az]", func() {
		t.defineTests(false)
	})

	Context("[multi-az]", func() {
		t.defineTests(true)
	})
})

type dynamicProvisioningTestSuite struct {
	allowedTopologyValues []string
}

func (t *dynamicProvisioningTestSuite) defineTests(isMultiZone bool) {
	f := framework.NewDefaultFramework("azuredisk")

	var (
		cs         clientset.Interface
		ns         *v1.Namespace
		testDriver driver.DynamicPVTestDriver
	)

	BeforeEach(func() {
		cs = f.ClientSet
		ns = f.Namespace
		// Populate allowedTopologyValues from node labels
		if isMultiZone && len(t.allowedTopologyValues) == 0 {
			nodes, err := cs.CoreV1().Nodes().List(metav1.ListOptions{})
			framework.ExpectNoError(err)
			allowedTopologyValuesMap := make(map[string]bool)
			for _, node := range nodes.Items {
				if zone, ok := node.Labels[driver.TopologyKey]; ok {
					allowedTopologyValuesMap[zone] = true
				}
			}
			for k := range allowedTopologyValuesMap {
				t.allowedTopologyValues = append(t.allowedTopologyValues, k)
			}
		}
	})

	testDriver = driver.InitAzureDiskDriver()
	It(fmt.Sprintf("should create a volume on demand"), func() {
		pod := testsuites.PodDetails{
			Cmd: "echo 'hello world' > /mnt/test-1/data && grep 'hello world' /mnt/test-1/data",
			Volumes: []testsuites.VolumeDetails{
				{
					ClaimSize: "10Gi",
					VolumeMount: testsuites.VolumeMountDetails{
						NameGenerate:      "test-volume-",
						MountPathGenerate: "/mnt/test-",
					},
				},
			},
		}
		if isMultiZone {
			pod.Volumes = t.injectAllowedTopologyValuesAndVolumeBindingMode(pod.Volumes)
		}
		test := testsuites.DynamicallyProvisionedCmdVolumeTest{
			CSIDriver: testDriver,
			Pods:      []testsuites.PodDetails{pod},
		}
		test.Run(cs, ns)
	})

	It(fmt.Sprintf("should create a raw block volume on demand"), func() {
		pod := testsuites.PodDetails{
			Cmd: "ls /dev | grep e2e-test",
			Volumes: []testsuites.VolumeDetails{
				{
					ClaimSize:  "10Gi",
					VolumeMode: testsuites.Block,
					VolumeDevice: testsuites.VolumeDeviceDetails{
						NameGenerate: "test-volume-",
						DevicePath:   "/dev/e2e-test",
					},
				},
			},
		}
		if isMultiZone {
			pod.Volumes = t.injectAllowedTopologyValuesAndVolumeBindingMode(pod.Volumes)
		}
		test := testsuites.DynamicallyProvisionedCmdVolumeTest{
			CSIDriver: testDriver,
			Pods:      []testsuites.PodDetails{pod},
		}
		test.Run(cs, ns)
	})

	//Track issue https://github.com/kubernetes/kubernetes/issues/70505
	It("should create a volume on demand and mount it as readOnly in a pod", func() {
		pod := testsuites.PodDetails{
			Cmd: "touch /mnt/test-1/data",
			Volumes: []testsuites.VolumeDetails{
				{
					FSType:    "ext4",
					ClaimSize: "10Gi",
					VolumeMount: testsuites.VolumeMountDetails{
						NameGenerate:      "test-volume-",
						MountPathGenerate: "/mnt/test-",
						ReadOnly:          true,
					},
				},
			},
		}
		if isMultiZone {
			pod.Volumes = t.injectAllowedTopologyValuesAndVolumeBindingMode(pod.Volumes)
		}
		test := testsuites.DynamicallyProvisionedReadOnlyVolumeTest{
			CSIDriver: testDriver,
			Pods:      []testsuites.PodDetails{pod},
		}
		test.Run(cs, ns)
	})

	It("should create multiple PV objects, bind to PVCs and attach all to different pods on the same node", func() {
		pods := []testsuites.PodDetails{
			{
				Cmd: "while true; do echo $(date -u) >> /mnt/test-1/data; sleep 1; done",
				Volumes: []testsuites.VolumeDetails{
					{
						FSType:    "ext3",
						ClaimSize: "10Gi",
						VolumeMount: testsuites.VolumeMountDetails{
							NameGenerate:      "test-volume-",
							MountPathGenerate: "/mnt/test-",
						},
					},
				},
			},
			{
				Cmd: "while true; do echo $(date -u) >> /mnt/test-1/data; sleep 1; done",
				Volumes: []testsuites.VolumeDetails{
					{
						FSType:    "ext4",
						ClaimSize: "10Gi",
						VolumeMount: testsuites.VolumeMountDetails{
							NameGenerate:      "test-volume-",
							MountPathGenerate: "/mnt/test-",
						},
					},
				},
			},
			{
				Cmd: "while true; do echo $(date -u) >> /mnt/test-1/data; sleep 1; done",
				Volumes: []testsuites.VolumeDetails{
					{
						FSType:    "xfs",
						ClaimSize: "10Gi",
						VolumeMount: testsuites.VolumeMountDetails{
							NameGenerate:      "test-volume-",
							MountPathGenerate: "/mnt/test-",
						},
					},
				},
			},
		}
		if isMultiZone {
			for i := range pods {
				pods[i].Volumes = t.injectAllowedTopologyValuesAndVolumeBindingMode(pods[i].Volumes)
			}
		}
		test := testsuites.DynamicallyProvisionedCollocatedPodTest{
			CSIDriver:    testDriver,
			Pods:         pods,
			ColocatePods: true,
		}
		test.Run(cs, ns)
	})

	It("should create a deployment object, write and read to it, delete the pod and write and read to it again", func() {
		pod := testsuites.PodDetails{
			Cmd: "echo 'hello world' >> /mnt/test-1/data && while true; do sleep 1; done",
			Volumes: []testsuites.VolumeDetails{
				{
					FSType:    "ext3",
					ClaimSize: "10Gi",
					VolumeMount: testsuites.VolumeMountDetails{
						NameGenerate:      "test-volume-",
						MountPathGenerate: "/mnt/test-",
					},
				},
			},
		}
		if isMultiZone {
			pod.Volumes = t.injectAllowedTopologyValuesAndVolumeBindingMode(pod.Volumes)
		}
		test := testsuites.DynamicallyProvisionedDeletePodTest{
			CSIDriver: testDriver,
			Pod:       pod,
			PodCheck: &testsuites.PodExecCheck{
				Cmd:            []string{"cat", "/mnt/test-1/data"},
				ExpectedString: "hello world\nhello world\n", // pod will be restarted so expect to see 2 instances of string
			},
		}
		test.Run(cs, ns)
	})

	It(fmt.Sprintf("should delete PV with reclaimPolicy %q", v1.PersistentVolumeReclaimDelete), func() {
		reclaimPolicy := v1.PersistentVolumeReclaimDelete
		volumes := []testsuites.VolumeDetails{
			{
				FSType:        "ext4",
				ClaimSize:     "10Gi",
				ReclaimPolicy: &reclaimPolicy,
			},
		}
		if isMultiZone {
			volumes = t.injectAllowedTopologyValuesAndVolumeBindingMode(volumes)
		}
		test := testsuites.DynamicallyProvisionedReclaimPolicyTest{
			CSIDriver: testDriver,
			Volumes:   volumes,
		}
		test.Run(cs, ns)
	})

	It(fmt.Sprintf("[env] should retain PV with reclaimPolicy %q", v1.PersistentVolumeReclaimRetain), func() {
		// This tests uses the CSI driver to delete the PV.
		// TODO: Go via the k8s interfaces and also make it more reliable for in-tree and then
		//       test can be enabled.
		if testDriver.IsInTree() {
			Skip("Test running with in tree configuration")
		}
		reclaimPolicy := v1.PersistentVolumeReclaimRetain
		volumes := []testsuites.VolumeDetails{
			{
				FSType:        "ext4",
				ClaimSize:     "10Gi",
				ReclaimPolicy: &reclaimPolicy,
			},
		}
		if isMultiZone {
			volumes = t.injectAllowedTopologyValuesAndVolumeBindingMode(volumes)
		}
		test := testsuites.DynamicallyProvisionedReclaimPolicyTest{
			CSIDriver: testDriver,
			Volumes:   volumes,
			Azuredisk: azurediskDriver,
		}
		test.Run(cs, ns)
	})

	It("cloning a volume from an existing volume", func() {
		pod := testsuites.PodDetails{
			Cmd: "echo 'hello world' >> /mnt/test-1/data && while true; do sleep 1; done",
			Volumes: []testsuites.VolumeDetails{
				{
					FSType:    "ext4",
					ClaimSize: "10Gi",
					VolumeMount: testsuites.VolumeMountDetails{
						NameGenerate:      "test-volume-",
						MountPathGenerate: "/mnt/test-",
					},
				},
			},
		}
		volume := testsuites.VolumeDetails{
			FSType:    "ext4",
			ClaimSize: "10Gi",
			DataSource: &testsuites.DataSource{
				Kind: testsuites.VolumePVCKind,
			},
		}
		test := testsuites.DynamicallyProvisionedVolumeCloningTest{
			CSIDriver: testDriver,
			Pod:       pod,
			Volume:    volume,
		}
		test.Run(cs, ns)
	})
}

func (t *dynamicProvisioningTestSuite) injectAllowedTopologyValuesAndVolumeBindingMode(volumes []testsuites.VolumeDetails) []testsuites.VolumeDetails {
	for i := range volumes {
		volumes[i].AllowedTopologyValues = t.allowedTopologyValues
		volumeBindingMode := storagev1.VolumeBindingWaitForFirstConsumer
		volumes[i].VolumeBindingMode = &volumeBindingMode
	}
	return volumes
}
