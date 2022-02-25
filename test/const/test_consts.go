/*
Copyright 2019 The Kubernetes Authors.

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

package testconsts

import (
	"os"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	AzureDriverNameVar = "AZURE_STORAGE_DRIVER"
	TopologyKey        = "topology.disk.csi.azure.com/zone"
	HostNameLabel      = "kubernetes.io/hostname"

	AzurePublicCloud            = "AzurePublicCloud"
	ResourceGroupPrefix         = "azuredisk-csi-driver-test-"
	TempAzureCredentialFilePath = "/tmp/azure.json"

	AzureCredentialFileTemplate = `{
    "cloud": "{{.Cloud}}",
    "tenantId": "{{.TenantID}}",
    "subscriptionId": "{{.SubscriptionID}}",
    "aadClientId": "{{.AADClientID}}",
    "aadClientSecret": "{{.AADClientSecret}}",
    "resourceGroup": "{{.ResourceGroup}}",
    "location": "{{.Location}}",
    "vmType": "{{.VMType}}"
}`
	DefaultAzurePublicCloudLocation = "eastus2"
	DefaultAzurePublicCloudVMType   = "vmss"

	// Env vars
	TenantIDEnvVar        = "AZURE_TENANT_ID"
	SubscriptionIDEnvVar  = "AZURE_SUBSCRIPTION_ID"
	AadClientIDEnvVar     = "AZURE_CLIENT_ID"
	AadClientSecretEnvVar = "AZURE_CLIENT_SECRET"
	ResourceGroupEnvVar   = "AZURE_RESOURCE_GROUP"
	LocationEnvVar        = "AZURE_LOCATION"
	VMTypeEnvVar          = "AZURE_VM_TYPE"

	VolumeSnapshotClassKind = "VolumeSnapshotClass"
	VolumeSnapshotKind      = "VolumeSnapshot"
	VolumePVCKind           = "PersistentVolumeClaim"
	APIVersionv1            = "v1"
	SnapshotAPIGroup        = "snapshot.storage.k8s.io"
	SnapshotAPIVersion      = SnapshotAPIGroup + "/" + APIVersionv1

	KubeconfigEnvVar = "KUBECONFIG"
	ReportDirEnvVar  = "ARTIFACTS"
	DefaultReportDir = "/workspace/_artifacts"

	testMigrationEnvVar     = "TEST_MIGRATION"
	testWindowsEnvVar       = "TEST_WINDOWS"
	CloudNameEnvVar         = "AZURE_CLOUD_NAME"
	inTreeStorageClass      = "kubernetes.io/azure-disk"
	BuildV2Driver           = "BUILD_V2"
	useOnlyDefaultScheduler = "USE_ONLY_DEFAULT_SCHEDULER"

	ExecTimeout = 10 * time.Second
	// Some pods can take much longer to get ready due to volume attach/detach latency.
	SlowPodStartTimeout = 10 * time.Minute
	// Description that will printed during tests
	FailedConditionDescription = "Error status code"

	Poll            = 2 * time.Second
	PollLongTimeout = 5 * time.Minute
	PollTimeout     = 10 * time.Minute

	testTolerationKey   = "test-toleration-key"
	testTolerationValue = "test-toleration-value"
	testLabelKey        = "test-label-key"
	testLabelValue      = "test-label-value"

	MasterNodeLabel = "node-role.kubernetes.io/master"
)

var (
	AzurePublicCloudSupportedStorageAccountTypes = []string{"Standard_LRS", "Premium_LRS", "StandardSSD_LRS"}
	AzureStackCloudSupportedStorageAccountTypes  = []string{"Standard_LRS", "Premium_LRS"}

	TestTaint = v1.Taint{
		Key:    testTolerationKey,
		Value:  testTolerationValue,
		Effect: v1.TaintEffectNoSchedule,
	}

	TestLabel = map[string]string{
		testLabelKey: testLabelValue,
	}

	TestAffinity = v1.Affinity{
		NodeAffinity: &v1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
				NodeSelectorTerms: []v1.NodeSelectorTerm{
					{
						MatchExpressions: []v1.NodeSelectorRequirement{
							{
								Key:      testLabelKey,
								Operator: v1.NodeSelectorOpExists,
							},
						},
					},
				},
			},
		},
	}

	TestNodeAffinity = v1.Affinity{
		NodeAffinity: &v1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
				NodeSelectorTerms: []v1.NodeSelectorTerm{
					{
						MatchExpressions: []v1.NodeSelectorRequirement{
							{
								Key:      testLabelKey,
								Operator: v1.NodeSelectorOpExists,
							},
						},
					},
				},
			},
		},
	}

	TestPodAffinity = v1.Affinity{
		PodAffinity: &v1.PodAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []v1.PodAffinityTerm{
				{
					LabelSelector: &metav1.LabelSelector{MatchLabels: TestLabel},
					TopologyKey:   HostNameLabel,
				}},
		},
	}

	TestPodAntiAffinity = v1.Affinity{
		PodAntiAffinity: &v1.PodAntiAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []v1.PodAffinityTerm{
				{
					LabelSelector: &metav1.LabelSelector{MatchLabels: TestLabel},
					TopologyKey:   HostNameLabel,
				}},
		},
	}

	IsUsingInTreeVolumePlugin   = os.Getenv(AzureDriverNameVar) == inTreeStorageClass
	IsTestingMigration          = os.Getenv(testMigrationEnvVar) != ""
	IsWindowsCluster            = os.Getenv(testWindowsEnvVar) != ""
	IsUsingCSIDriverV2          = strings.EqualFold(os.Getenv(BuildV2Driver), "true")
	IsUsingOnlyDefaultScheduler = os.Getenv(useOnlyDefaultScheduler) != ""
	IsAzureStackCloud           = strings.EqualFold(os.Getenv(CloudNameEnvVar), "AZURESTACKCLOUD")
)
