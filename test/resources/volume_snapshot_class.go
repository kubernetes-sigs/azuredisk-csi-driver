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

	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v4/apis/volumesnapshot/v1"
	snapshotclientset "github.com/kubernetes-csi/external-snapshotter/client/v4/clientset/versioned"
	"github.com/onsi/ginkgo/v2"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	restclientset "k8s.io/client-go/rest"
	"k8s.io/kubernetes/test/e2e/framework"

	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
)

type TestVolumeSnapshotClass struct {
	Client              restclientset.Interface
	VolumeSnapshotClass *snapshotv1.VolumeSnapshotClass
	Namespace           *v1.Namespace
}

func NewTestVolumeSnapshotClass(c restclientset.Interface, ns *v1.Namespace, vsc *snapshotv1.VolumeSnapshotClass) *TestVolumeSnapshotClass {
	return &TestVolumeSnapshotClass{
		Client:              c,
		VolumeSnapshotClass: vsc,
		Namespace:           ns,
	}
}

func (t *TestVolumeSnapshotClass) Create() {
	ginkgo.By("creating a VolumeSnapshotClass")
	var err error
	t.VolumeSnapshotClass, err = snapshotclientset.New(t.Client).SnapshotV1().VolumeSnapshotClasses().Create(context.TODO(), t.VolumeSnapshotClass, metav1.CreateOptions{})
	framework.ExpectNoError(err)
}

func (t *TestVolumeSnapshotClass) CreateSnapshot(pvc *v1.PersistentVolumeClaim) *snapshotv1.VolumeSnapshot {
	ginkgo.By("creating a VolumeSnapshot for " + pvc.Name)
	snapshot := &snapshotv1.VolumeSnapshot{
		TypeMeta: metav1.TypeMeta{
			Kind:       testconsts.VolumeSnapshotKind,
			APIVersion: testconsts.SnapshotAPIVersion,
		},
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "volume-snapshot-",
			Namespace:    t.Namespace.Name,
		},
		Spec: snapshotv1.VolumeSnapshotSpec{
			VolumeSnapshotClassName: &t.VolumeSnapshotClass.Name,
			Source: snapshotv1.VolumeSnapshotSource{
				PersistentVolumeClaimName: &pvc.Name,
			},
		},
	}
	snapshot, err := snapshotclientset.New(t.Client).SnapshotV1().VolumeSnapshots(t.Namespace.Name).Create(context.TODO(), snapshot, metav1.CreateOptions{})
	framework.ExpectNoError(err)
	return snapshot
}

func (t *TestVolumeSnapshotClass) ReadyToUse(snapshot *snapshotv1.VolumeSnapshot) {
	ginkgo.By("waiting for VolumeSnapshot to be ready to use - " + snapshot.Name)
	err := wait.Poll(15*time.Second, 5*time.Minute, func() (bool, error) {
		vs, err := snapshotclientset.New(t.Client).SnapshotV1().VolumeSnapshots(t.Namespace.Name).Get(context.TODO(), snapshot.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Errorf("did not see ReadyToUse: %v", err)
		}
		return *vs.Status.ReadyToUse, nil
	})
	framework.ExpectNoError(err)
}

func (t *TestVolumeSnapshotClass) DeleteSnapshot(vs *snapshotv1.VolumeSnapshot) {
	ginkgo.By("deleting a VolumeSnapshot " + vs.Name)
	err := snapshotclientset.New(t.Client).SnapshotV1().VolumeSnapshots(t.Namespace.Name).Delete(context.TODO(), vs.Name, metav1.DeleteOptions{})
	framework.ExpectNoError(err)
}

func (t *TestVolumeSnapshotClass) Cleanup() {
	// skip deleting volume snapshot storage class otherwise snapshot e2e test will fail, details:
	// https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/260#issuecomment-583296932
	framework.Logf("skip deleting VolumeSnapshotClass %s", t.VolumeSnapshotClass.Name)
	//err := snapshotclientset.New(t.Client).SnapshotV1().VolumeSnapshotClasses().Delete(t.VolumeSnapshotClass.Name, nil)
	//framework.ExpectNoError(err)
}
