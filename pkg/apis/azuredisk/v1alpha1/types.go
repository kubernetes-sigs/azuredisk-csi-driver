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
	k8stypes "k8s.io/apimachinery/pkg/types"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AzVolume is a specification for an AzVolume resource
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="State",type="string",JSONPath=`.status.state`,description="Indicates the state of the volume"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=`.status.detail.phase`,description="Indicates the phase of the underlying persistent volume"
type AzVolume struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of an AzVolume.
	// Required.
	Spec AzVolumeSpec `json:"spec"`
	// status represents the current state of AzVolume.
	// includes error, state, and volume status
	// +required
	Status AzVolumeStatus `json:"status"`
}

type AzVolumePhase string

const (
	VolumePending   AzVolumePhase = "Pending"
	VolumeBound     AzVolumePhase = "Bound"
	VolumeReleased  AzVolumePhase = "Released"
	VolumeAvailable AzVolumePhase = "Available"
	VolumeFailed    AzVolumePhase = "Failed"
)

// AzVolumeSpec is the spec for an AzVolume resource
type AzVolumeSpec struct {
	//the disk URI of the underlying volume
	UnderlyingVolume     string `json:"underlyingVolume"`
	MaxMountReplicaCount int    `json:"maxMountReplicaCount"`
	//The capabilities that the volume MUST have
	VolumeCapability []VolumeCapability `json:"volumeCapability"`
	//The capacity of the storage
	//+optional
	CapacityRange *CapacityRange `json:"capacityRange,omitempty"`
	//Parameters for the volume
	//+optional
	Parameters map[string]string `json:"parameters,omitempty"`
	//Secrets for the volume
	//+optional
	Secrets map[string]string `json:"secrets,omitempty"`
	//ContentVolumeSource for the volume
	//+optional
	ContentVolumeSource *ContentVolumeSource `json:"contentVolumeSource,omitempty"`
	//Specifies where the provisioned volume should be accessible
	//+optional
	AccessibilityRequirements *TopologyRequirement `json:"accessibilityRequirements,omitempty"`
}

type AzVolumeState string

const (
	VolumeOperationPending AzVolumeState = "Pending"
	VolumeCreating         AzVolumeState = "Creating"
	VolumeCreationFailed   AzVolumeState = "CreationFailed"
	VolumeCreated          AzVolumeState = "Created"
	VolumeUpdating         AzVolumeState = "Updating"
	VolumeUpdateFailed     AzVolumeState = "UpdateFailed"
	VolumeUpdated          AzVolumeState = "Updated"
	VolumeDeleting         AzVolumeState = "Deleting"
	VolumeDeletionFailed   AzVolumeState = "DeletionFailed"
	VolumeDeleted          AzVolumeState = "Deleted"
)

// AzVolumeStatus is the status for an AzVolume resource
type AzVolumeStatus struct {
	//Current status detail of the AzVolume
	//Nil detail indicates that the volume has not been created
	//+optional
	Detail *AzVolumeStatusDetail `json:"detail,omitempty"`

	//Current state of underlying volume
	//+required
	State AzVolumeState `json:"state"`

	//Error occurred during creation/deletion of volume
	//+optional
	Error *AzError `json:"error,omitempty"`
}

// AzVolumeStatusDetail is the status of the underlying Volume resource
type AzVolumeStatusDetail struct {
	//Current status of the AzVolume
	//+optional
	ResponseObject *AzVolumeStatusParams `json:"status,omitempty"`
	//Current phase of the underlying PV
	//+optional
	Phase AzVolumePhase `json:"phase,omitempty"`
}

