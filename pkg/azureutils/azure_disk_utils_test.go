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

package azureutils

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2021-07-01/compute"
	"github.com/Azure/go-autorest/autorest/to"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/testutil"
)

func TestCheckDiskName(t *testing.T) {
	tests := []struct {
		diskName string
		expected bool
	}{
		{
			diskName: "a",
			expected: true,
		},
		{
			diskName: ".",
			expected: false,
		},
		{
			diskName: "_",
			expected: false,
		},
		{
			diskName: "_",
			expected: false,
		},
		{
			diskName: "09",
			expected: true,
		},
		{
			diskName: "az",
			expected: true,
		},
		{
			diskName: "1_",
			expected: true,
		},
		{
			diskName: "_1",
			expected: false,
		},
		{
			diskName: "1.",
			expected: false,
		},
		{
			diskName: "1-",
			expected: false,
		},
		{
			diskName: "0.z",
			expected: true,
		},
		{
			diskName: "1.2",
			expected: true,
		},
		{
			diskName: "a-9",
			expected: true,
		},
		{
			diskName: "a_c",
			expected: true,
		},
		{
			diskName: "1__",
			expected: true,
		},
		{
			diskName: "a---9",
			expected: true,
		},
		{
			diskName: "1#2",
			expected: false,
		},
		{
			diskName: "-",
			expected: false,
		},
		{
			diskName: "test",
			expected: true,
		},
	}

	for _, test := range tests {
		result := checkDiskName(test.diskName)
		if !reflect.DeepEqual(result, test.expected) {
			t.Errorf("input: %q, checkShareNameBeginAndEnd result: %v, expected: %v", test.diskName, result, test.expected)
		}
	}
}

func TestGetCachingMode(t *testing.T) {
	tests := []struct {
		options             map[string]string
		expectedCachingMode compute.CachingTypes
		expectedError       bool
	}{
		{
			nil,
			compute.CachingTypes(defaultAzureDataDiskCachingMode),
			false,
		},
		{
			map[string]string{},
			compute.CachingTypes(defaultAzureDataDiskCachingMode),
			false,
		},
		{
			map[string]string{consts.CachingModeField: ""},
			compute.CachingTypes(defaultAzureDataDiskCachingMode),
			false,
		},
		{
			map[string]string{consts.CachingModeField: "None"},
			compute.CachingTypes("None"),
			false,
		},
		{
			map[string]string{consts.CachingModeField: "ReadOnly"},
			compute.CachingTypes("ReadOnly"),
			false,
		},
		{
			map[string]string{consts.CachingModeField: "ReadWrite"},
			compute.CachingTypes("ReadWrite"),
			false,
		},
		{
			map[string]string{consts.CachingModeField: "WriteOnly"},
			compute.CachingTypes(""),
			true,
		},
	}

	for _, test := range tests {
		resultCachingMode, resultError := GetCachingMode(test.options)
		if resultCachingMode != test.expectedCachingMode || (resultError != nil) != test.expectedError {
			t.Errorf("input: %s, getCachingMode resultCachingMode: %s, expectedCachingMode: %s, resultError: %s, expectedError: %t", test.options, resultCachingMode, test.expectedCachingMode, resultError, test.expectedError)
		}
	}
}

func TestGetKubeConfig(t *testing.T) {
	// skip for now as this is very flaky on Windows
	skipIfTestingOnWindows(t)
	emptyKubeConfig := "empty-Kube-Config"
	validKubeConfig := "valid-Kube-Config"
	fakeContent := `
apiVersion: v1
clusters:
- cluster:
    server: https://localhost:8080
  name: foo-cluster
contexts:
- context:
    cluster: foo-cluster
    user: foo-user
    namespace: bar
  name: foo-context
current-context: foo-context
kind: Config
users:
- name: foo-user
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      args:
      - arg-1
      - arg-2
      command: foo-command
`

	err := createTestFile(emptyKubeConfig)
	if err != nil {
		t.Error(err)
	}
	defer func() {
		if err := os.Remove(emptyKubeConfig); err != nil {
			t.Error(err)
		}
	}()

	err = createTestFile(validKubeConfig)
	if err != nil {
		t.Error(err)
	}
	defer func() {
		if err := os.Remove(validKubeConfig); err != nil {
			t.Error(err)
		}
	}()

	if err := ioutil.WriteFile(validKubeConfig, []byte(fakeContent), 0666); err != nil {
		t.Error(err)
	}

	tests := []struct {
		desc                     string
		kubeconfig               string
		expectError              bool
		envVariableHasConfig     bool
		envVariableConfigIsValid bool
	}{
		{
			desc:                     "[success] valid kube config passed",
			kubeconfig:               validKubeConfig,
			expectError:              false,
			envVariableHasConfig:     false,
			envVariableConfigIsValid: false,
		},
		{
			desc:                     "[failure] invalid kube config passed",
			kubeconfig:               emptyKubeConfig,
			expectError:              true,
			envVariableHasConfig:     false,
			envVariableConfigIsValid: false,
		},
	}

	for _, test := range tests {
		_, err := GetKubeConfig(test.kubeconfig)
		receiveError := (err != nil)
		if test.expectError != receiveError {
			t.Errorf("desc: %s,\n input: %q, GetCloudProvider err: %v, expectErr: %v", test.desc, test.kubeconfig, err, test.expectError)
		}
	}
}

