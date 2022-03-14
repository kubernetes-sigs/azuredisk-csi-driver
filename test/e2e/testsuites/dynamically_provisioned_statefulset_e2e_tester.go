/*
Copyright 2020 The Kubernetes Authors.

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

package testsuites

import (
	"time"

	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/resources"
)

// DynamicallyProvisionedStatefulSetTest will provision required StorageClass and StatefulSet
// Testing if the Pod can write and read to mounted volumes
// Deleting a pod, and again testing if the Pod can write and read to mounted volumes
type DynamicallyProvisionedStatefulSetTest struct {
	CSIDriver driver.DynamicPVTestDriver
	Pod       resources.PodDetails
	PodCheck  *PodExecCheck
}

func (t *DynamicallyProvisionedStatefulSetTest) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	tStorageClass, storageCleanup := t.Pod.CreateStorageClass(client, namespace, t.CSIDriver, driver.GetParameters())
	defer storageCleanup()
	tStatefulSet, cleanup := t.Pod.SetupStatefulset(client, namespace, t.CSIDriver, schedulerName, 1, &tStorageClass, nil)
	// defer must be called here for resources not get removed before using them
	defer cleanup(15 * time.Minute)

	ginkgo.By("deploying the statefulset")
	tStatefulSet.Create()

	ginkgo.By("checking that the pod is running")
	err := tStatefulSet.WaitForPodReadyOrFail()
	framework.ExpectNoError(err)

	if t.PodCheck != nil {
		ginkgo.By("sleep 5s and then check pod exec")
		time.Sleep(5 * time.Second)
		tStatefulSet.Exec(t.PodCheck.Cmd, t.PodCheck.ExpectedString)
	}

	ginkgo.By("deleting the pod for statefulset")
	tStatefulSet.DeletePodAndWait()

	ginkgo.By("checking again that the pod is running")
	err = tStatefulSet.WaitForPodReadyOrFail()
	framework.ExpectNoError(err)

	if t.PodCheck != nil {
		ginkgo.By("sleep 5s and then check pod exec after pod restart again")
		time.Sleep(5 * time.Second)
		// pod will be restarted so expect to see 2 instances of string
		tStatefulSet.Exec(t.PodCheck.Cmd, t.PodCheck.ExpectedString+t.PodCheck.ExpectedString)
	}
}
