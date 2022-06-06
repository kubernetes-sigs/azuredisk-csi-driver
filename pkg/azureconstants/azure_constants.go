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

package azureconstants

import (
	"regexp"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"k8s.io/apimachinery/pkg/util/sets"
	api "k8s.io/kubernetes/pkg/apis/core"
)

const (
	AzureDiskCSIDriverName        = "azuredisk_csi_driver"
	CachingModeField              = "cachingmode"
	DefaultAzureCredentialFileEnv = "AZURE_CREDENTIAL_FILE"
	DefaultCredFilePathLinux      = "/etc/kubernetes/azure.json"
	DefaultCredFilePathWindows    = "C:\\k\\azure.json"
	DefaultAzureDiskCrdNamespace  = "azure-disk-csi"
	DefaultDriverName             = "disk.csi.azure.com"
	DesIDField                    = "diskencryptionsetid"
	DiskEncryptionTypeField       = "diskencryptiontype"
	DiskAccessIDField             = "diskaccessid"
	DiskIOPSReadWriteField        = "diskiopsreadwrite"
	DiskMBPSReadWriteField        = "diskmbpsreadwrite"
	DiskNameField                 = "diskname"
	EnableBurstingField           = "enablebursting"
	ErrDiskNotFound               = "not found"
	FsTypeField                   = "fstype"
	IncrementalField              = "incremental"
	KindField                     = "kind"
	LocationField                 = "location"
	LogicalSectorSizeField        = "logicalsectorsize"
	LUN                           = "LUN"
	MaxMountReplicaCountField     = "maxmountreplicacount"
	MaxSharesField                = "maxshares"
	MinimumDiskSizeGiB            = 1
	NetworkAccessPolicyField      = "networkaccesspolicy"
	NotFound                      = "NotFound"
	PerfProfileBasic              = "basic"
	PerfProfileAdvanced           = "advanced"
	PerfProfileField              = "perfprofile"
	PerfProfileNone               = "none"
	PremiumAccountPrefix          = "premium"
	PvcNameKey                    = "csi.storage.k8s.io/pvc/name"
	PvcNamespaceKey               = "csi.storage.k8s.io/pvc/namespace"
	PvcNamespaceTag               = "kubernetes.io-created-for-pvc-namespace"
	PvcNameTag                    = "kubernetes.io-created-for-pvc-name"
	PvNameKey                     = "csi.storage.k8s.io/pv/name"
	PvNameTag                     = "kubernetes.io-created-for-pv-name"
	RateLimited                   = "rate limited"
	RequestedSizeGib              = "requestedsizegib"
	ResizeRequired                = "resizeRequired"
	SubscriptionIDField           = "subscriptionid"
	ResourceGroupField            = "resourcegroup"
	ResourceNotFound              = "ResourceNotFound"
	SkuNameField                  = "skuname"
	SourceDiskSearchMaxDepth      = 10
	SourceSnapshot                = "snapshot"
	SourceVolume                  = "volume"
	StandardSsdAccountPrefix      = "standardssd"
	StorageAccountTypeField       = "storageaccounttype"
	TagsField                     = "tags"
	ThrottlingKey                 = "throttlingKey"
	TrueValue                     = "true"
	FalseValue                    = "false"
	UserAgentField                = "useragent"
	VolumeAttributePartition      = "partition"
	WellKnownTopologyKey          = "topology.kubernetes.io/zone"
	InstanceTypeKey               = "node.kubernetes.io/instance-type"
	TopologyRegionKey             = "topology.kubernetes.io/region"
	MasterNodeRoleTaintKey        = "node-role.kubernetes.io/master"
	WriteAcceleratorEnabled       = "writeacceleratorenabled"
	AttachableVolumesField        = "attachable-volumes-azure-disk"
	DeviceSettingsKeyPrefix       = "device-setting/"
	BlockDeviceRootPathLinux      = "/sys/block"
	DummyBlockDevicePathLinux     = "/sys/block/sda"

	// CRDs specific constants
	// 1. AzVolumeAttachmentFinalizer for AzVolumeAttachment objects handles deletion of AzVolumeAttachment CRIs
	// 2. AzVolumeAttachmentFinalizer for AzVolume prevents AzVolume CRI from being deleted before all AzVolumeAttachments attached to that volume is deleted as well
	AzVolumeAttachmentFinalizer = "disk.csi.azure.com/azvolumeattachment-finalizer"
	AzVolumeFinalizer           = "disk.csi.azure.com/azvolume-finalizer"
	// ControllerFinalizer is a finalizer added to the pod running Azuredisk driver controller
	// to prevent the pod deletion until clean up is completed
	ControllerFinalizer                   = "disk.csi.azure.com/azuredisk-finalizer"
	CleanUpAnnotation                     = "disk.csi.azure.com/clean-up"
	NodeNameLabel                         = "disk.csi.azure.com/node-name"
	PartitionLabel                        = "azdrivernodes.disk.csi.azure.com/partition"
	RoleLabel                             = "disk.csi.azure.com/requested-role"
	RoleChangeLabel                       = "disk.csi.azure.com/role-change"
	Demoted                               = "demoted"
	Promoted                              = "promoted"
	VolumeDeleteRequestAnnotation         = "disk.csi.azure.com/volume-delete-request"
	VolumeDetachRequestAnnotation         = "disk.csi.azure.com/volume-detach-request"
	RecoverAnnotation                     = "disk.csi.azure.com/recovery" // used to ensure reconciliation is triggered for recovering CRIs
	VolumeNameLabel                       = "disk.csi.azure.com/volume-name"
	VolumeIDLabel                         = "disk.csi.azure.com/volume-id"
	InlineVolumeAnnotation                = "disk.csi.azure.com/inline-volume"
	PodNameKey                            = "disk.csi/azure.com/pod-name"
	PreProvisionedVolumeAnnotation        = "disk.csi.azure.com/pre-provisioned"
	PreProvisionedVolumeCleanupAnnotation = "disk.csi.azure.com/pre-provisioned-clean-up"
	RequestIDKey                          = "disk.csi.azure.com/request-id"
	RequestStartimeKey                    = "disk.csi.azure.com/request-starttime"
	RequestTimeFormat                     = time.RFC3339Nano
	RequesterKey                          = "disk.csi.azure.com/requester-name"
	WorkflowKey                           = "disk.csi.azure.com/requester-name"

	ControllerClusterRoleName         = "azuredisk-external-provisioner-role"
	ControllerClusterRoleBindingName  = "azuredisk-csi-provisioner-binding"
	ControllerServiceAccountName      = "csi-azuredisk-controller-sa"
	ControllerServiceAccountFinalizer = "disk.csi.azure.com/azuredisk-controller"
	ReleaseNamespace                  = "kube-system"
	NamespaceField                    = "metadata.namespace"

	CRIUpdateRetryDuration  = time.Duration(1) * time.Second
	CRIUpdateRetryFactor    = 3.0
	CRIUpdateRetryStep      = 5
	DefaultInformerResync   = time.Duration(30) * time.Second
	ZonedField              = "zoned"
	TooManyRequests         = "TooManyRequests"
	ClientThrottled         = "client throttled"
	VolumeID                = "volumeid"
	Node                    = "node"
	SourceResourceID        = "source_resource_id"
	SnapshotName            = "snapshot_name"
	SnapshotID              = "snapshot_id"
	Latency                 = "latency"
	NormalUpdateMaxNetRetry = 0
	ForcedUpdateMaxNetRetry = 10
	DefaultBackoffCap       = 10 * time.Minute
	EnableAsyncAttachField  = "enableasyncattach"

	// define different sleep time when hit throttling
	SnapshotOpThrottlingSleepSec = 50

	CurrentNodeParameter = "currentNode"
	DevicePathParameter  = "devicePath"

	ReplicaAttachmentFailedEvent  = "ReplicaAttachmentFailed"
	ReplicaAttachmentSuccessEvent = "ReplicaAttachmentSucceeded"
	ClientFailedGetEvent          = "ClientFailedToGetObject"
)

