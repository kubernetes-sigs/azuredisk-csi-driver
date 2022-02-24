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
	diskv1alpha2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha2"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/typed/azuredisk/v1alpha2"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

// should only be used for integration tests
type TestAzVolume struct {
	Azclient             azDiskClientSet.DiskV1alpha2Interface
	Namespace            string
	UnderlyingVolume     string
	MaxMountReplicaCount int
}

// should only be used for integration tests
func SetupTestAzVolume(azclient azDiskClientSet.DiskV1alpha2Interface, namespace string, underlyingVolume string, maxMountReplicaCount int) *TestAzVolume {
	return &TestAzVolume{
		Azclient:             azclient,
		Namespace:            namespace,
		UnderlyingVolume:     underlyingVolume,
		MaxMountReplicaCount: maxMountReplicaCount,
	}
}

// should only be used for integration tests
func NewTestAzVolume(azVolume azDiskClientSet.AzVolumeInterface, underlyingVolumeName string, maxMountReplicaCount int) *diskv1alpha2.AzVolume {
	// Delete leftover azVolumes from previous runs
	if _, err := azVolume.Get(context.Background(), underlyingVolumeName, metav1.GetOptions{}); err == nil {
		err := azVolume.Delete(context.Background(), underlyingVolumeName, metav1.DeleteOptions{})
		framework.ExpectNoError(err)
	}
	newAzVolume, err := azVolume.Create(context.Background(), &diskv1alpha2.AzVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: underlyingVolumeName,
		},
		Spec: diskv1alpha2.AzVolumeSpec{
			VolumeName:           underlyingVolumeName,
			MaxMountReplicaCount: maxMountReplicaCount,
			VolumeCapability: []diskv1alpha2.VolumeCapability{
				{
					AccessType: diskv1alpha2.VolumeCapabilityAccessMount,
					AccessMode: diskv1alpha2.VolumeCapabilityAccessModeSingleNodeWriter,
				},
			},
			CapacityRange: &diskv1alpha2.CapacityRange{
				RequiredBytes: 0,
				LimitBytes:    0,
			},
			AccessibilityRequirements: &diskv1alpha2.TopologyRequirement{},
		},
		Status: diskv1alpha2.AzVolumeStatus{
			State: diskv1alpha2.VolumeOperationPending,
		},
	}, metav1.CreateOptions{})
	framework.ExpectNoError(err)

	return newAzVolume
}

// should only be used for integration tests
func (t *TestAzVolume) Create() *diskv1alpha2.AzVolume {
	// create test az volume
	azVolClient := t.Azclient.AzVolumes(t.Namespace)
	azVolume := NewTestAzVolume(azVolClient, t.UnderlyingVolume, t.MaxMountReplicaCount)

	return azVolume
}

// should only be used for integration tests
//Cleanup after TestAzVolume was created
func (t *TestAzVolume) Cleanup() {
	klog.Info("cleaning up TestAzVolume")
	err := t.Azclient.AzVolumes(t.Namespace).Delete(context.Background(), t.UnderlyingVolume, metav1.DeleteOptions{})
	if !errors.IsNotFound(err) {
		framework.ExpectNoError(err)
	}
	time.Sleep(time.Duration(1) * time.Minute)

}

// should only be used for integration tests
// Wait for the azVolume object update
func (t *TestAzVolume) WaitForFinalizer(timeout time.Duration) error {
	conditionFunc := func() (bool, error) {
		azVolume, err := t.Azclient.AzVolumes(t.Namespace).Get(context.TODO(), t.UnderlyingVolume, metav1.GetOptions{})
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

// should only be used for integration tests
// Wait for the azVolume object update
func (t *TestAzVolume) WaitForDelete(timeout time.Duration) error {
	klog.Infof("Waiting for delete azVolume object")
	conditionFunc := func() (bool, error) {
		_, err := t.Azclient.AzVolumes(t.Namespace).Get(context.TODO(), t.UnderlyingVolume, metav1.GetOptions{})
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