func TestGetCloudProvider(t *testing.T) {
	fakeCredFile, err := testutil.GetWorkDirPath("fake-cred-file.json")
	if err != nil {
		t.Errorf("GetWorkDirPath failed with %v", err)
	}
	fakeKubeConfig, err := testutil.GetWorkDirPath("fake-kube-config")
	if err != nil {
		t.Errorf("GetWorkDirPath failed with %v", err)
	}
	emptyKubeConfig, err := testutil.GetWorkDirPath("empty-kube-config")
	if err != nil {
		t.Errorf("GetWorkDirPath failed with %v", err)
	}

	fakeContent := `apiVersion: v1
clusters:
- cluster:
    server: https://localhost:8080
  name: foo-cluster
contexts:
- context:
    cluster: foo-cluster
    user: foo-user
    namespace: bar
  name: foo-context
current-context: foo-context
kind: Config
users:
- name: foo-user
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      args:
      - arg-1
      - arg-2
      command: foo-command
`

	err = createTestFile(emptyKubeConfig)
	if err != nil {
		t.Error(err)
	}
	defer func() {
		if err := os.Remove(emptyKubeConfig); err != nil {
			t.Error(err)
		}
	}()

	tests := []struct {
		desc                  string
		createFakeCredFile    bool
		createFakeKubeConfig  bool
		kubeconfig            string
		userAgent             string
		allowEmptyCloudConfig bool
		expectedErr           error
	}{
		{
			desc:                  "[failure] out of cluster & in cluster, specify a fake kubeconfig, no credential file",
			createFakeKubeConfig:  true,
			kubeconfig:            fakeKubeConfig,
			allowEmptyCloudConfig: false,
			expectedErr: testutil.TestError{
				DefaultError: fmt.Errorf("no cloud config provided, error"),
			},
		},
		{
			desc:                  "[failure] out of cluster & in cluster, specify a empty kubeconfig, no credential file",
			kubeconfig:            emptyKubeConfig,
			allowEmptyCloudConfig: true,
			expectedErr:           fmt.Errorf("failed to get KubeClient: invalid configuration: no configuration has been provided, try setting KUBERNETES_MASTER environment variable"),
		},
		{
			desc:                  "[success] out of cluster & in cluster, no kubeconfig, a fake credential file",
			createFakeCredFile:    true,
			kubeconfig:            "",
			userAgent:             "useragent",
			allowEmptyCloudConfig: true,
			expectedErr:           nil,
		},
		{
			desc:                  "[success] out of cluster & in cluster, specify a fake kubeconfig, no credential file",
			createFakeKubeConfig:  true,
			kubeconfig:            fakeKubeConfig,
			allowEmptyCloudConfig: true,
			expectedErr:           nil,
		},
	}

	for _, test := range tests {
		if test.createFakeCredFile {
			if err := createTestFile(fakeCredFile); err != nil {
				t.Error(err)
			}
			defer func() {
				os.Remove(fakeCredFile)
			}()

			originalCredFile, ok := os.LookupEnv(consts.DefaultAzureCredentialFileEnv)
			if ok {
				defer os.Setenv(consts.DefaultAzureCredentialFileEnv, originalCredFile)
			} else {
				defer os.Unsetenv(consts.DefaultAzureCredentialFileEnv)
			}
			os.Setenv(consts.DefaultAzureCredentialFileEnv, fakeCredFile)
		}
		if test.createFakeKubeConfig {
			if err := createTestFile(fakeKubeConfig); err != nil {
				t.Error(err)
			}
			defer func() {
				os.Remove(fakeKubeConfig)
			}()

			if err := ioutil.WriteFile(fakeKubeConfig, []byte(fakeContent), 0666); err != nil {
				t.Error(err)
			}
		}
		cloud, err := GetCloudProvider(test.kubeconfig, "", "", test.userAgent, test.allowEmptyCloudConfig)
		if !reflect.DeepEqual(err, test.expectedErr) && !strings.Contains(err.Error(), test.expectedErr.Error()) {
			t.Errorf("desc: %s,\n input: %q, GetCloudProvider err: %v, expectedErr: %v", test.desc, test.kubeconfig, err, test.expectedErr)
		}
		if cloud != nil {
			assert.Equal(t, cloud.UserAgent, test.userAgent)
			assert.Equal(t, cloud.DiskRateLimit != nil && cloud.DiskRateLimit.CloudProviderRateLimit, false)
			assert.Equal(t, cloud.SnapshotRateLimit != nil && cloud.SnapshotRateLimit.CloudProviderRateLimit, false)
		}
	}
}

func TestGetDiskLUN(t *testing.T) {
	tests := []struct {
		deviceInfo  string
		expectedLUN int32
		expectError bool
	}{
		{
			deviceInfo:  "0",
			expectedLUN: 0,
			expectError: false,
		},
		{
			deviceInfo:  "10",
			expectedLUN: 10,
			expectError: false,
		},
		{
			deviceInfo:  "11d",
			expectedLUN: -1,
			expectError: true,
		},
		{
			deviceInfo:  "999",
			expectedLUN: -1,
			expectError: true,
		},
		{
			deviceInfo:  "",
			expectedLUN: -1,
			expectError: true,
		},
		{
			deviceInfo:  "/dev/disk/azure/scsi1/lun2",
			expectedLUN: 2,
			expectError: false,
		},
		{
			deviceInfo:  "/dev/disk/azure/scsi0/lun12",
			expectedLUN: 12,
			expectError: false,
		},
		{
			deviceInfo:  "/devhost/disk/azure/scsi0/lun13",
			expectedLUN: 13,
			expectError: false,
		},
		{
			deviceInfo:  "/dev/disk/by-id/scsi1/lun2",
			expectedLUN: -1,
			expectError: true,
		},
	}

	for _, test := range tests {
		result, err := GetDiskLUN(test.deviceInfo)
		assert.Equal(t, result, test.expectedLUN)
		assert.Equal(t, err != nil, test.expectError, fmt.Sprintf("error msg: %v", err))
	}
}

func TestGetDiskName(t *testing.T) {
	mDiskPathRE := consts.ManagedDiskPathRE
	tests := []struct {
		options   string
		expected1 string
		expected2 error
	}{
		{
			options:   "testurl/subscriptions/12/resourceGroups/23/providers/Microsoft.Compute/disks/name",
			expected1: "name",
			expected2: nil,
		},
		{
			options:   "testurl/subscriptions/23/providers/Microsoft.Compute/disks/name",
			expected1: "",
			expected2: fmt.Errorf("could not get disk name from testurl/subscriptions/23/providers/Microsoft.Compute/disks/name, correct format: %s", mDiskPathRE),
		},
		{
			options:   "testurl/subscriptions/12/resourcegroups/23/providers/microsoft.compute/disks/name",
			expected1: "name",
			expected2: nil,
		},
		{
			options:   "testurl/subscriPtions/12/Resourcegroups/23/Providers/microsoft.compute/dISKS/name",
			expected1: "name",
			expected2: nil,
		},
		{
			options:   "http://test.com/vhds/name",
			expected1: "",
			expected2: fmt.Errorf("could not get disk name from http://test.com/vhds/name, correct format: %s", mDiskPathRE),
		},
		{
			options:   "http://test.io/name",
			expected1: "",
			expected2: fmt.Errorf("could not get disk name from http://test.io/name, correct format: %s", mDiskPathRE),
		},
	}

	for _, test := range tests {
		result1, result2 := GetDiskName(test.options)
		if !reflect.DeepEqual(result1, test.expected1) || !reflect.DeepEqual(result2, test.expected2) {
			t.Errorf("input: %q, getDiskName result1: %q, expected1: %q, result2: %q, expected2: %q", test.options, result1, test.expected1,
				result2, test.expected2)
		}
	}
}

