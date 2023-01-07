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
	"os"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/framework/kubectl"

	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
	nodeutils "sigs.k8s.io/azuredisk-csi-driver/test/utils/node"
)

// Normalize volumes by adding allowed topology values and WaitForFirstConsumer binding mode if we are testing in a multi-az cluster
func NormalizeVolumes(volumes []VolumeDetails, allowedTopologies []string, isMultiZone bool) []VolumeDetails {
	for i := range volumes {
		volumes[i] = normalizeVolume(volumes[i], allowedTopologies, isMultiZone)
	}

	return volumes
}

func VerifySuccessfulAzVolumeAttachments(pod PodDetails, azDiskClient *azdisk.Clientset, storageClassParameters map[string]string, client clientset.Interface, namespace *v1.Namespace) (isVerified bool, allAttachments, unattachedAttachments []azdiskv1beta2.AzVolumeAttachment, err error) {
	var expectedNumberOfReplicas int
	nodes := getSchedulableNodes(azDiskClient, client, pod, namespace)
	nodesAvailableForReplicas := len(nodes) - 1

	for _, volume := range pod.Volumes {
		pv := volume.PersistentVolume
		if pv == nil {
			if pv = getPVFromPVC(client, volume.PersistentVolumeClaim); pv == nil {
				continue
			}
		}
		_, maxMountReplicas := azureutils.GetMaxSharesAndMaxMountReplicaCount(storageClassParameters, volume.VolumeMode == Block)
		if nodesAvailableForReplicas >= maxMountReplicas {
			expectedNumberOfReplicas = maxMountReplicas
		} else {
			expectedNumberOfReplicas = nodesAvailableForReplicas
		}

		pvAttachments, derr := getAzVolumeAttachmentsForPV(pv, client, namespace, azDiskClient)
		framework.ExpectNoError(derr)
		numAttachments := len(pvAttachments.Items)

		if numAttachments != expectedNumberOfReplicas+1 {
			framework.Logf("expected %d attachments, found %d", expectedNumberOfReplicas+1, numAttachments)
			return
		}
		allAttachments = append(allAttachments, pvAttachments.Items...)

		for _, attachment := range pvAttachments.Items {
			if attachment.Status.State != azdiskv1beta2.Attached {
				framework.Logf("found attachment %s, currently not attached", attachment.Name)
				unattachedAttachments = append(unattachedAttachments, attachment)
			} else {
				framework.Logf("found attachment %s in attached state", attachment.Name)
			}
		}
		if len(unattachedAttachments) > 0 {
			return
		}
	}
	isVerified = true
	return
}

func pollForStringWorker(namespace string, pod string, command []string, expectedString string, ch chan<- error) {
	args := append([]string{"exec", pod, "--"}, command...)
	err := wait.PollImmediate(testconsts.Poll, testconsts.PollForStringTimeout, func() (bool, error) {
		stdout, err := kubectl.RunKubectl(namespace, args...)
		if err != nil {
			framework.Logf("Error waiting for output %q in pod %q: %v.", expectedString, pod, err)
			return false, nil
		}
		if !strings.Contains(stdout, expectedString) {
			framework.Logf("The stdout did not contain output %q in pod %q, found: %q.", expectedString, pod, stdout)
			return false, nil
		}
		return true, nil
	})
	ch <- err
}

// Execute the command for all pods in the namespace, looking for expectedString in stdout
func pollForStringInPodsExec(namespace string, pods []string, command []string, expectedString string) {
	ch := make(chan error, len(pods))
	for _, pod := range pods {
		go pollForStringWorker(namespace, pod, command, expectedString, ch)
	}
	errs := make([]error, 0, len(pods))
	for range pods {
		errs = append(errs, <-ch)
	}
	framework.ExpectNoError(utilerrors.NewAggregate(errs), "Failed to find %q in at least one pod's output.", expectedString)
}

func generatePVC(namespace, storageClassName, name, claimSize string, volumeMode v1.PersistentVolumeMode, accessMode v1.PersistentVolumeAccessMode, dataSource *v1.TypedLocalObjectReference) *v1.PersistentVolumeClaim {
	if accessMode != v1.ReadWriteOnce && accessMode != v1.ReadOnlyMany && accessMode != v1.ReadWriteMany {
		accessMode = v1.ReadWriteOnce
	}

	pvcMeta := metav1.ObjectMeta{
		Namespace: namespace,
	}
	if name == "" {
		pvcMeta.GenerateName = "pvc-"
	} else {
		pvcMeta.Name = name
	}

	return &v1.PersistentVolumeClaim{
		ObjectMeta: pvcMeta,
		Spec: v1.PersistentVolumeClaimSpec{
			StorageClassName: &storageClassName,
			AccessModes: []v1.PersistentVolumeAccessMode{
				accessMode,
			},
			Resources: v1.ResourceRequirements{
				Requests: v1.ResourceList{
					v1.ResourceName(v1.ResourceStorage): resource.MustParse(claimSize),
				},
			},
			VolumeMode: &volumeMode,
			DataSource: dataSource,
		},
	}
}

