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

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// PodFailure is a specification for a PodFailure resource
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="FailureType",type="string",JSONPath=".status.failureType",description="The facilitated failure type"
// +kubebuilder:printcolumn:name="HeartBeat",type="string",JSONPath=".status.heartbeat",description="Last heartbeat of pod"
type PodFailure struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	//+required
	Spec PodFailureSpec `json:"spec"`
	// +required
	Status PodFailureStatus `json:"status"`
}

type PodFailureSpec struct {
	// +required
	PodName string `json:"podName"`
}

type PodFailureStatus struct {
	// +optional
	FailureType string `json:"failureType,omitempty"`
	// +optional
	HeartBeat string `json:"heartbeat,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type PodFailureList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []PodFailure `json:"items"`
}