func TestGetFStype(t *testing.T) {
	tests := []struct {
		options  map[string]string
		expected string
	}{
		{
			nil,
			"",
		},
		{
			map[string]string{},
			"",
		},
		{
			map[string]string{"fstype": ""},
			"",
		},
		{
			map[string]string{"fstype": "xfs"},
			"xfs",
		},
		{
			map[string]string{"FSType": "xfs"},
			"xfs",
		},
		{
			map[string]string{"fstype": "EXT4"},
			"ext4",
		},
	}

	for _, test := range tests {
		result := GetFStype(test.options)
		if result != test.expected {
			t.Errorf("input: %q, GetFStype result: %s, expected: %s", test.options, result, test.expected)
		}
	}
}

func TestGetMaxShares(t *testing.T) {
	tests := []struct {
		options       map[string]string
		expectedValue int
		expectedError error
	}{
		{
			nil,
			1,
			nil,
		},
		{
			map[string]string{},
			1,
			nil,
		},
		{
			map[string]string{consts.MaxSharesField: ""},
			0,
			fmt.Errorf("parse  failed with error: strconv.Atoi: parsing \"\": invalid syntax"),
		},
		{
			map[string]string{consts.MaxSharesField: "-1"},
			0,
			fmt.Errorf("parse -1 returned with invalid value: -1"),
		},
		{
			map[string]string{consts.MaxSharesField: "NAN"},
			0,
			fmt.Errorf("parse NAN failed with error: strconv.Atoi: parsing \"NAN\": invalid syntax"),
		},
		{
			map[string]string{consts.MaxSharesField: "2"},
			2,
			nil,
		},
	}

	for _, test := range tests {
		result, err := GetMaxShares(test.options)
		if result != test.expectedValue {
			t.Errorf("input: %q, GetMaxShates result: %v, expected: %v", test.options, result, test.expectedValue)
		}
		if !reflect.DeepEqual(err, test.expectedError) {
			t.Errorf("input: %q, GetMaxShates error: %v, expected: %v", test.options, err, test.expectedError)
		}
	}
}

func TestGetResourceGroupFromURI(t *testing.T) {
	tests := []struct {
		diskURL        string
		expectedResult string
		expectError    bool
	}{
		{
			diskURL:        "/subscriptions/4be8920b-2978-43d7-axyz-04d8549c1d05/resourceGroups/azure-k8s1102/providers/Microsoft.Compute/disks/andy-mghyb1102-dynamic-pvc-f7f014c9-49f4-11e8-ab5c-000d3af7b38e",
			expectedResult: "azure-k8s1102",
			expectError:    false,
		},
		{
			// case insentive check
			diskURL:        "/subscriptions/4be8920b-2978-43d7-axyz-04d8549c1d05/resourcegroups/azure-k8s1102/providers/Microsoft.Compute/disks/andy-mghyb1102-dynamic-pvc-f7f014c9-49f4-11e8-ab5c-000d3af7b38e",
			expectedResult: "azure-k8s1102",
			expectError:    false,
		},
		{
			diskURL:        "/4be8920b-2978-43d7-axyz-04d8549c1d05/resourceGroups/azure-k8s1102/providers/Microsoft.Compute/disks/andy-mghyb1102-dynamic-pvc-f7f014c9-49f4-11e8-ab5c-000d3af7b38e",
			expectedResult: "",
			expectError:    true,
		},
		{
			diskURL:        "",
			expectedResult: "",
			expectError:    true,
		},
	}

	for _, test := range tests {
		result, err := GetResourceGroupFromURI(test.diskURL)
		assert.Equal(t, result, test.expectedResult, "Expect result not equal with getResourceGroupFromURI(%s) return: %q, expected: %q",
			test.diskURL, result, test.expectedResult)

		if test.expectError {
			assert.NotNil(t, err, "Expect error during getResourceGroupFromURI(%s)", test.diskURL)
		} else {
			assert.Nil(t, err, "Expect error is nil during getResourceGroupFromURI(%s)", test.diskURL)
		}
	}
}

func TestGetSourceVolumeID(t *testing.T) {
	SourceResourceID := "test"

	tests := []struct {
		snapshot *compute.Snapshot
		expected string
	}{
		{
			snapshot: &compute.Snapshot{
				SnapshotProperties: &compute.SnapshotProperties{
					CreationData: &compute.CreationData{
						SourceResourceID: &SourceResourceID,
					},
				},
			},
			expected: "test",
		},
		{
			snapshot: &compute.Snapshot{
				SnapshotProperties: &compute.SnapshotProperties{
					CreationData: &compute.CreationData{},
				},
			},
			expected: "",
		},
		{
			snapshot: &compute.Snapshot{
				SnapshotProperties: &compute.SnapshotProperties{},
			},
			expected: "",
		},
		{
			snapshot: &compute.Snapshot{},
			expected: "",
		},
		{
			snapshot: nil,
			expected: "",
		},
	}

	for _, test := range tests {
		result := GetSourceVolumeID(test.snapshot)
		if !reflect.DeepEqual(result, test.expected) {
			t.Errorf("input: %v, getValidFileShareName result: %q, expected: %q", test.snapshot, result, test.expected)
		}
	}
}

