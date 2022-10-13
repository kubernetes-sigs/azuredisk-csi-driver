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

package resources

import (
	"context"
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"sigs.k8s.io/cloud-provider-azure/pkg/provider"

	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	e2elog "k8s.io/kubernetes/test/e2e/framework/log"
	e2epv "k8s.io/kubernetes/test/e2e/framework/pv"
)

type TestPersistentVolumeClaim struct {
	Client                         clientset.Interface
	ClaimSize                      string
	VolumeMode                     v1.PersistentVolumeMode
	AccessMode                     v1.PersistentVolumeAccessMode
	StorageClass                   *storagev1.StorageClass
	Namespace                      *v1.Namespace
	PersistentVolume               *v1.PersistentVolume
	PersistentVolumeClaim          *v1.PersistentVolumeClaim
	RequestedPersistentVolumeClaim *v1.PersistentVolumeClaim
	DataSource                     *v1.TypedLocalObjectReference
}

func NewTestPersistentVolumeClaim(c clientset.Interface, ns *v1.Namespace, claimSize string, volumeMode VolumeMode, accessMode v1.PersistentVolumeAccessMode, sc *storagev1.StorageClass) *TestPersistentVolumeClaim {
	mode := v1.PersistentVolumeFilesystem
	if volumeMode == Block {
		mode = v1.PersistentVolumeBlock
	}
	return &TestPersistentVolumeClaim{
		Client:       c,
		ClaimSize:    claimSize,
		VolumeMode:   mode,
		AccessMode:   accessMode,
		Namespace:    ns,
		StorageClass: sc,
	}
}

func NewTestPersistentVolumeClaimWithDataSource(c clientset.Interface, ns *v1.Namespace, claimSize string, volumeMode VolumeMode, accessMode v1.PersistentVolumeAccessMode, sc *storagev1.StorageClass, dataSource *v1.TypedLocalObjectReference) *TestPersistentVolumeClaim {
	mode := v1.PersistentVolumeFilesystem
	if volumeMode == Block {
		mode = v1.PersistentVolumeBlock
	}
	return &TestPersistentVolumeClaim{
		Client:       c,
		ClaimSize:    claimSize,
		VolumeMode:   mode,
		AccessMode:   accessMode,
		Namespace:    ns,
		StorageClass: sc,
		DataSource:   dataSource,
	}
}

func (t *TestPersistentVolumeClaim) Create() {
	var err error

	ginkgo.By("creating a PVC")
	storageClassName := ""
	if t.StorageClass != nil {
		storageClassName = t.StorageClass.Name
	}
	t.RequestedPersistentVolumeClaim = generatePVC(t.Namespace.Name, storageClassName, "", t.ClaimSize, t.VolumeMode, t.AccessMode, t.DataSource)
	t.PersistentVolumeClaim, err = t.Client.CoreV1().PersistentVolumeClaims(t.Namespace.Name).Create(context.TODO(), t.RequestedPersistentVolumeClaim, metav1.CreateOptions{})
	framework.ExpectNoError(err)
}

func (t *TestPersistentVolumeClaim) ValidateProvisionedPersistentVolume() {
	var err error

	// Get the bound PersistentVolume
	ginkgo.By("validating provisioned PV")
	t.PersistentVolume, err = t.Client.CoreV1().PersistentVolumes().Get(context.TODO(), t.PersistentVolumeClaim.Spec.VolumeName, metav1.GetOptions{})
	framework.ExpectNoError(err)

	// Check sizes
	expectedCapacity := t.RequestedPersistentVolumeClaim.Spec.Resources.Requests[v1.ResourceName(v1.ResourceStorage)]
	claimCapacity := t.PersistentVolumeClaim.Spec.Resources.Requests[v1.ResourceName(v1.ResourceStorage)]
	gomega.Expect(claimCapacity.Value()).To(gomega.Equal(expectedCapacity.Value()), "claimCapacity is not equal to requestedCapacity")

	pvCapacity := t.PersistentVolume.Spec.Capacity[v1.ResourceName(v1.ResourceStorage)]
	gomega.Expect(pvCapacity.Value()).To(gomega.Equal(expectedCapacity.Value()), "pvCapacity is not equal to requestedCapacity")

	// Check PV properties
	ginkgo.By("checking the PV")
	expectedAccessModes := t.RequestedPersistentVolumeClaim.Spec.AccessModes
	gomega.Expect(t.PersistentVolume.Spec.AccessModes).To(gomega.Equal(expectedAccessModes))
	gomega.Expect(t.PersistentVolume.Spec.ClaimRef.Name).To(gomega.Equal(t.PersistentVolumeClaim.ObjectMeta.Name))
	gomega.Expect(t.PersistentVolume.Spec.ClaimRef.Namespace).To(gomega.Equal(t.PersistentVolumeClaim.ObjectMeta.Namespace))
	// If storageClass is nil, PV was pre-provisioned with these values already set
	if t.StorageClass != nil {
		gomega.Expect(t.PersistentVolume.Spec.PersistentVolumeReclaimPolicy).To(gomega.Equal(*t.StorageClass.ReclaimPolicy))
		gomega.Expect(t.PersistentVolume.Spec.MountOptions).To(gomega.Equal(t.StorageClass.MountOptions))
		if *t.StorageClass.VolumeBindingMode == storagev1.VolumeBindingWaitForFirstConsumer {
			gomega.Expect(t.PersistentVolume.Spec.NodeAffinity.Required.NodeSelectorTerms[0].MatchExpressions[0].Values).
				To(gomega.HaveLen(1))
		}
	}
}