func getAzVolumeAttachmentsForPV(persistentVolume *v1.PersistentVolume, client clientset.Interface, namespace *v1.Namespace, azDiskClient *azdisk.Clientset) (*azdiskv1beta2.AzVolumeAttachmentList, error) {
	pv, err := client.CoreV1().PersistentVolumes().Get(context.TODO(), persistentVolume.Name, metav1.GetOptions{})
	if err != nil {
		ginkgo.Fail("failed to get persistent volume")
	}
	diskname, err := azureutils.GetDiskName(pv.Spec.CSI.VolumeHandle)
	if err != nil {
		ginkgo.Fail("failed to get persistent volume diskname")
	}
	azVolumeAttachments, err := azDiskClient.DiskV1beta2().AzVolumeAttachments(consts.DefaultAzureDiskCrdNamespace).List(context.Background(), metav1.ListOptions{LabelSelector: labels.Set(map[string]string{consts.VolumeNameLabel: diskname}).String()})
	if err != nil {
		ginkgo.Fail("failed while getting replica attachments")
	}
	return azVolumeAttachments, nil
}

func getSchedulableNodes(azDiskClient *azdisk.Clientset, client clientset.Interface, pod PodDetails, namespace *v1.Namespace) []*v1.Node {
	nodes := nodeutils.ListAzDriverNodeNames(azDiskClient)
	var availableNodes []*v1.Node
	schedulableNodes, err := client.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{FieldSelector: fields.Set{
		"spec.unschedulable": "false",
	}.AsSelector().String()})

	if err != nil {
		ginkgo.Fail("failed while getting schedulable nodes list")
	}

	podObj, err := client.CoreV1().Pods(namespace.Name).Get(context.TODO(), pod.Name, metav1.GetOptions{})
	if err != nil {
		ginkgo.Fail("failed while getting pod")
	}

	for _, nodeName := range nodes {
		for i, schedulableNode := range schedulableNodes.Items {
			if nodeName == schedulableNode.Name {
				//Check if node has any taints making it unschedulable

				nodeDetails, err := client.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
				framework.ExpectNoError(err)

				tolerable := true
				for _, taint := range nodeDetails.Spec.Taints {
					for _, podToleration := range podObj.Spec.Tolerations {
						if !podToleration.ToleratesTaint(&taint) {
							tolerable = false
						}
					}
				}
				if tolerable {
					availableNodes = append(availableNodes, &schedulableNodes.Items[i])
				}
			}
		}
	}

	return availableNodes
}

func normalizeVolume(volume VolumeDetails, allowedTopologies []string, isMultiZone bool) VolumeDetails {
	driverName := os.Getenv(testconsts.AzureDriverNameVar)
	switch driverName {
	case "kubernetes.io/azure-disk":
		volumeBindingMode := storagev1.VolumeBindingWaitForFirstConsumer
		volume.VolumeBindingMode = &volumeBindingMode
	case "", consts.DefaultDriverName:
		if !isMultiZone {
			return volume
		}
		volume.AllowedTopologyValues = allowedTopologies
		volumeBindingMode := storagev1.VolumeBindingWaitForFirstConsumer
		volume.VolumeBindingMode = &volumeBindingMode
	}

	return volume
}

// waitForPersistentVolumeClaimDeleted waits for a PersistentVolumeClaim to be removed from the system until timeout occurs, whichever comes first.
func waitForPersistentVolumeClaimDeleted(c clientset.Interface, ns string, pvcName string, Poll, timeout time.Duration) error {
	framework.Logf("Waiting up to %v for PersistentVolumeClaim %s to be removed", timeout, pvcName)
	err := wait.PollImmediate(testconsts.Poll, testconsts.PollTimeout, func() (bool, error) {
		var err error
		_, err = c.CoreV1().PersistentVolumeClaims(ns).Get(context.TODO(), pvcName, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				framework.Logf("Claim %q in namespace %q doesn't exist in the system", pvcName, ns)
				return true, nil
			}
			framework.Logf("Failed to get claim %q in namespace %q, retrying in %v. Error: %v", pvcName, ns, Poll, err)
			return false, err
		}
		return false, nil
	})

	return err
}

// Ideally this would be in "k8s.io/kubernetes/test/e2e/framework"
// Similar to framework.WaitForPodSuccessInNamespaceSlow
var podFailedCondition = func(pod *v1.Pod) (bool, error) {
	switch pod.Status.Phase {
	case v1.PodFailed:
		ginkgo.By("Saw pod failure")
		return true, nil
	case v1.PodSucceeded:
		return true, fmt.Errorf("pod %q succeeded with reason: %q, message: %q", pod.Name, pod.Status.Reason, pod.Status.Message)
	default:
		return false, nil
	}
}

func getPVFromPVC(client clientset.Interface, pvc *v1.PersistentVolumeClaim) (pv *v1.PersistentVolume) {
	if pvc == nil {
		return nil
	}
	var err error
	ctx := context.Background()
	pvc, err = client.CoreV1().PersistentVolumeClaims(pvc.Namespace).Get(ctx, pvc.Name, metav1.GetOptions{})
	if err != nil {
		klog.Error(err)
	}
	pv, err = client.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
	if err != nil {
		klog.Error(err)
	}
	return
}

func getWinImageTag(winServerVer string) string {
	testWinImageTag := "ltsc2019"
	if testconsts.WinServerVer == "windows-2022" {
		testWinImageTag = "ltsc2022"
	}
	return testWinImageTag
}