func TestGetValidCreationData(t *testing.T) {
	sourceResourceSnapshotID := "/subscriptions/xxx/resourceGroups/xxx/providers/Microsoft.Compute/snapshots/xxx"
	sourceResourceVolumeID := "/subscriptions/xxx/resourceGroups/xxx/providers/Microsoft.Compute/disks/xxx"

	tests := []struct {
		subscriptionID   string
		resourceGroup    string
		sourceResourceID string
		sourceType       string
		expected1        compute.CreationData
		expected2        error
	}{
		{
			subscriptionID:   "",
			resourceGroup:    "",
			sourceResourceID: "",
			sourceType:       "",
			expected1: compute.CreationData{
				CreateOption: compute.DiskCreateOptionEmpty,
			},
			expected2: nil,
		},
		{
			subscriptionID:   "",
			resourceGroup:    "",
			sourceResourceID: "/subscriptions/xxx/resourceGroups/xxx/providers/Microsoft.Compute/snapshots/xxx",
			sourceType:       consts.SourceSnapshot,
			expected1: compute.CreationData{
				CreateOption:     compute.DiskCreateOptionCopy,
				SourceResourceID: &sourceResourceSnapshotID,
			},
			expected2: nil,
		},
		{
			subscriptionID:   "xxx",
			resourceGroup:    "xxx",
			sourceResourceID: "xxx",
			sourceType:       consts.SourceSnapshot,
			expected1: compute.CreationData{
				CreateOption:     compute.DiskCreateOptionCopy,
				SourceResourceID: &sourceResourceSnapshotID,
			},
			expected2: nil,
		},
		{
			subscriptionID:   "",
			resourceGroup:    "",
			sourceResourceID: "/subscriptions/23/providers/Microsoft.Compute/disks/name",
			sourceType:       consts.SourceSnapshot,
			expected1:        compute.CreationData{},
			expected2:        fmt.Errorf("sourceResourceID(%s) is invalid, correct format: %s", "/subscriptions//resourceGroups//providers/Microsoft.Compute/snapshots//subscriptions/23/providers/Microsoft.Compute/disks/name", diskSnapshotPathRE),
		},
		{
			subscriptionID:   "",
			resourceGroup:    "",
			sourceResourceID: "http://test.com/vhds/name",
			sourceType:       consts.SourceSnapshot,
			expected1:        compute.CreationData{},
			expected2:        fmt.Errorf("sourceResourceID(%s) is invalid, correct format: %s", "/subscriptions//resourceGroups//providers/Microsoft.Compute/snapshots/http://test.com/vhds/name", diskSnapshotPathRE),
		},
		{
			subscriptionID:   "",
			resourceGroup:    "",
			sourceResourceID: "/subscriptions/xxx/snapshots/xxx",
			sourceType:       consts.SourceSnapshot,
			expected1:        compute.CreationData{},
			expected2:        fmt.Errorf("sourceResourceID(%s) is invalid, correct format: %s", "/subscriptions//resourceGroups//providers/Microsoft.Compute/snapshots//subscriptions/xxx/snapshots/xxx", diskSnapshotPathRE),
		},
		{
			subscriptionID:   "",
			resourceGroup:    "",
			sourceResourceID: "/subscriptions/xxx/resourceGroups/xxx/providers/Microsoft.Compute/snapshots/xxx/snapshots/xxx/snapshots/xxx",
			sourceType:       consts.SourceSnapshot,
			expected1:        compute.CreationData{},
			expected2:        fmt.Errorf("sourceResourceID(%s) is invalid, correct format: %s", "/subscriptions/xxx/resourceGroups/xxx/providers/Microsoft.Compute/snapshots/xxx/snapshots/xxx/snapshots/xxx", diskSnapshotPathRE),
		},
		{
			subscriptionID:   "",
			resourceGroup:    "",
			sourceResourceID: "xxx",
			sourceType:       "",
			expected1: compute.CreationData{
				CreateOption: compute.DiskCreateOptionEmpty,
			},
			expected2: nil,
		},
		{
			subscriptionID:   "",
			resourceGroup:    "",
			sourceResourceID: "/subscriptions/xxx/resourceGroups/xxx/providers/Microsoft.Compute/disks/xxx",
			sourceType:       consts.SourceVolume,
			expected1: compute.CreationData{
				CreateOption:     compute.DiskCreateOptionCopy,
				SourceResourceID: &sourceResourceVolumeID,
			},
			expected2: nil,
		},
		{
			subscriptionID:   "xxx",
			resourceGroup:    "xxx",
			sourceResourceID: "xxx",
			sourceType:       consts.SourceVolume,
			expected1: compute.CreationData{
				CreateOption:     compute.DiskCreateOptionCopy,
				SourceResourceID: &sourceResourceVolumeID,
			},
			expected2: nil,
		},
		{
			subscriptionID:   "",
			resourceGroup:    "",
			sourceResourceID: "/subscriptions/xxx/resourceGroups/xxx/providers/Microsoft.Compute/snapshots/xxx",
			sourceType:       consts.SourceVolume,
			expected1:        compute.CreationData{},
			expected2:        fmt.Errorf("sourceResourceID(%s) is invalid, correct format: %s", "/subscriptions//resourceGroups//providers/Microsoft.Compute/disks//subscriptions/xxx/resourceGroups/xxx/providers/Microsoft.Compute/snapshots/xxx", consts.ManagedDiskPathRE),
		},
	}

	for _, test := range tests {
		result, err := GetValidCreationData(test.subscriptionID, test.resourceGroup, test.sourceResourceID, test.sourceType)
		if !reflect.DeepEqual(result, test.expected1) || !reflect.DeepEqual(err, test.expected2) {
			t.Errorf("input sourceResourceID: %v, sourceType: %v, getValidCreationData result: %v, expected1 : %v, err: %v, expected2: %v", test.sourceResourceID, test.sourceType, result, test.expected1, err, test.expected2)
		}
	}
}

func TestIsCorruptedDir(t *testing.T) {
	isCorrupted := IsCorruptedDir("/non-existing-dir")
	assert.False(t, isCorrupted)

	isCorrupted = IsCorruptedDir(os.TempDir())
	assert.False(t, isCorrupted)
}

func TestCreateValidDiskName(t *testing.T) {
	tests := []struct {
		volumeName      string
		expected        string
		expectedIsRegEx bool
	}{
		{
			volumeName: "az",
			expected:   "az",
		},
		{
			volumeName: "09",
			expected:   "09",
		},
		{
			volumeName: "a-z",
			expected:   "a-z",
		},
		{
			volumeName: "AZ",
			expected:   "AZ",
		},
		{
			volumeName: "123456789-123456789-123456789-123456789-123456789.123456789-123456789_1234567890",
			expected:   "123456789-123456789-123456789-123456789-123456789.123456789-123456789_1234567890",
		},
		{
			volumeName: "123456789-123456789-123456789-123456789-123456789.123456789-123456789_1234567890-123456789-123456789-123456789-123456789-123456789.123456789-123456789_1234567890-123456789-123456789-123456789-123456789-123456789.123456789-123456789_1234567890",
			expected:   "123456789-123456789-123456789-123456789-123456789.123456789-123456789_1234567890",
		},
		{
			volumeName:      "",
			expected:        "pvc-disk-dynamic-[[:xdigit:]]{8}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{12}",
			expectedIsRegEx: true,
		},
		{
			volumeName:      "$xyz123",
			expected:        "pvc-disk-dynamic-[[:xdigit:]]{8}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{12}",
			expectedIsRegEx: true,
		},
	}

	for _, test := range tests {
		result := CreateValidDiskName(test.volumeName)
		if !test.expectedIsRegEx {
			assert.Equal(t, test.expected, result)
		} else {
			assert.Regexp(t, test.expected, result)
		}
	}
}