type AzVolumeStatusParams struct {
	VolumeID string `json:"volume_id"`
	// +optional
	VolumeContext map[string]string `json:"parameters,omitempty"`
	CapacityBytes int64             `json:"capacity_bytes"`
	// +optional
	ContentSource *ContentVolumeSource `json:"content_source,omitempty"`
	// +optional
	AccessibleTopology    []Topology `json:"accessible_topology,omitempty"`
	NodeExpansionRequired bool       `json:"node_expansion_required"`
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
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="The age of the attachment"
// +kubebuilder:printcolumn:name="NodeName",type=string,JSONPath=`.spec.nodeName`,description="Name of the Node which this AzVolumeAttachment object is attached to"
// +kubebuilder:printcolumn:name="UnderlyingVolume",type=string,JSONPath=`.spec.underlyingVolume`,description="Name of the Volume which this AzVolumeAttachment object references"
// +kubebuilder:printcolumn:name="RequestedRole",type=string,JSONPath=`.spec.role`,description="Indicates if the volume attachment should be primary attachment or not"
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=`.status.detail.role`,description="Indicates if the volume attachment is primary attachment or not"
type AzVolumeAttachment struct {
	metav1.TypeMeta `json:",inline"`
	// Standard object's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/api-conventions.md#metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of a AzVolumeAttachment.
	// Required.
	Spec AzVolumeAttachmentSpec `json:"spec"`

	// status represents the current state of AzVolumeAttachment.
	// includes error, state, and attachment status
	// Required
	Status AzVolumeAttachmentStatus `json:"status,omitempty"`
}

// AzVolumeAttachmentSpec is the spec for a AzVolumeAttachment resource
type AzVolumeAttachmentSpec struct {
	UnderlyingVolume string            `json:"underlyingVolume"`
	VolumeID         string            `json:"volume_id"`
	NodeName         string            `json:"nodeName"`
	VolumeContext    map[string]string `json:"volume_context"`
	RequestedRole    Role              `json:"role"`
}

// Role indicates if the volume attachment is replica attachment or not
type Role string

const (
	// Primary indicates that the specified node the volume is attached to is where the pod is currently running on
	PrimaryRole Role = "Primary"
	// Replica indicates that the specified node the volume is attached to is one of replicas that pod can failover to if the primary node fails
	ReplicaRole Role = "Replica"
)

// AzVolumeAttachmentAttachmentState indicates the current attachment state of the underlying volume and node
type AzVolumeAttachmentAttachmentState string

const (
	// Pending indicates a state where no operation has been initated
	AttachmentPending AzVolumeAttachmentAttachmentState = "Pending"
	// AttachmentInProgress indicates that node to volume attachment is in progress
	Attaching AzVolumeAttachmentAttachmentState = "Attaching"
	// Attached indicates that the volume has been successfully attached to node
	Attached AzVolumeAttachmentAttachmentState = "Attached"
	// AttachmentFailed indicates that the volume attachment has failed
	AttachmentFailed AzVolumeAttachmentAttachmentState = "AttachmentFailed"
	// Detaching indicates that the node to volume detachment is in progress
	Detaching AzVolumeAttachmentAttachmentState = "Detaching"
	// Detached indicates that the volume has been successfully detached from the volume
	Detached AzVolumeAttachmentAttachmentState = "Detached"
	// DetachmentFailed indicates that the volume detachment has failed
	DetachmentFailed AzVolumeAttachmentAttachmentState = "DetachmentFailed"
)

// AzVolumeAttachmentStatus is the status for a AzVolumeAttachment resource
type AzVolumeAttachmentStatus struct {
	//Status summarizes the current attachment state of the volume attachment
	//Nil Status indicates that the volume has not yet been attached to the node
	//+optional
	Detail *AzVolumeAttachmentStatusDetail `json:"detail,omitempty"`
	//State shows the current attachment state (whether operations are in progress or not)
	//+required
	State AzVolumeAttachmentAttachmentState `json:"state,omitempty"`
	//Error occurred during attach/detach of volume
	//+optional
	Error *AzError `json:"error,omitempty"`
}

// AzVolumeAttachmentStatusDetail is the status of the attachment between specified node and volume.
type AzVolumeAttachmentStatusDetail struct {
	Role Role `json:"role"`
	//+optional
	PublishContext map[string]string `json:"publish_context,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AzVolumeAttachmentList is a list of AzVolumeAttachment resources
type AzVolumeAttachmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []AzVolumeAttachment `json:"items"`
}

type AzError struct {
	ErrorCode    string            `json:"errorCode"`
	ErrorMessage string            `json:"errorMessage"`
	CurrentNode  k8stypes.NodeName `json:"currentNode"`
	DevicePath   string            `json:"devicePath"`
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

type VolumeCapabilityAccessMode int

const (
	VolumeCapabilityAccessModeUnknown VolumeCapabilityAccessMode = iota
	VolumeCapabilityAccessModeSingleNodeWriter
	VolumeCapabilityAccessModeSingleNodeReaderOnly
	VolumeCapabilityAccessModeMultiNodeReaderOnly
	VolumeCapabilityAccessModeMultiNodeSingleWriter
	VolumeCapabilityAccessModeMultiNodeMultiWriter
)

type VolumeCapabilityAccess int

const (
	VolumeCapabilityAccessBlock VolumeCapabilityAccess = iota
	VolumeCapabilityAccessMount
)

type VolumeCapabilityAccessDetails struct {
	// Specifies the access type for the volume.
	AccessType VolumeCapabilityAccess `json:"access_type"`
	// The filesystem type. This field is OPTIONAL.
	// An empty string is equal to an unspecified field value.
	// +optional
	FsType string `json:"fs_type"`
	// The mount options that can be used for the volume. This field is
	// OPTIONAL. `mount_flags` MAY contain sensitive information.
	// Therefore, the CO and the Plugin MUST NOT leak this information
	// to untrusted entities. The total size of this repeated field
	// SHALL NOT exceed 4 KiB.
	// +optional
	MountFlags []string `json:"mount_flags,omitempty"`
}

type VolumeCapability struct {
	// Specifies what API the volume will be accessed using. One of the
	// following fields MUST be specified.
	//
	// Types that are valid to be assigned to AccessType: block, mount
	AccessDetails VolumeCapabilityAccessDetails `json:"access_details"`
	// This is a REQUIRED field.
	AccessMode VolumeCapabilityAccessMode `json:"access_mode"`
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
	LimitBytes int64 `protobuf:"varint,2,opt,name=limit_bytes,json=limitBytes,proto3" json:"limit_bytes,omitempty"`
}

type ContentVolumeSourceType int

const (
	ContentVolumeSourceTypeVolume ContentVolumeSourceType = iota
	ContentVolumeSourceTypeSnapshot
)

type ContentVolumeSource struct {
	ContentSource   ContentVolumeSourceType `json:"content_source"`
	ContentSourceID string                  `json:"content_source_id"`
}

type TopologyRequirement struct {
	Requisite []Topology `json:"requisite,omitempty"`
	Preferred []Topology `json:"preferred,omitempty"`
}

type Topology struct {
	Segments map[string]string `json:"segments,omitempty"`
}

type Snapshot struct {
	SnapshotID     string      `json:"snapshot_id"`
	SourceVolumeID string      `json:"source_volume_id"`
	CreationTime   metav1.Time `json:"creation_time"`
	ReadyToUse     bool        `json:"ready_to_use"`
	// +optional
	SizeBytes int64 `json:"size_bytes,omitempty"`
}

type VolumeDetails struct {
	VolumeID string `json:"volume_id"`
	// +optional
	CapacityBytes int64 `json:"capacity_bytes,omitempty"`
	// +optional
	VolumeContext map[string]string `json:"volume_context,omitempty"`
	// +optional
	ContentSource *ContentVolumeSource `json:"content_source,omitempty"`
	// +optional
	AccessibleTopology []Topology `json:"accessible_topology,omitempty"`
}

type VolumeCondition struct {
	Abnormal bool   `json:"abnormal"`
	Message  string `json:"message"`
}

type VolumeStatus struct {
	// +optional
	PublishedNodeIds []string `json:"published_node_ids,omitempty"`
	// +optional
	Condition *VolumeCondition `json:"condition,omitempty"`
}

type VolumeEntry struct {
	Details *VolumeDetails `json:"details,omitempty"`
	// +optional
	Status *VolumeStatus `json:"status,omitempty"`
}

type ListVolumesResult struct {
	Entries []VolumeEntry `json:"entries"`
	// +optional
	NextToken string `json:"next_token,omitempty"`
}

type ListSnapshotsResult struct {
	Entries []Snapshot `json:"entries"`
	// +optional
	NextToken string `json:"next_token,omitempty"`
}
