/*
Copyright 2026 The Kubernetes Authors.

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
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCanHostFailoverPod(t *testing.T) {
	selector := func(zone string) *v1.NodeSelector {
		return &v1.NodeSelector{NodeSelectorTerms: []v1.NodeSelectorTerm{{
			MatchExpressions: []v1.NodeSelectorRequirement{{
				Key: "topology.disk.csi.azure.com/zone", Operator: v1.NodeSelectorOpIn, Values: []string{zone},
			}},
		}}}
	}
	for _, tc := range []struct {
		name   string
		change func(*v1.Pod, *v1.Node, []*v1.PersistentVolume)
		want   bool
	}{
		{name: "eligible destination", want: true},
		{name: "original node is not first in list", change: func(p *v1.Pod, n *v1.Node, _ []*v1.PersistentVolume) { n.Name = p.Spec.NodeName }},
		{name: "cordoned", change: func(_ *v1.Pod, n *v1.Node, _ []*v1.PersistentVolume) { n.Spec.Unschedulable = true }},
		{name: "not ready", change: func(_ *v1.Pod, n *v1.Node, _ []*v1.PersistentVolume) { n.Status.Conditions = nil }},
		{name: "wrong OS", change: func(_ *v1.Pod, n *v1.Node, _ []*v1.PersistentVolume) { n.Labels[v1.LabelOSStable] = "windows" }},
		{name: "untolerated NoSchedule", change: func(_ *v1.Pod, n *v1.Node, _ []*v1.PersistentVolume) {
			n.Spec.Taints = []v1.Taint{{Key: "dedicated", Effect: v1.TaintEffectNoSchedule}}
		}},
		{name: "untolerated NoExecute", change: func(_ *v1.Pod, n *v1.Node, _ []*v1.PersistentVolume) {
			n.Spec.Taints = []v1.Taint{{Key: "dedicated", Effect: v1.TaintEffectNoExecute}}
		}},
		{name: "tolerated taint", want: true, change: func(p *v1.Pod, n *v1.Node, _ []*v1.PersistentVolume) {
			n.Spec.Taints = []v1.Taint{{Key: "dedicated", Effect: v1.TaintEffectNoSchedule}}
			p.Spec.Tolerations = []v1.Toleration{{Key: "dedicated", Operator: v1.TolerationOpExists}}
		}},
		{name: "soft taint", want: true, change: func(_ *v1.Pod, n *v1.Node, _ []*v1.PersistentVolume) {
			n.Spec.Taints = []v1.Taint{{Key: "dedicated", Effect: v1.TaintEffectPreferNoSchedule}}
		}},
		{name: "wrong disk zone", change: func(_ *v1.Pod, n *v1.Node, _ []*v1.PersistentVolume) {
			n.Labels["topology.disk.csi.azure.com/zone"] = "zone-2"
		}},
		{name: "ZRS without PV affinity", want: true, change: func(_ *v1.Pod, n *v1.Node, volumes []*v1.PersistentVolume) {
			n.Labels["topology.disk.csi.azure.com/zone"] = "zone-2"
			volumes[0].Spec.NodeAffinity = nil
		}},
		{name: "required pod affinity", change: func(p *v1.Pod, _ *v1.Node, _ []*v1.PersistentVolume) {
			p.Spec.Affinity = &v1.Affinity{NodeAffinity: &v1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: selector("zone-2")}}
		}},
		{name: "all PVs must match", change: func(_ *v1.Pod, _ *v1.Node, volumes []*v1.PersistentVolume) {
			volumes[1].Spec.NodeAffinity = &v1.VolumeNodeAffinity{Required: selector("zone-2")}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pod := &v1.Pod{Spec: v1.PodSpec{NodeName: "source", NodeSelector: map[string]string{v1.LabelOSStable: "linux"}}}
			node := &v1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "destination", Labels: map[string]string{v1.LabelOSStable: "linux", "topology.disk.csi.azure.com/zone": "zone-1"}},
				Status:     v1.NodeStatus{Conditions: []v1.NodeCondition{{Type: v1.NodeReady, Status: v1.ConditionTrue}}},
			}
			volumes := []*v1.PersistentVolume{
				{Spec: v1.PersistentVolumeSpec{NodeAffinity: &v1.VolumeNodeAffinity{Required: selector("zone-1")}}},
				{},
			}
			if tc.change != nil {
				tc.change(pod, node, volumes)
			}
			got, err := canHostFailoverPod(pod, node, volumes)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestCheckFailoverReplacement(t *testing.T) {
	original := &v1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "original"}, Spec: v1.PodSpec{NodeName: "source"}}
	for _, tc := range []struct {
		name   string
		change func(*v1.Pod)
		wantOK bool
	}{
		{name: "ready on another node", wantOK: true},
		{name: "same pod", change: func(p *v1.Pod) { p.UID = original.UID }},
		{name: "same node", change: func(p *v1.Pod) { p.Spec.NodeName = original.Spec.NodeName }},
		{name: "unscheduled", change: func(p *v1.Pod) { p.Spec.NodeName = "" }},
		{name: "terminating", change: func(p *v1.Pod) { now := metav1.Now(); p.DeletionTimestamp = &now }},
		{name: "pending", change: func(p *v1.Pod) { p.Status.Phase = v1.PodPending }},
		{name: "not ready", change: func(p *v1.Pod) { p.Status.Conditions[0].Status = v1.ConditionFalse }},
		{name: "no ready condition", change: func(p *v1.Pod) { p.Status.Conditions = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			replacement := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "replacement", UID: "replacement"},
				Spec:       v1.PodSpec{NodeName: "destination"},
				Status: v1.PodStatus{
					Phase:      v1.PodRunning,
					Conditions: []v1.PodCondition{{Type: v1.PodReady, Status: v1.ConditionTrue}},
				},
			}
			if tc.change != nil {
				tc.change(replacement)
			}
			err := checkFailoverReplacement(original, replacement)
			if tc.wantOK {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