func TestIsARMResourceID(t *testing.T) {
	tests := []struct {
		resourceID   string
		expectResult bool
	}{
		{
			resourceID:   "/subscriptions/b9d2281e/resourceGroups/test-resource/providers/Microsoft.Compute/disks/pvc-disk-dynamic-9e102c53",
			expectResult: true,
		},
		{
			resourceID:   "/Subscriptions/b9d2281e/resourceGroups/test-resource/providers/Microsoft.Compute/disks/pvc-disk-dynamic-9e102c53",
			expectResult: true,
		},
		{
			resourceID:   "resourceGroups/test-resource/providers/Microsoft.Compute/disks/pvc-disk-dynamic-9e102c53",
			expectResult: false,
		},
		{
			resourceID:   "https://test-saccount.blob.core.windows.net/container/pvc-disk-dynamic-9e102c53-593d-11e9-934e-705a0f18a318.vhd",
			expectResult: false,
		},
		{
			resourceID:   "test.com",
			expectResult: false,
		},
		{
			resourceID:   "",
			expectResult: false,
		},
	}

	for _, test := range tests {
		result := IsARMResourceID(test.resourceID)
		if result != test.expectResult {
			t.Errorf("ResourceID: %s, result: %v, expectResult: %v", test.resourceID, result, test.expectResult)
		}
	}
}

func TestIsAvailabilityZone(t *testing.T) {
	region := "eastus"
	tests := []struct {
		desc     string
		zone     string
		expected bool
	}{
		{"empty string should return false", "", false},
		{"wrong farmat should return false", "123", false},
		{"wrong location should return false", "chinanorth-1", false},
		{"correct zone should return true", "eastus-1", true},
	}

	for _, test := range tests {
		actual := IsValidAvailabilityZone(test.zone, region)
		if actual != test.expected {
			t.Errorf("test [%q] get unexpected result: %v != %v", test.desc, actual, test.expected)
		}
	}
}

func TestIsAzureStackCloud(t *testing.T) {
	tests := []struct {
		cloud                  string
		disableAzureStackCloud bool
		expectedResult         bool
	}{
		{
			cloud:                  "AzurePublicCloud",
			disableAzureStackCloud: false,
			expectedResult:         false,
		},
		{
			cloud:                  "",
			disableAzureStackCloud: true,
			expectedResult:         false,
		},
		{
			cloud:                  azureStackCloud,
			disableAzureStackCloud: false,
			expectedResult:         true,
		},
		{
			cloud:                  azureStackCloud,
			disableAzureStackCloud: true,
			expectedResult:         false,
		},
	}

	for i, test := range tests {
		result := IsAzureStackCloud(test.cloud, test.disableAzureStackCloud)
		assert.Equal(t, test.expectedResult, result, "TestCase[%d]", i)
	}
}

func TestIsValidDiskURI(t *testing.T) {
	supportedManagedDiskURI := diskURISupportedManaged

	tests := []struct {
		diskURI     string
		expectError error
	}{
		{
			diskURI:     "/subscriptions/b9d2281e/resourceGroups/test-resource/providers/Microsoft.Compute/disks/pvc-disk-dynamic-9e102c53",
			expectError: nil,
		},
		{
			diskURI:     "/Subscriptions/b9d2281e/resourceGroups/test-resource/providers/Microsoft.Compute/disks/pvc-disk-dynamic-9e102c53",
			expectError: nil,
		},
		{
			diskURI:     "resourceGroups/test-resource/providers/Microsoft.Compute/disks/pvc-disk-dynamic-9e102c53",
			expectError: fmt.Errorf("invalid DiskURI: resourceGroups/test-resource/providers/Microsoft.Compute/disks/pvc-disk-dynamic-9e102c53, correct format: %v", supportedManagedDiskURI),
		},
		{
			diskURI:     "https://test-saccount.blob.core.windows.net/container/pvc-disk-dynamic-9e102c53-593d-11e9-934e-705a0f18a318.vhd",
			expectError: fmt.Errorf("invalid DiskURI: https://test-saccount.blob.core.windows.net/container/pvc-disk-dynamic-9e102c53-593d-11e9-934e-705a0f18a318.vhd, correct format: %v", supportedManagedDiskURI),
		},
		{
			diskURI:     "test.com",
			expectError: fmt.Errorf("invalid DiskURI: test.com, correct format: %v", supportedManagedDiskURI),
		},
		{
			diskURI:     "http://test-saccount.blob.core.windows.net/container/pvc-disk-dynamic-9e102c53-593d-11e9-934e-705a0f18a318.vhd",
			expectError: fmt.Errorf("invalid DiskURI: http://test-saccount.blob.core.windows.net/container/pvc-disk-dynamic-9e102c53-593d-11e9-934e-705a0f18a318.vhd, correct format: %v", supportedManagedDiskURI),
		},
	}

	for _, test := range tests {
		err := IsValidDiskURI(test.diskURI)
		if !reflect.DeepEqual(err, test.expectError) {
			t.Errorf("DiskURI: %q, isValidDiskURI err: %q, expected1: %q", test.diskURI, err, test.expectError)
		}
	}
}

func TestIsValidVolumeCapabilities(t *testing.T) {
	tests := []struct {
		description    string
		volCaps        []*csi.VolumeCapability
		maxShares      int
		expectedResult bool
	}{
		{
			description: "[Success] Returns true for valid mount capabilities",
			volCaps: []*csi.VolumeCapability{
				{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
				},
			},
			maxShares:      1,
			expectedResult: true,
		},
		{
			description: "[Failure] Returns false for unsupported mount access mode",
			volCaps: []*csi.VolumeCapability{
				{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
					},
				},
			},
			maxShares:      2,
			expectedResult: false,
		},
		{
			description: "[Failure] Returns false for invalid mount access mode",
			volCaps: []*csi.VolumeCapability{
				{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: 10,
					},
				},
			},
			maxShares:      1,
			expectedResult: false,
		},
		{
			description: "[Success] Returns true for valid block capabilities",
			volCaps: []*csi.VolumeCapability{
				{
					AccessType: &csi.VolumeCapability_Block{
						Block: &csi.VolumeCapability_BlockVolume{},
					},
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
				},
			},
			maxShares:      1,
			expectedResult: true,
		},
		{
			description: "[Success] Returns true for shared block access mode",
			volCaps: []*csi.VolumeCapability{
				{
					AccessType: &csi.VolumeCapability_Block{
						Block: &csi.VolumeCapability_BlockVolume{},
					},
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
					},
				},
			},
			maxShares:      2,
			expectedResult: true,
		},
		{
			description: "[Failure] Returns false for unsupported mount access mode",
			volCaps: []*csi.VolumeCapability{
				{
					AccessType: &csi.VolumeCapability_Block{
						Block: &csi.VolumeCapability_BlockVolume{},
					},
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
					},
				},
			},
			maxShares:      1,
			expectedResult: false,
		},
		{
			description: "[Failure] Returns false for invalid block access mode",
			volCaps: []*csi.VolumeCapability{
				{
					AccessType: &csi.VolumeCapability_Block{
						Block: &csi.VolumeCapability_BlockVolume{},
					},
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: 10,
					},
				},
			},
			maxShares:      1,
			expectedResult: false,
		},
		{
			description: "[Failure] Returns false for empty volume capability",
			volCaps: []*csi.VolumeCapability{
				{
					AccessType: nil,
					AccessMode: nil,
				},
			},
			maxShares:      1,
			expectedResult: false,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.description, func(t *testing.T) {
			result := IsValidVolumeCapabilities(test.volCaps, test.maxShares)
			assert.Equal(t, test.expectedResult, result)
		})
	}
	var caps []*csi.VolumeCapability
	stdVolCap := csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Mount{
			Mount: &csi.VolumeCapability_MountVolume{},
		},
		AccessMode: &csi.VolumeCapability_AccessMode{
			Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		},
	}
	caps = append(caps, &stdVolCap)
	if !IsValidVolumeCapabilities(caps, 1) {
		t.Errorf("Unexpected error")
	}
	stdVolCap1 := csi.VolumeCapability{
		AccessMode: &csi.VolumeCapability_AccessMode{
			Mode: 10,
		},
	}
	caps = append(caps, &stdVolCap1)
	if IsValidVolumeCapabilities(caps, 1) {
		t.Errorf("Unexpected error")
	}
}

