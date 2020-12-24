/*
Copyright 2017 The Kubernetes Authors.

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
// +genclient:noStatus
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AzVolume is a specification for a AzVolume resource
type AzVolume struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AzVolumeSpec   `json:"spec"`
	Status AzVolumeStatus `json:"status"`
}

// AzVolumeSpec is the spec for a AzVolume resource
type AzVolumeSpec struct {
	UnderlyingVolume string `json:"underlyingVolume"`
}

// AzVolumeStatus is the status for a AzVolume resource
type AzVolumeStatus struct {
	UnderlyingVolume int32 `json:"underlyingVolume"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AzVolumeList is a list of AzVolume resources
type AzVolumeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []AzVolume `json:"items"`
}

// +genclient
// +genclient:noStatus
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AzVolumeAttachment is a specification for a AzVolumeAttachment resource
type AzVolumeAttachment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AzVolumeAttachmentSpec   `json:"spec"`
	Status AzVolumeAttachmentStatus `json:"status"`
}

// AzVolumeAttachmentSpec is the spec for a AzVolumeAttachment resource
type AzVolumeAttachmentSpec struct {
	UnderlyingVolume string `json:"underlyingVolume"`
}

// AzVolumeAttachmentStatus is the status for a AzVolumeAttachment resource
type AzVolumeAttachmentStatus struct {
	UnderlyingVolume int32 `json:"underlyingVolume"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AzVolumeAttachmentList is a list of AzVolumeAttachment resources
type AzVolumeAttachmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []AzVolumeAttachment `json:"items"`
}

// +genclient
// +genclient:noStatus
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AzDriverNode is a specification for a AzDriverNode resource
type AzDriverNode struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AzDriverNodeSpec   `json:"spec"`
	Status AzDriverNodeStatus `json:"status"`
}

// AzDriverNodeSpec is the spec for a AzDriverNode resource
type AzDriverNodeSpec struct {
	UnderlyingNode string `json:"underlyingNode"`
}

// AzDriverNodeStatus is the status for a AzDriverNode resource
type AzDriverNodeStatus struct {
	UnderlyingNode int32 `json:"underlyingNode"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AzDriverNodeList is a list of AzDriverNode resources
type AzDriverNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []AzDriverNode `json:"items"`
}
