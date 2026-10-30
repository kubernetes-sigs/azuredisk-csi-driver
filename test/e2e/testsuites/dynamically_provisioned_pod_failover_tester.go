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

package testsuites

import (
	"context"
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	scheduling "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/framework/deployment"
	e2enode "k8s.io/kubernetes/test/e2e/framework/node"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
)

// Will provision required PV(s), PVC(s) and Pod(s)
//
// A single-replica deployment must relocate to another node after cordoning its
// current node and deleting its pod, preserving the volume's data.
type PodFailover struct {
	CSIDriver              driver.DynamicPVTestDriver
	Pod                    PodDetails
	Volume                 VolumeDetails
	PodCheck               *PodExecCheck
	StorageClassParameters map[string]string
}

func (t *PodFailover) Run(ctx context.Context, client clientset.Interface, namespace *v1.Namespace) {
	tDeployment, cleanup := t.Pod.SetupDeployment(ctx, client, namespace, t.CSIDriver, t.StorageClassParameters)

	// defer must be called here so resources don't get removed before using them
	for i := range cleanup {
		defer cleanup[i](ctx)
	}

	gomega.Expect(*tDeployment.deployment.Spec.Replicas).To(gomega.Equal(int32(1)), "pod failover requires a single replica")

	ginkgo.By("deploying the deployment")
	tDeployment.Create(ctx)

	ginkgo.By("checking that the pod is running")
	tDeployment.WaitForPodReady(ctx)

	if t.PodCheck != nil {
		ginkgo.By("check pod exec")
		tDeployment.PollForStringInPodsExec(t.PodCheck.Cmd, t.PodCheck.ExpectedString)
	}

	gomega.Expect(tDeployment.podNames).To(gomega.HaveLen(1))
	originalPod, err := client.CoreV1().Pods(namespace.Name).Get(ctx, tDeployment.podNames[0], metav1.GetOptions{})
	framework.ExpectNoError(err)
	gomega.Expect(originalPod.Spec.NodeName).NotTo(gomega.BeEmpty())

	volumes := []*v1.PersistentVolume{}
	for _, volume := range originalPod.Spec.Volumes {
		if volume.PersistentVolumeClaim == nil {
			continue
		}
		claim, err := client.CoreV1().PersistentVolumeClaims(namespace.Name).Get(ctx, volume.PersistentVolumeClaim.ClaimName, metav1.GetOptions{})
		framework.ExpectNoError(err)
		gomega.Expect(claim.Spec.VolumeName).NotTo(gomega.BeEmpty())
		pv, err := client.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
		framework.ExpectNoError(err)
		volumes = append(volumes, pv)
	}

	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	framework.ExpectNoError(err)
	hasDestination := false
	for i := range nodes.Items {
		matches, err := canHostFailoverPod(originalPod, &nodes.Items[i], volumes)
		framework.ExpectNoError(err)
		if matches {
			hasDestination = true
			break
		}
	}
	if !hasDestination {
		ginkgo.Skip(fmt.Sprintf("no other ready, schedulable node matches pod placement, taints, and PV topology for pod %s on node %s", originalPod.Name, originalPod.Spec.NodeName))
	}

	sourceNode, err := client.CoreV1().Nodes().Get(ctx, originalPod.Spec.NodeName, metav1.GetOptions{})
	framework.ExpectNoError(err)
	ginkgo.By(fmt.Sprintf("cordoning pod's node %s", sourceNode.Name))

	testPod := TestPod{
		client: client,
	}

	defer testPod.SetNodeUnschedulable(context.WithoutCancel(ctx), sourceNode.Name, sourceNode.Spec.Unschedulable)
	testPod.SetNodeUnschedulable(ctx, sourceNode.Name, true)

	ginkgo.By("deleting the pod for deployment")
	tDeployment.DeletePodAndWait(ctx)

	ginkgo.By("checking that a ready replacement pod is running on a different node")
	gomega.Eventually(ctx, func() error {
		pods, err := deployment.GetPodsForDeployment(ctx, client, tDeployment.deployment)
		if err != nil {
			return err
		}
		if len(pods.Items) != 1 {
			return fmt.Errorf("expected one replacement pod, got %d", len(pods.Items))
		}
		if err := checkFailoverReplacement(originalPod, &pods.Items[0]); err != nil {
			return err
		}
		tDeployment.podNames = []string{pods.Items[0].Name}
		return nil
	}, pollLongTimeout, poll).Should(gomega.Succeed())

	if t.PodCheck != nil {
		ginkgo.By("check pod exec")
		// pod will be restarted so expect to see 2 instances of string
		tDeployment.PollForStringInPodsExec(t.PodCheck.Cmd, t.PodCheck.ExpectedString+t.PodCheck.ExpectedString)
	}
}

// This preflight checks placement constraints, not dynamic scheduler resources.
// The replacement must still become ready to prove that relocation succeeded.
func canHostFailoverPod(pod *v1.Pod, node *v1.Node, volumes []*v1.PersistentVolume) (bool, error) {
	if node.Name == pod.Spec.NodeName || !e2enode.IsNodeSchedulable(klog.Background(), node) {
		return false, nil
	}
	matches, err := nodeaffinity.GetRequiredNodeAffinity(pod).Match(node)
	if err != nil || !matches {
		return false, err
	}
	_, untolerated := scheduling.FindMatchingUntoleratedTaint(klog.Background(), node.Spec.Taints, pod.Spec.Tolerations, func(taint *v1.Taint) bool {
		return taint.Effect == v1.TaintEffectNoSchedule || taint.Effect == v1.TaintEffectNoExecute
	}, false)
	if untolerated {
		return false, nil
	}
	for _, pv := range volumes {
		if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
			continue
		}
		matches, err := scheduling.MatchNodeSelectorTerms(node, pv.Spec.NodeAffinity.Required)
		if err != nil || !matches {
			return false, err
		}
	}
	return true, nil
}

func checkFailoverReplacement(original, replacement *v1.Pod) error {
	if replacement.UID == original.UID || replacement.DeletionTimestamp != nil {
		return fmt.Errorf("pod %s is not a live replacement", replacement.Name)
	}
	if replacement.Spec.NodeName == "" || replacement.Spec.NodeName == original.Spec.NodeName {
		return fmt.Errorf("replacement pod %s has not moved away from node %s (current node %q)", replacement.Name, original.Spec.NodeName, replacement.Spec.NodeName)
	}
	if replacement.Status.Phase == v1.PodRunning {
		for _, condition := range replacement.Status.Conditions {
			if condition.Type == v1.PodReady && condition.Status == v1.ConditionTrue {
				return nil
			}
		}
	}
	return fmt.Errorf("replacement pod %s on node %s is not running and ready", replacement.Name, replacement.Spec.NodeName)
}