func TestNormalizeCachingMode(t *testing.T) {
	tests := []struct {
		desc          string
		req           v1.AzureDataDiskCachingMode
		expectedErr   error
		expectedValue v1.AzureDataDiskCachingMode
	}{
		{
			desc:          "CachingMode not exist",
			req:           "",
			expectedErr:   nil,
			expectedValue: v1.AzureDataDiskCachingReadOnly,
		},
		{
			desc:          "Not supported CachingMode",
			req:           "WriteOnly",
			expectedErr:   fmt.Errorf("azureDisk - WriteOnly is not supported cachingmode. Supported values are [None ReadOnly ReadWrite]"),
			expectedValue: "",
		},
		{
			desc:          "Valid CachingMode",
			req:           "ReadOnly",
			expectedErr:   nil,
			expectedValue: "ReadOnly",
		},
	}
	for _, test := range tests {
		value, err := NormalizeCachingMode(test.req)
		assert.Equal(t, value, test.expectedValue)
		assert.Equal(t, err, test.expectedErr, fmt.Sprintf("error msg: %v", err))
	}
}

func TestNormalizeNetworkAccessPolicy(t *testing.T) {
	tests := []struct {
		networkAccessPolicy         string
		expectedNetworkAccessPolicy compute.NetworkAccessPolicy
		expectError                 bool
	}{
		{
			networkAccessPolicy:         "",
			expectedNetworkAccessPolicy: compute.NetworkAccessPolicyAllowAll,
			expectError:                 false,
		},
		{
			networkAccessPolicy:         "AllowAll",
			expectedNetworkAccessPolicy: compute.NetworkAccessPolicyAllowAll,
			expectError:                 false,
		},
		{
			networkAccessPolicy:         "DenyAll",
			expectedNetworkAccessPolicy: compute.NetworkAccessPolicyDenyAll,
			expectError:                 false,
		},
		{
			networkAccessPolicy:         "AllowPrivate",
			expectedNetworkAccessPolicy: compute.NetworkAccessPolicyAllowPrivate,
			expectError:                 false,
		},
		{
			networkAccessPolicy:         "allowAll",
			expectedNetworkAccessPolicy: compute.NetworkAccessPolicy(""),
			expectError:                 true,
		},
		{
			networkAccessPolicy:         "invalid",
			expectedNetworkAccessPolicy: compute.NetworkAccessPolicy(""),
			expectError:                 true,
		},
	}

	for _, test := range tests {
		result, err := NormalizeNetworkAccessPolicy(test.networkAccessPolicy)
		assert.Equal(t, result, test.expectedNetworkAccessPolicy)
		assert.Equal(t, err != nil, test.expectError, fmt.Sprintf("error msg: %v", err))
	}
}

func TestNormalizeStorageAccountType(t *testing.T) {
	tests := []struct {
		cloud                  string
		storageAccountType     string
		disableAzureStackCloud bool
		expectedAccountType    compute.DiskStorageAccountTypes
		expectError            bool
	}{
		{
			cloud:                  azurePublicCloud,
			storageAccountType:     "",
			disableAzureStackCloud: false,
			expectedAccountType:    compute.DiskStorageAccountTypesStandardSSDLRS,
			expectError:            false,
		},
		{
			cloud:                  azureStackCloud,
			storageAccountType:     "",
			disableAzureStackCloud: false,
			expectedAccountType:    compute.DiskStorageAccountTypesStandardLRS,
			expectError:            false,
		},
		{
			cloud:                  azurePublicCloud,
			storageAccountType:     "NOT_EXISTING",
			disableAzureStackCloud: false,
			expectedAccountType:    "",
			expectError:            true,
		},
		{
			cloud:                  azurePublicCloud,
			storageAccountType:     "Standard_LRS",
			disableAzureStackCloud: false,
			expectedAccountType:    compute.DiskStorageAccountTypesStandardLRS,
			expectError:            false,
		},
		{
			cloud:                  azurePublicCloud,
			storageAccountType:     "Premium_LRS",
			disableAzureStackCloud: false,
			expectedAccountType:    compute.DiskStorageAccountTypesPremiumLRS,
			expectError:            false,
		},
		{
			cloud:                  azurePublicCloud,
			storageAccountType:     "StandardSSD_LRS",
			disableAzureStackCloud: false,
			expectedAccountType:    compute.DiskStorageAccountTypesStandardSSDLRS,
			expectError:            false,
		},
		{
			cloud:                  azurePublicCloud,
			storageAccountType:     "UltraSSD_LRS",
			disableAzureStackCloud: false,
			expectedAccountType:    compute.DiskStorageAccountTypesUltraSSDLRS,
			expectError:            false,
		},
		{
			cloud:                  azureStackCloud,
			storageAccountType:     "UltraSSD_LRS",
			disableAzureStackCloud: false,
			expectedAccountType:    "",
			expectError:            true,
		},
		{
			cloud:                  azureStackCloud,
			storageAccountType:     "UltraSSD_LRS",
			disableAzureStackCloud: true,
			expectedAccountType:    compute.DiskStorageAccountTypesUltraSSDLRS,
			expectError:            false,
		},
	}

	for _, test := range tests {
		result, err := NormalizeStorageAccountType(test.storageAccountType, test.cloud, test.disableAzureStackCloud)
		assert.Equal(t, result, test.expectedAccountType)
		assert.Equal(t, err != nil, test.expectError, fmt.Sprintf("error msg: %v", err))
	}
}