func (t *TestPersistentVolumeClaim) WaitForBound() v1.PersistentVolumeClaim {
	var err error

	ginkgo.By(fmt.Sprintf("waiting for PVC to be in phase %q", v1.ClaimBound))
	err = e2epv.WaitForPersistentVolumeClaimPhase(v1.ClaimBound, t.Client, t.Namespace.Name, t.PersistentVolumeClaim.Name, framework.Poll, framework.ClaimProvisionTimeout)
	framework.ExpectNoError(err)

	ginkgo.By("checking the PVC")
	// Get new copy of the claim
	t.PersistentVolumeClaim, err = t.Client.CoreV1().PersistentVolumeClaims(t.Namespace.Name).Get(context.TODO(), t.PersistentVolumeClaim.Name, metav1.GetOptions{})
	framework.ExpectNoError(err)

	return *t.PersistentVolumeClaim
}

func (t *TestPersistentVolumeClaim) Cleanup() {
	if t.PersistentVolumeClaim == nil {
		framework.Failf("Cannot clean PVCs up. No reference to PVC found.")
	}
	// Since PV is created after pod creation when the volume binding mode is WaitForFirstConsumer,
	// we need to populate fields such as PVC and PV info in TestPersistentVolumeClaim, and valid it
	if t.StorageClass != nil && *t.StorageClass.VolumeBindingMode == storagev1.VolumeBindingWaitForFirstConsumer {
		var err error
		t.PersistentVolumeClaim, err = t.Client.CoreV1().PersistentVolumeClaims(t.Namespace.Name).Get(context.TODO(), t.PersistentVolumeClaim.Name, metav1.GetOptions{})
		framework.ExpectNoError(err)
		t.ValidateProvisionedPersistentVolume()
	}
	e2elog.Logf("deleting PVC %q/%q", t.Namespace.Name, t.PersistentVolumeClaim.Name)
	err := e2epv.DeletePersistentVolumeClaim(t.Client, t.PersistentVolumeClaim.Name, t.Namespace.Name)
	framework.ExpectNoError(err)
	// Wait for the PV to get deleted if reclaim policy is Delete. (If it's
	// Retain, there's no use waiting because the PV won't be auto-deleted and
	// it's expected for the caller to do it.) Technically, the first few delete
	// attempts may fail, as the volume is still attached to a node because
	// kubelet is slowly cleaning up the previous pod, however it should succeed
	// in a couple of minutes.
	if t.PersistentVolume == nil {
		t.PersistentVolume, err = t.Client.CoreV1().PersistentVolumes().Get(context.Background(), t.PersistentVolumeClaim.Spec.VolumeName, metav1.GetOptions{})
		framework.ExpectNoError(err)
	}
	if t.PersistentVolume.Spec.PersistentVolumeReclaimPolicy == v1.PersistentVolumeReclaimDelete {
		ginkgo.By(fmt.Sprintf("waiting for claim's PV %q to be deleted", t.PersistentVolume.Name))
		err := e2epv.WaitForPersistentVolumeDeleted(t.Client, t.PersistentVolume.Name, 5*time.Second, 10*time.Minute)
		framework.ExpectNoError(err)
	}
	// Wait for the PVC to be deleted
	err = waitForPersistentVolumeClaimDeleted(t.Client, t.Namespace.Name, t.PersistentVolumeClaim.Name, 5*time.Second, 5*time.Minute)
	framework.ExpectNoError(err)
}

func (t *TestPersistentVolumeClaim) ReclaimPolicy() v1.PersistentVolumeReclaimPolicy {
	return t.PersistentVolume.Spec.PersistentVolumeReclaimPolicy
}

func (t *TestPersistentVolumeClaim) WaitForPersistentVolumePhase(phase v1.PersistentVolumePhase) {
	err := e2epv.WaitForPersistentVolumePhase(phase, t.Client, t.PersistentVolume.Name, 5*time.Second, 10*time.Minute)
	framework.ExpectNoError(err)
}

func (t *TestPersistentVolumeClaim) DeleteBoundPersistentVolume() {
	ginkgo.By(fmt.Sprintf("deleting PV %q", t.PersistentVolume.Name))
	err := e2epv.DeletePersistentVolume(t.Client, t.PersistentVolume.Name)
	framework.ExpectNoError(err)
	ginkgo.By(fmt.Sprintf("waiting for claim's PV %q to be deleted", t.PersistentVolume.Name))
	err = e2epv.WaitForPersistentVolumeDeleted(t.Client, t.PersistentVolume.Name, 5*time.Second, 10*time.Minute)
	framework.ExpectNoError(err)
}

func (t *TestPersistentVolumeClaim) DeleteBackingVolume(azureCloud *provider.Cloud) {
	diskURI := t.PersistentVolume.Spec.CSI.VolumeHandle
	ginkgo.By(fmt.Sprintf("deleting azuredisk volume %q", diskURI))
	err := azureCloud.DeleteManagedDisk(context.Background(), diskURI)
	if err != nil {
		ginkgo.Fail(fmt.Sprintf("could not delete volume %q: %v", diskURI, err))
	}
}
