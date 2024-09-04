/*
Copyright 2023 The Kubernetes Authors.

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

package disk

type StorageDeviceNumber struct {
	DeviceType      DeviceType
	DeviceNumber    uint32
	PartitionNumber uint32
}
type DeviceType uint32

type StoragePropertyID uint32

const (
	StorageDeviceProperty                  StoragePropertyID = 0
	StorageAdapterProperty                 StoragePropertyID = 1
	StorageDeviceIDProperty                StoragePropertyID = 2
	StorageDeviceUniqueIDProperty          StoragePropertyID = 3
	StorageDeviceWriteCacheProperty        StoragePropertyID = 4
	StorageMiniportProperty                StoragePropertyID = 5
	StorageAccessAlignmentProperty         StoragePropertyID = 6
	StorageDeviceSeekPenaltyProperty       StoragePropertyID = 7
	StorageDeviceTrimProperty              StoragePropertyID = 8
	StorageDeviceWriteAggregationProperty  StoragePropertyID = 9
	StorageDeviceDeviceTelemetryProperty   StoragePropertyID = 10
	StorageDeviceLBProvisioningProperty    StoragePropertyID = 11
	StorageDevicePowerProperty             StoragePropertyID = 12
	StorageDeviceCopyOffloadProperty       StoragePropertyID = 13
	StorageDeviceResiliencyProperty        StoragePropertyID = 14
	StorageDeviceMediumProductType         StoragePropertyID = 15
	StorageAdapterRpmbProperty             StoragePropertyID = 16
	StorageAdapterCryptoProperty           StoragePropertyID = 17
	StorageDeviceIoCapabilityProperty      StoragePropertyID = 18
	StorageAdapterProtocolSpecificProperty StoragePropertyID = 19
	StorageDeviceProtocolSpecificProperty  StoragePropertyID = 20
	StorageAdapterTemperatureProperty      StoragePropertyID = 21
	StorageDeviceTemperatureProperty       StoragePropertyID = 22
	StorageAdapterPhysicalTopologyProperty StoragePropertyID = 23
	StorageDevicePhysicalTopologyProperty  StoragePropertyID = 24
	StorageDeviceAttributesProperty        StoragePropertyID = 25
	StorageDeviceManagementStatus          StoragePropertyID = 26
	StorageAdapterSerialNumberProperty     StoragePropertyID = 27
	StorageDeviceLocationProperty          StoragePropertyID = 28
	StorageDeviceNumaProperty              StoragePropertyID = 29
	StorageDeviceZonedDeviceProperty       StoragePropertyID = 30
	StorageDeviceUnsafeShutdownCount       StoragePropertyID = 31
	StorageDeviceEnduranceProperty         StoragePropertyID = 32
)

type StorageQueryType uint32

const (
	PropertyStandardQuery StorageQueryType = iota
	PropertyExistsQuery
	PropertyMaskQuery
	PropertyQueryMaxDefined
)

type StoragePropertyQuery struct {
	PropertyID StoragePropertyID
	QueryType  StorageQueryType
	Byte       []AdditionalParameters
}

type AdditionalParameters byte

type StorageDeviceIDDescriptor struct {
	Version             uint32
	Size                uint32
	NumberOfIdentifiers uint32
	Identifiers         [1]byte
}

type StorageIdentifierCodeSet uint32

const (
	StorageIDCodeSetReserved StorageIdentifierCodeSet = 0
	StorageIDCodeSetBinary   StorageIdentifierCodeSet = 1
	StorageIDCodeSetASCII    StorageIdentifierCodeSet = 2
	StorageIDCodeSetUtf8     StorageIdentifierCodeSet = 3
)

type StorageIdentifierType uint32

const (
	StorageIDTypeVendorSpecific           StorageIdentifierType = 0
	StorageIDTypeVendorID                 StorageIdentifierType = 1
	StorageIDTypeEUI64                    StorageIdentifierType = 2
	StorageIDTypeFCPHName                 StorageIdentifierType = 3
	StorageIDTypePortRelative             StorageIdentifierType = 4
	StorageIDTypeTargetPortGroup          StorageIdentifierType = 5
	StorageIDTypeLogicalUnitGroup         StorageIdentifierType = 6
	StorageIDTypeMD5LogicalUnitIdentifier StorageIdentifierType = 7
	StorageIDTypeScsiNameString           StorageIdentifierType = 8
)

type StorageAssociationType uint32

const (
	StorageIDAssocDevice StorageAssociationType = 0
	StorageIDAssocPort   StorageAssociationType = 1
	StorageIDAssocTarget StorageAssociationType = 2
)

type StorageIdentifier struct {
	CodeSet        StorageIdentifierCodeSet
	Type           StorageIdentifierType
	IdentifierSize uint16
	NextOffset     uint16
	Association    StorageAssociationType
	Identifier     [1]byte
}

type Disk struct {
	Path         string `json:"Path"`
	SerialNumber string `json:"SerialNumber"`
}

// Location definition
type Location struct {
	Adapter string
	Bus     string
	Target  string
	LUNID   string
}

// IDs definition
type IDs struct {
	Page83       string
	SerialNumber string
}