func TestParseDiskParameters(t *testing.T) {
	testCases := []struct {
		name           string
		inputParams    map[string]string
		expectedOutput ManagedDiskParameters
		expectedError  error
	}{
		{
			name:        "nil disk parameters",
			inputParams: nil,
			expectedOutput: ManagedDiskParameters{
				Incremental:   true,
				Tags:          make(map[string]string),
				VolumeContext: make(map[string]string),
			},
			expectedError: nil,
		},
		{
			name:        "invalid field in parameters",
			inputParams: map[string]string{"invalidField": "someValue"},
			expectedOutput: ManagedDiskParameters{
				Incremental:   true,
				Tags:          make(map[string]string),
				VolumeContext: map[string]string{"invalidField": "someValue"},
			},
			expectedError: fmt.Errorf("invalid parameter %s in storage class", "invalidField"),
		},
		{
			name:        "invalid value in parameters",
			inputParams: map[string]string{consts.LogicalSectorSizeField: "invalidValue"},
			expectedOutput: ManagedDiskParameters{
				Incremental:   true,
				Tags:          make(map[string]string),
				VolumeContext: map[string]string{consts.LogicalSectorSizeField: "invalidValue"},
			},
			expectedError: fmt.Errorf("parse invalidValue failed with error: strconv.Atoi: parsing \"invalidValue\": invalid syntax"),
		},
		{
			name: "valid parameters input",
			inputParams: map[string]string{
				consts.SkuNameField:             "skuName",
				consts.LocationField:            "location",
				consts.CachingModeField:         "cachingMode",
				consts.ResourceGroupField:       "resourceGroup",
				consts.DiskIOPSReadWriteField:   "diskIOPSReadWrite",
				consts.DiskMBPSReadWriteField:   "diskMBPSReadWrite",
				consts.LogicalSectorSizeField:   "1",
				consts.DiskNameField:            "diskName",
				consts.DiskEncryptionSetID:      "diskEncyptionSetID",
				consts.TagsField:                "key0=value0, key1=value1",
				consts.WriteAcceleratorEnabled:  "writeAcceleratorEnabled",
				consts.PvcNameKey:               "pvcName",
				consts.PvcNamespaceKey:          "pvcNamespace",
				consts.PvNameKey:                "pvName",
				consts.FsTypeField:              "fsType",
				consts.KindField:                "ignored",
				consts.MaxSharesField:           "1",
				consts.PerfProfileField:         "None",
				consts.NetworkAccessPolicyField: "networkAccessPolicy",
				consts.DiskAccessIDField:        "diskAccessID",
				consts.EnableBurstingField:      "true",
				consts.UserAgentField:           "userAgent",
				consts.EnableAsyncAttachField:   "enableAsyncAttach",
				consts.IncrementalField:         "false",
				consts.ZonedField:               "ignored",
			},
			expectedOutput: ManagedDiskParameters{
				AccountType:         "skuName",
				Location:            "location",
				CachingMode:         v1.AzureDataDiskCachingMode("cachingMode"),
				ResourceGroup:       "resourceGroup",
				DiskIOPSReadWrite:   "diskIOPSReadWrite",
				DiskMBPSReadWrite:   "diskMBPSReadWrite",
				DiskName:            "diskName",
				DiskEncryptionSetID: "diskEncyptionSetID",
				Incremental:         false,
				Tags: map[string]string{
					consts.PvcNameTag:      "pvcName",
					consts.PvcNamespaceTag: "pvcNamespace",
					consts.PvNameTag:       "pvName",
					"key0":                 "value0",
					"key1":                 "value1",
				},
				WriteAcceleratorEnabled: "writeAcceleratorEnabled",
				FsType:                  "fstype",
				PerfProfile:             "None",
				NetworkAccessPolicy:     "networkAccessPolicy",
				DiskAccessID:            "diskAccessID",
				EnableBursting:          to.BoolPtr(true),
				UserAgent:               "userAgent",
				VolumeContext: map[string]string{
					consts.SkuNameField:             "skuName",
					consts.LocationField:            "location",
					consts.CachingModeField:         "cachingMode",
					consts.ResourceGroupField:       "resourceGroup",
					consts.DiskIOPSReadWriteField:   "diskIOPSReadWrite",
					consts.DiskMBPSReadWriteField:   "diskMBPSReadWrite",
					consts.LogicalSectorSizeField:   "1",
					consts.DiskNameField:            "diskName",
					consts.DiskEncryptionSetID:      "diskEncyptionSetID",
					consts.TagsField:                "key0=value0, key1=value1",
					consts.WriteAcceleratorEnabled:  "writeAcceleratorEnabled",
					consts.PvcNameKey:               "pvcName",
					consts.PvcNamespaceKey:          "pvcNamespace",
					consts.PvNameKey:                "pvName",
					consts.FsTypeField:              "fsType",
					consts.KindField:                string(v1.AzureManagedDisk),
					consts.MaxSharesField:           "1",
					consts.PerfProfileField:         "None",
					consts.NetworkAccessPolicyField: "networkAccessPolicy",
					consts.DiskAccessIDField:        "diskAccessID",
					consts.EnableBurstingField:      "true",
					consts.UserAgentField:           "userAgent",
					consts.EnableAsyncAttachField:   "enableAsyncAttach",
					consts.IncrementalField:         "false",
					consts.ZonedField:               "ignored",
				},
				MaxShares:         1,
				LogicalSectorSize: 1,
			},
			expectedError: nil,
		},
	}
	for _, test := range testCases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			result, err := ParseDiskParameters(test.inputParams)
			require.Equal(t, test.expectedError, err)
			assert.Equal(t, test.expectedOutput, result)
		})
	}
}

