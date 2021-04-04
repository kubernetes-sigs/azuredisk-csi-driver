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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AzVolume is a specification for a AzVolume resource
type AzVolume struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	
	// spec defines the desired state of an AzVolume.
	// Required.
	Spec   AzVolumeSpec   `json:"spec"`
	// status represents the current state of AzVolume.
	// Nil status indicates that the underlying volume has not yet been provisioned
	// +optional
	Status AzVolumeStatus `json:"status,omitempty"`
}

// AzVolumeSpec is the spec for a AzVolume resource
type AzVolumeSpec struct {

	UnderlyingVolume     string `json:"underlyingVolume"`
	
	MaxMountReplicaCount int 	`json:"maxMountReplicaCount"`
	//The suggested name for the storage space
	Name                 string `json:"name"`
	//The capabilities that the volume MUST have
	VolumeCapability     *VolumeCapability `json:"volumeCapability"`
	//The capacity of the storage
	CapacityRange        *CapacityRange `json:"capacityRange"`
	//Parameters for the volume
	//+optional
	Parameters           map[string]string `json:"parameters,omitempty"`
}

// AzVolumeStatus is the status for a AzVolume resource
type AzVolumeStatus struct {
	//Current state of the AzVolume
	//+optional
	State                string `json:"state,omitempty"`
	//Current volume ID for the underlying volume
	//+optional
	UnderlyingVolume     string `json:"underlyingVolume"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AzVolumeList is a list of AzVolume resources
type AzVolumeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []AzVolume `json:"items"`
}

// +genclient
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
	NodeName         string `json:"NodeName"`
	Partition        int32  `json:"partition"`
}

// AzVolumeAttachmentStatus is the status for a AzVolumeAttachment resource
type AzVolumeAttachmentStatus struct {
	State string `json:"state,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AzVolumeAttachmentList is a list of AzVolumeAttachment resources
type AzVolumeAttachmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []AzVolumeAttachment `json:"items"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AzDriverNode is a representation of a node, where azure CSI driver node plug-in runs.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="NodeName",type=string,JSONPath=`.spec.nodeName`,description="Name of the Node which this AzDriverNode object represents."
// +kubebuilder:printcolumn:name="ReadyForVolumeAllocation",type=boolean,JSONPath=`.status.readyForVolumeAllocation`,description="Indicates if the azure persistent volume driver is ready for new pods which use azure persistent volumes."
// +kubebuilder:printcolumn:name="LastHeartbeatTime",type=integer,JSONPath=`.status.lastHeartbeatTime`,description="Represents the time stamp at which azure persistent volume driver sent a heatbeat."
// +kubebuilder:printcolumn:name="StatusMessage",type=string,JSONPath=`.status.statusMessage`,description="A brief node status message."
type AzDriverNode struct {
	metav1.TypeMeta `json:",inline"`
	// Standard object's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/api-conventions.md#metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty" protobuf:"bytes,1,opt,name=metadata"`

	// spec defines the desired state of a AzDriverNode.
	// Required.
	Spec AzDriverNodeSpec `json:"spec" protobuf:"bytes,2,name=spec"`

	// status represents the current state of AzDriverNode.
	// If this is nil or empty, clients should prefer other nodes
	// for persistent volume allocations or pod places for pods which use azure persistent volumes.
	// +optional
	Status *AzDriverNodeStatus `json:"status,omitempty" protobuf:"bytes,3,opt,name=status"`
}

// AzDriverNodeSpec is the spec for a AzDriverNode resource.
type AzDriverNodeSpec struct {
	// Name of the node which this AzDriverNode represents.
	// Required.
	NodeName string `json:"nodeName" protobuf:"bytes,1,name=nodeName"`
}

// AzDriverNodeStatus is the status for a AzDriverNode resource.
type AzDriverNodeStatus struct {
	// LastHeartbeatTime represents the timestamp when a heatbeat was sent by driver node plugin.
	// A recent timestamp means that node-plugin is responsive and is communicating to API server.
	// Clients should not solely reply on LastHeartbeatTime to ascertain node plugin's health state.
	// +optional
	LastHeartbeatTime *int64 `json:"lastHeartbeatTime,omitempty" protobuf:"varint,1,opt,name=lastHeartbeatTime"`

	// ReadyForVolumeAllocation tells client wheather the node plug-in is ready for volume allocation.
	// If status is not present or ReadyForVolumeAllocation, then clients should prefer
	// other nodes in the clusters for azure persistent volumes\pod placements for pods with azure disks.
	// +optional
	ReadyForVolumeAllocation *bool `json:"readyForVolumeAllocation,omitempty" protobuf:"varint,2,opt,name=readyForVolumeAllocation"`

	// StatusMessage is a brief status message regarding nodes health
	// This field should not be used for any decision making in code
	// It is for display/debug purpose only
	// For code logic dependency, use Conditions filed
	// +optional
	StatusMessage *string `json:"statusMessage,omitempty" protobuf:"bytes,3,opt,name=statusMessage"`

	// Conditions contains an array of generic AzDriver related health conditions
	// These conditions can be used programatically to take decisions
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []AzDriverCondition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,4,rep,name=conditions"`
}

