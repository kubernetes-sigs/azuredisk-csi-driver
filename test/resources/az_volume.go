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
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

type TestAzVolume struct {
	Azclient             azdisk.Interface
	Namespace            string
	UnderlyingVolume     string
	MaxMountReplicaCount int
}

func SetupTestAzVolume(azclient azdisk.Interface, namespace, underlyingVolume string, maxMountReplicaCount int) *TestAzVolume {
	return &TestAzVolume{
		Azclient:             azclient,
		Namespace:            namespace,
		UnderlyingVolume:     underlyingVolume,
		MaxMountReplicaCount: maxMountReplicaCount,
	}
}

func NewTestAzVolume(azclient azdisk.Interface, namespace, underlyingVolumeName string, maxMountReplicaCount int) *azdiskv1beta2.AzVolume {
	// Delete leftover azVolumes from previous runs
	azVolumes := azclient.DiskV1beta2().AzVolumes(namespace)
	if _, err := azVolumes.Get(context.Background(), underlyingVolumeName, metav1.GetOptions{}); err == nil {
		err := azVolumes.Delete(context.Background(), underlyingVolumeName, metav1.DeleteOptions{})
		framework.ExpectNoError(err)
	}
	newAzVolume, err := azVolumes.Create(context.Background(), &azdiskv1beta2.AzVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: underlyingVolumeName,
		},
		Spec: azdiskv1beta2.AzVolumeSpec{
			VolumeName:           underlyingVolumeName,
			MaxMountReplicaCount: maxMountReplicaCount,
			VolumeCapability: []azdiskv1beta2.VolumeCapability{
				{
					AccessType: azdiskv1beta2.VolumeCapabilityAccessMount,
					AccessMode: azdiskv1beta2.VolumeCapabilityAccessModeSingleNodeWriter,
				},
			},
			CapacityRange: &azdiskv1beta2.CapacityRange{
				RequiredBytes: 0,
				LimitBytes:    0,
			},
			AccessibilityRequirements: &azdiskv1beta2.TopologyRequirement{},
		},
		Status: azdiskv1beta2.AzVolumeStatus{
			State: azdiskv1beta2.VolumeOperationPending,
		},
	}, metav1.CreateOptions{})
	framework.ExpectNoError(err)

	return newAzVolume
}

func (t *TestAzVolume) Create() *azdiskv1beta2.AzVolume {
	// create test az volume
	azVolume := NewTestAzVolume(t.Azclient, t.Namespace, t.UnderlyingVolume, t.MaxMountReplicaCount)

	return azVolume
}

func (t *TestAzVolume) Cleanup() {
	klog.Info("cleaning up TestAzVolume")
	err := t.Azclient.DiskV1beta2().AzVolumes(t.Namespace).Delete(context.Background(), t.UnderlyingVolume, metav1.DeleteOptions{})
	if !errors.IsNotFound(err) {
		framework.ExpectNoError(err)
	}
	time.Sleep(time.Duration(1) * time.Minute)

}

func (t *TestAzVolume) WaitForFinalizer(timeout time.Duration) error {
	conditionFunc := func() (bool, error) {
		azVolume, err := t.Azclient.DiskV1beta2().AzVolumes(t.Namespace).Get(context.TODO(), t.UnderlyingVolume, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if azVolume.ObjectMeta.Finalizers == nil {
			return false, nil
		}
		for _, finalizer := range azVolume.ObjectMeta.Finalizers {
			if finalizer == consts.AzVolumeFinalizer {
				klog.Infof("finalizer (%s) found on AzVolume object (%s)", consts.AzVolumeFinalizer, azVolume.Name)
				return true, nil
			}
		}
		return false, nil
	}
	return wait.PollImmediate(time.Duration(15)*time.Second, timeout, conditionFunc)
}

func (t *TestAzVolume) WaitForDelete(timeout time.Duration) error {
	klog.Infof("Waiting for delete azVolume object")
	conditionFunc := func() (bool, error) {
		_, err := t.Azclient.DiskV1beta2().AzVolumes(t.Namespace).Get(context.TODO(), t.UnderlyingVolume, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			klog.Infof("azVolume %s deleted.", t.UnderlyingVolume)
			return true, nil
		} else if err != nil {
			return false, err
		}
		return false, nil
	}
	return wait.PollImmediate(time.Duration(15)*time.Second, timeout, conditionFunc)
}