func TestPickAvailabilityZone(t *testing.T) {
	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "requirement missing ",
			testFunc: func(t *testing.T) {
				expectedresponse := ""
				region := "test"
				actualresponse := PickAvailabilityZone(nil, region, "N/A")
				if !reflect.DeepEqual(expectedresponse, actualresponse) {
					t.Errorf("actualresponse: (%v), expectedresponse: (%v)", actualresponse, expectedresponse)
				}
			},
		},
		{
			name: "valid get preferred",
			testFunc: func(t *testing.T) {
				expectedresponse := "test-01"
				region := "test"
				mp := make(map[string]string)
				mp["N/A"] = "test-01"
				topology := &csi.Topology{
					Segments: mp,
				}
				topologies := []*csi.Topology{}
				topologies = append(topologies, topology)
				req := &csi.TopologyRequirement{
					Preferred: topologies,
				}
				actualresponse := PickAvailabilityZone(req, region, "N/A")
				if !reflect.DeepEqual(expectedresponse, actualresponse) {
					t.Errorf("actualresponse: (%v), expectedresponse: (%v)", actualresponse, expectedresponse)
				}
			},
		},
		{
			name: "valid get requisite",
			testFunc: func(t *testing.T) {
				expectedresponse := "test-01"
				region := "test"
				mp := make(map[string]string)
				mp["N/A"] = "test-01"
				topology := &csi.Topology{
					Segments: mp,
				}
				topologies := []*csi.Topology{}
				topologies = append(topologies, topology)
				req := &csi.TopologyRequirement{
					Requisite: topologies,
				}
				actualresponse := PickAvailabilityZone(req, region, "N/A")
				if !reflect.DeepEqual(expectedresponse, actualresponse) {
					t.Errorf("actualresponse: (%v), expectedresponse: (%v)", actualresponse, expectedresponse)
				}
			},
		},
		{
			name: "valid get preferred - WellKnownTopologyKey",
			testFunc: func(t *testing.T) {
				expectedresponse := "test-02"
				region := "test"
				mp := make(map[string]string)
				mp["N/A"] = "test-01"
				mp[consts.WellKnownTopologyKey] = "test-02"
				topology := &csi.Topology{
					Segments: mp,
				}
				topologies := []*csi.Topology{}
				topologies = append(topologies, topology)
				req := &csi.TopologyRequirement{
					Preferred: topologies,
				}
				actualresponse := PickAvailabilityZone(req, region, "N/A")
				if !reflect.DeepEqual(expectedresponse, actualresponse) {
					t.Errorf("actualresponse: (%v), expectedresponse: (%v)", actualresponse, expectedresponse)
				}
			},
		},
		{
			name: "valid get requisite - WellKnownTopologyKey",
			testFunc: func(t *testing.T) {
				expectedresponse := "test-02"
				region := "test"
				mp := make(map[string]string)
				mp["N/A"] = "test-01"
				mp[consts.WellKnownTopologyKey] = "test-02"
				topology := &csi.Topology{
					Segments: mp,
				}
				topologies := []*csi.Topology{}
				topologies = append(topologies, topology)
				req := &csi.TopologyRequirement{
					Requisite: topologies,
				}
				actualresponse := PickAvailabilityZone(req, region, "N/A")
				if !reflect.DeepEqual(expectedresponse, actualresponse) {
					t.Errorf("actualresponse: (%v), expectedresponse: (%v)", actualresponse, expectedresponse)
				}
			},
		},
		{
			name: "empty request ",
			testFunc: func(t *testing.T) {
				req := &csi.TopologyRequirement{}
				expectedresponse := ""
				region := "test"
				actualresponse := PickAvailabilityZone(req, region, "N/A")
				if !reflect.DeepEqual(expectedresponse, actualresponse) {
					t.Errorf("actualresponse: (%v), expectedresponse: (%v)", actualresponse, expectedresponse)
				}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func createTestFile(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	return nil
}

func skipIfTestingOnWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping tests on Windows")
	}
}

func TestInsertDiskProperties(t *testing.T) {
	tests := []struct {
		desc        string
		disk        *compute.Disk
		inputMap    map[string]string
		expectedMap map[string]string
	}{
		{
			desc: "nil pointer",
		},
		{
			desc:        "empty",
			disk:        &compute.Disk{},
			inputMap:    map[string]string{},
			expectedMap: map[string]string{},
		},
		{
			desc: "skuName",
			disk: &compute.Disk{
				Sku: &compute.DiskSku{Name: compute.DiskStorageAccountTypesPremiumLRS},
			},
			inputMap:    map[string]string{},
			expectedMap: map[string]string{"skuname": string(compute.DiskStorageAccountTypesPremiumLRS)},
		},
		{
			desc: "DiskProperties",
			disk: &compute.Disk{
				Sku: &compute.DiskSku{Name: compute.DiskStorageAccountTypesStandardSSDLRS},
				DiskProperties: &compute.DiskProperties{
					NetworkAccessPolicy: compute.NetworkAccessPolicyAllowPrivate,
					DiskIOPSReadWrite:   to.Int64Ptr(6400),
					DiskMBpsReadWrite:   to.Int64Ptr(100),
					CreationData: &compute.CreationData{
						LogicalSectorSize: to.Int32Ptr(512),
					},
					Encryption: &compute.Encryption{DiskEncryptionSetID: to.StringPtr("/subs/DiskEncryptionSetID")},
					MaxShares:  to.Int32Ptr(3),
				},
			},
			inputMap: map[string]string{},
			expectedMap: map[string]string{
				consts.SkuNameField:             string(compute.DiskStorageAccountTypesStandardSSDLRS),
				consts.NetworkAccessPolicyField: string(compute.NetworkAccessPolicyAllowPrivate),
				consts.DiskIOPSReadWriteField:   "6400",
				consts.DiskMBPSReadWriteField:   "100",
				consts.LogicalSectorSizeField:   "512",
				consts.DesIDField:               "/subs/DiskEncryptionSetID",
				consts.MaxSharesField:           "3",
			},
		},
	}

	for _, test := range tests {
		InsertDiskProperties(test.disk, test.inputMap)
		for k, v := range test.inputMap {
			if test.expectedMap[k] != v {
				t.Errorf("test [%q] get unexpected result: (%v, %v) != (%v, %v)", test.desc, k, v, k, test.expectedMap[k])
			}
		}
	}
}

func TestSleepIfThrottled(t *testing.T) {
	const sleepDuration = 1 * time.Second

	tests := []struct {
		description           string
		err                   error
		expectedSleepDuration time.Duration
	}{
		{
			description: "No sleep",
			err:         errors.New("do not sleep"),
		},
		{
			description:           "Too many requests, sleep 100ms",
			err:                   errors.New(consts.TooManyRequests),
			expectedSleepDuration: sleepDuration,
		},
		{
			description:           "Client throttled, sleep 100ms",
			err:                   errors.New(consts.ClientThrottled),
			expectedSleepDuration: sleepDuration,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.description, func(t *testing.T) {
			start := time.Now()
			SleepIfThrottled(test.err, int(sleepDuration.Seconds()))
			actualSleepDuration := time.Since(start)
			if test.expectedSleepDuration == 0 {
				assert.Less(t, actualSleepDuration, sleepDuration)
			} else {
				assert.GreaterOrEqual(t, actualSleepDuration, test.expectedSleepDuration)
			}
		})
	}
}
