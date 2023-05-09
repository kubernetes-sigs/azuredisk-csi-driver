/*
Copyright 2022 The Kubernetes Authors.

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
	"strconv"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
)

type PreProvisionedSharedDiskTester struct {
	CSIDriver     driver.PreProvisionedVolumeTestDriver
	Pod           PodDetails
	PodCheck      *PodExecCheck
	VolumeContext map[string]string
}

func (t *PreProvisionedSharedDiskTester) Run(ctx context.Context, client clientset.Interface, namespace *v1.Namespace) {
	tDeployment, cleanup := t.Pod.SetupDeploymentWithPreProvisionedVolumes(ctx, client, namespace, t.CSIDriver, t.VolumeContext)
	for i := range cleanup {
		defer cleanup[i](ctx)
	}

	maxSharesStr, ok := t.VolumeContext[consts.MaxSharesField]
	gomega.Expect(ok).To(gomega.BeTrue(), "test case must specify maxshares parameter")
	maxShares, err := strconv.Atoi(maxSharesStr)
	framework.ExpectNoError(err)
	gomega.Expect(maxShares).To(gomega.BeNumerically(">=", 2), "test case must specify maxshares of at least 2")

	expectedAttachmentCount := int32(1)
	if tDeployment.deployment.Spec.Replicas != nil {
		expectedAttachmentCount = *tDeployment.deployment.Spec.Replicas
	}
	gomega.Expect(expectedAttachmentCount).To(gomega.BeNumerically("<=", int32(maxShares)), "test case must specify a number of replica <= maxshares")

	ginkgo.By("deploying the deployment")
	tDeployment.Create(ctx)

	ginkgo.By("checking that the pod is running")
	tDeployment.WaitForPodReady(ctx)

	if t.PodCheck != nil {
		ginkgo.By("check pod exec")
		tDeployment.PollForStringInPodsExec(t.PodCheck.Cmd, t.PodCheck.ExpectedString)
	}

	ginkgo.By("verifying shared attachment")

	for _, volSpec := range tDeployment.deployment.Spec.Template.Spec.Volumes {
		attachedToNodes := make(map[string]struct{})

		volumeAttachments, err := client.StorageV1().VolumeAttachments().List(ctx, metav1.ListOptions{})
		framework.ExpectNoError(err)

		for _, volumeAttachment := range volumeAttachments.Items {
			if volumeAttachment.Spec.Source.PersistentVolumeName != nil {
				pvc, err := client.CoreV1().PersistentVolumeClaims(namespace.Name).Get(ctx, volSpec.PersistentVolumeClaim.ClaimName, metav1.GetOptions{})
				framework.ExpectNoError(err)

				if strings.EqualFold(pvc.Spec.VolumeName, *volumeAttachment.Spec.Source.PersistentVolumeName) {
					attachedToNodes[strings.ToLower(volumeAttachment.Spec.NodeName)] = struct{}{}
				}
			}
		}

		attachedNodeCount := int32(len(attachedToNodes))
		gomega.Expect(attachedNodeCount).To(gomega.Equal(expectedAttachmentCount),
			"volume %s is attached to %d nodes when %d nodes were expected, AttachedNodes: %v",
			volSpec.PersistentVolumeClaim.ClaimName,
			attachedNodeCount,
			expectedAttachmentCount,
			attachedToNodes)
	}
}
