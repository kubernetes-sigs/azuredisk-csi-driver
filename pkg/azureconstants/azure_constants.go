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
)

const (
	AzureDiskCSIDriverName            = "azuredisk_csi_driver"
	CachingModeField                  = "cachingmode"
	DefaultAzureCredentialFileEnv     = "AZURE_CREDENTIAL_FILE"
	DefaultCredFilePathLinux          = "/etc/kubernetes/azure.json"
	DefaultCredFilePathWindows        = "C:\\k\\azure.json"
	DefaultDriverName                 = "disk.csi.azure.com"
	DesIDField                        = "diskencryptionsetid"
	DiskEncryptionTypeField           = "diskencryptiontype"
	DiskAccessIDField                 = "diskaccessid"
	DiskIOPSReadWriteField            = "diskiopsreadwrite"
	DiskMBPSReadWriteField            = "diskmbpsreadwrite"
	DiskNameField                     = "diskname"
	EnableBurstingField               = "enablebursting"
	ErrDiskNotFound                   = "not found"
	FsTypeField                       = "fstype"
	IncrementalField                  = "incremental"
	KindField                         = "kind"
	LocationField                     = "location"
	LogicalSectorSizeField            = "logicalsectorsize"
	LUN                               = "LUN"
	MaxSharesField                    = "maxshares"
	MinimumDiskSizeGiB                = 1
	NetworkAccessPolicyField          = "networkaccesspolicy"
	PublicNetworkAccessField          = "publicnetworkaccess"
	NotFound                          = "NotFound"
	PerfProfileBasic                  = "basic"
	PerfProfileAdvanced               = "advanced"
	PerfProfileField                  = "perfprofile"
	PerfProfileNone                   = "none"
	PremiumAccountPrefix              = "premium"
	PvcNameKey                        = "csi.storage.k8s.io/pvc/name"
	PvcNamespaceKey                   = "csi.storage.k8s.io/pvc/namespace"
	PvcNamespaceTag                   = "kubernetes.io-created-for-pvc-namespace"
	PvcNameTag                        = "kubernetes.io-created-for-pvc-name"
	PvNameTag                         = "kubernetes.io-created-for-pv-name"
	PvcMetaDataName                   = "${pvc.metadata.name}"
	PvcMetaDataNamespace              = "${pvc.metadata.namespace}"
	PvMetaDataName                    = "${pv.metadata.name}"
	SnapshotNamespaceTag              = "kubernetes.io-created-for-snapshot-namespace"
	SnapshotNameTag                   = "kubernetes.io-created-for-snapshot-name"
	PvNameKey                         = "csi.storage.k8s.io/pv/name"
	VolumeSnapshotNameKey             = "csi.storage.k8s.io/volumesnapshot/name"
	VolumeSnapshotNamespaceKey        = "csi.storage.k8s.io/volumesnapshot/namespace"
	VolumeSnapshotContentNameKey      = "csi.storage.k8s.io/volumesnapshotcontent/name"
	RateLimited                       = "rate limited"
	RequestedSizeGib                  = "requestedsizegib"
	ResizeRequired                    = "resizeRequired"
	SubscriptionIDField               = "subscriptionid"
	ResourceGroupField                = "resourcegroup"
	DataAccessAuthModeField           = "dataaccessauthmode"
	ResourceNotFound                  = "ResourceNotFound"
	SkuNameField                      = "skuname"
	SourceDiskSearchMaxDepth          = 10
	SourceSnapshot                    = "snapshot"
	SourceVolume                      = "volume"
	StandardSsdAccountPrefix          = "standardssd"
	StorageAccountTypeField           = "storageaccounttype"
	TagsField                         = "tags"
	GetDiskThrottlingKey              = "getdiskthrottlingKey"
	CheckDiskLunThrottlingKey         = "checkdisklunthrottlingKey"
	TrueValue                         = "true"
	FalseValue                        = "false"
	UserAgentField                    = "useragent"
	VolumeAttributePartition          = "partition"
	WellKnownTopologyKey              = "topology.kubernetes.io/zone"
	InstanceTypeKey                   = "node.kubernetes.io/instance-type"
	WriteAcceleratorEnabled           = "writeacceleratorenabled"
	ZonedField                        = "zoned"
	EnableAsyncAttachField            = "enableasyncattach"
	PerformancePlusField              = "enableperformanceplus"
	PerformancePlusMinimumDiskSizeGiB = 513
	AttachDiskInitialDelayField       = "attachdiskinitialdelay"
	TooManyRequests                   = "TooManyRequests"
	ClientThrottled                   = "client throttled"
	VolumeID                          = "volumeid"
	Node                              = "node"
	SourceResourceID                  = "source_resource_id"
	SnapshotName                      = "snapshot_name"
	SnapshotID                        = "snapshot_id"
	DeviceSettingsKeyPrefix           = "device-setting/"
	BlockDeviceRootPathLinux          = "/sys/block"
	DummyBlockDevicePathLinux         = "/sys/block/sda"
	// define different sleep time when hit throttling
	SnapshotOpThrottlingSleepSec    = 50
	MaxThrottlingSleepSec           = 1200
	AgentNotReadyNodeTaintKeySuffix = "/agent-not-ready"
	// define tag value delimiter and default is comma
	TagValueDelimiterField       = "tagvaluedelimiter"
	AzureDiskDriverTag           = "kubernetes-azure-dd"
	InstantAccessDurationMinutes = "instantaccessdurationminutes"
)

var (
	// ManagedDiskPath is described here: https://docs.microsoft.com/en-us/rest/api/compute/disks/createorupdate#create-a-managed-disk-from-an-existing-managed-disk-in-the-same-or-different-subscription.
	ManagedDiskPath    = "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/disks/%s"
	ManagedDiskPathRE  = regexp.MustCompile(`(?i).*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/disks/(.+)`)
	DiskSnapshotPathRE = regexp.MustCompile(`(?i).*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/snapshots/(.+)`)
)