type UnpublishMode int

const (
	Detach UnpublishMode = iota
	DemoteOrDetach
)

var (
	// ManagedDiskPath is described here: https://docs.microsoft.com/en-us/rest/api/compute/disks/createorupdate#create-a-managed-disk-from-an-existing-managed-disk-in-the-same-or-different-subscription.
	ManagedDiskPath   = "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/disks/%s"
	ManagedDiskPathRE = regexp.MustCompile(`(?i).*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/disks/(.+)`)

	DiskSnapshotPath        = "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/snapshots/%s"
	DiskSnapshotPathRE      = regexp.MustCompile(`(?i).*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/snapshots/(.+)`)
	DiskURISupportedManaged = []string{"/subscriptions/{sub-id}/resourcegroups/{group-name}/providers/microsoft.compute/disks/{disk-id}"}
	LunPathRE               = regexp.MustCompile(`/dev(?:.*)/disk/azure/scsi(?:.*)/lun(.+)`)
	SupportedCachingModes   = sets.NewString(
		string(api.AzureDataDiskCachingNone),
		string(api.AzureDataDiskCachingReadOnly),
		string(api.AzureDataDiskCachingReadWrite),
	)

	// VolumeCaps represents how the volume could be accessed.
	VolumeCaps = []csi.VolumeCapability_AccessMode{
		{
			Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		},
		{
			Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY,
		},
		{
			Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER,
		},
		{
			Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_MULTI_WRITER,
		},
		{
			Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY,
		},
		{
			Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_SINGLE_WRITER,
		},
		{
			Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
		},
	}
)