// AzDriverCondition defines condition for the AzDriver
type AzDriverCondition struct {
	// Type of node condition.
	Type AzDriverConditionType `json:"type" protobuf:"bytes,1,opt,name=type,casttype=AzDriverNodeConditionType"`
	// Status of the condition, one of True, False, Unknown.
	Status AzDriverConditionStatus `json:"status" protobuf:"bytes,2,opt,name=status,casttype=AzDriverConditionStatus"`
	// Last time we got an update on a given condition.
	// +optional
	LastHeartbeatTime metav1.Time `json:"lastHeartbeatTime,omitempty" protobuf:"bytes,3,opt,name=lastHeartbeatTime"`
	// Last time the condition transit from one status to another.
	// +optional
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty" protobuf:"bytes,4,opt,name=lastTransitionTime"`
	// (brief) reason for the condition's last transition.
	// +optional
	Reason string `json:"reason,omitempty" protobuf:"bytes,5,opt,name=reason"`
	// Human readable message indicating details about last transition.
	// +optional
	Message string `json:"message,omitempty" protobuf:"bytes,6,opt,name=message"`
}

// AzDriverConditionStatus defines condition status' for the AzDriver
type AzDriverConditionStatus string

const (
	// ConditionTrue means status of the given condition is true
	ConditionTrue AzDriverConditionStatus = "True"
	// ConditionFalse means status of the given condition is false
	ConditionFalse AzDriverConditionStatus = "False"
	// ConditionUnknown means status of the given condition is unknown
	ConditionUnknown AzDriverConditionStatus = "Unknown"
)

// AzDriverConditionType defines the condition type for AzDriver
type AzDriverConditionType string

const (
	// IsNodePluginReady means node plug-in is ready
	IsNodePluginReady AzDriverConditionType = "IsNodePluginReady"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AzDriverNodeList is a list of AzDriverNode resources
type AzDriverNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []AzDriverNode `json:"items"`
}


type VolumeCapability struct {
	// Specifies what API the volume will be accessed using. One of the
	// following fields MUST be specified.
	//
	// Types that are valid to be assigned to AccessType:
	//	*VolumeCapability_Block
	//	*VolumeCapability_Mount
	//TODO: figure out how to update the crd with this interface
	AccessType isVolumeCapability_AccessType `json:"access_type"`
	// This is a REQUIRED field.

	AccessMode *VolumeCapability_AccessMode `json:"access_mode,omitempty"`
	XXX_NoUnkeyedLiteral struct{}                     `json:"-"`
	XXX_unrecognized     []byte                       `json:"-"`
	XXX_sizecache        int32                        `json:"-"`
}

//Specify how a volume can be accessed
// +k8s:deepcopy-gen=false
type VolumeCapability_AccessMode struct {
	Mode VolumeCapability_AccessMode_Mode `json:"mode,omitempty"`
}

type VolumeCapability_AccessMode_Mode int32

type isVolumeCapability_AccessType interface {
	isVolumeCapability_AccessType()
}

func (*VolumeCapability_Block) isVolumeCapability_AccessType() {}
func (*VolumeCapability_Mount) isVolumeCapability_AccessType() {} 

type VolumeCapability_Mount struct {
	Mount *VolumeCapability_MountVolume `json:"mount"` 	
}
type VolumeCapability_Block struct {
	Block *VolumeCapability_BlockVolume `json:"block"`
}

type VolumeCapability_MountVolume struct {
	// The filesystem type. This field is OPTIONAL.
	// An empty string is equal to an unspecified field value.
	FsType string `protobuf:"bytes,1,opt,name=fs_type,json=fsType,proto3" json:"fs_type,omitempty"`
	// The mount options that can be used for the volume. This field is
	// OPTIONAL. `mount_flags` MAY contain sensitive information.
	// Therefore, the CO and the Plugin MUST NOT leak this information
	// to untrusted entities. The total size of this repeated field
	// SHALL NOT exceed 4 KiB.
	MountFlags           []string `protobuf:"bytes,2,rep,name=mount_flags,json=mountFlags,proto3" json:"mount_flags,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

// Indicate that the volume will be accessed via the block device API.
type VolumeCapability_BlockVolume struct {
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
} 


// The capacity of the storage space in bytes. To specify an exact size,
// `required_bytes` and `limit_bytes` SHALL be set to the same value. At
// least one of the these fields MUST be specified.
type CapacityRange struct {
	// Volume MUST be at least this big. This field is OPTIONAL.
	// A value of 0 is equal to an unspecified field value.
	// The value of this field MUST NOT be negative.
	RequiredBytes int64 `protobuf:"varint,1,opt,name=required_bytes,json=requiredBytes,proto3" json:"required_bytes,omitempty"`
	// Volume MUST not be bigger than this. This field is OPTIONAL.
	// A value of 0 is equal to an unspecified field value.
	// The value of this field MUST NOT be negative.
	LimitBytes           int64    `protobuf:"varint,2,opt,name=limit_bytes,json=limitBytes,proto3" json:"limit_bytes,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}