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

package azuredisk

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"testing"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2022-03-01/compute"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/status"
	clientset "k8s.io/client-go/kubernetes"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/cloud-provider-azure/pkg/azureclients/diskclient/mockdiskclient"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

func TestNewDriverV1(t *testing.T) {
	d := newDriverV1(&DriverOptions{
		NodeID:                 os.Getenv("nodeid"),
		DriverName:             consts.DefaultDriverName,
		VolumeAttachLimit:      16,
		EnablePerfOptimization: false,
	})
	assert.NotNil(t, d)
}

func TestCheckDiskCapacity(t *testing.T) {
	d, _ := NewFakeDriver(t)
	size := int32(10)
	diskName := "unit-test"
	resourceGroup := "unit-test"
	disk := compute.Disk{
		DiskProperties: &compute.DiskProperties{
			DiskSizeGB: &size,
		},
	}
	d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
	flag, err := d.checkDiskCapacity(context.TODO(), "", resourceGroup, diskName, 10)
	assert.Equal(t, flag, true)
	assert.Nil(t, err)

	flag, err = d.checkDiskCapacity(context.TODO(), "", resourceGroup, diskName, 11)
	assert.Equal(t, flag, false)
	expectedErr := status.Errorf(6, "the request volume already exists, but its capacity(10) is different from (11)")
	assert.Equal(t, err, expectedErr)
}

func TestRun(t *testing.T) {
	fakeCredFile := "fake-cred-file.json"
	fakeCredContent := `{
    "tenantId": "1234",
    "subscriptionId": "12345",
    "aadClientId": "123456",
    "aadClientSecret": "1234567",
    "resourceGroup": "rg1",
    "location": "loc"
}`

	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "Successful run",
			testFunc: func(t *testing.T) {
				if err := ioutil.WriteFile(fakeCredFile, []byte(fakeCredContent), 0666); err != nil {
					t.Error(err)
				}

				defer func() {
					if err := os.Remove(fakeCredFile); err != nil {
						t.Error(err)
					}
				}()

				t.Setenv(consts.DefaultAzureCredentialFileEnv, fakeCredFile)

				d, _ := NewFakeDriver(t)
				d.Run("tcp://127.0.0.1:0", "", true, true)
			},
		},
		{
			name: "Successful run with node ID missing",
			testFunc: func(t *testing.T) {
				if err := ioutil.WriteFile(fakeCredFile, []byte(fakeCredContent), 0666); err != nil {
					t.Error(err)
				}

				defer func() {
					if err := os.Remove(fakeCredFile); err != nil {
						t.Error(err)
					}
				}()

				t.Setenv(consts.DefaultAzureCredentialFileEnv, fakeCredFile)

				d, _ := NewFakeDriver(t)
				d.setCloud(&azure.Cloud{})
				d.setNodeID("")
				d.Run("tcp://127.0.0.1:0", "", true, true)
			},
		},
		{
			name: "Successful run with vmss VMType",
			testFunc: func(t *testing.T) {
				if err := ioutil.WriteFile(fakeCredFile, []byte(fakeCredContent), 0666); err != nil {
					t.Error(err)
				}

				defer func() {
					if err := os.Remove(fakeCredFile); err != nil {
						t.Error(err)
					}
				}()

				t.Setenv(consts.DefaultAzureCredentialFileEnv, fakeCredFile)

				d := newDriverV1(&DriverOptions{
					NodeID:                 "",
					DriverName:             consts.DefaultDriverName,
					EnableListVolumes:      true,
					EnableListSnapshots:    true,
					EnablePerfOptimization: true,
					VMSSCacheTTLInSeconds:  10,
					VMType:                 "vmss",
				})
				d.Run("tcp://127.0.0.1:0", "", true, true)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func TestDriver_checkDiskExists(t *testing.T) {
	d, _ := NewFakeDriver(t)
	_, err := d.checkDiskExists(context.TODO(), "testurl/subscriptions/12/providers/Microsoft.Compute/disks/name")
	assert.NotEqual(t, err, nil)
}

func TestGetNodeInfoFromLabels(t *testing.T) {
	tests := []struct {
		nodeName      string
		kubeClient    clientset.Interface
		expectedError error
	}{
		{
			nodeName:      "",
			kubeClient:    nil,
			expectedError: fmt.Errorf("kubeClient is nil"),
		},
	}

	for _, test := range tests {
		_, _, err := getNodeInfoFromLabels(context.TODO(), test.nodeName, test.kubeClient)
		if !reflect.DeepEqual(err, test.expectedError) {
			t.Errorf("Unexpected result: %v, expected result: %v", err, test.expectedError)
		}
	}
}

func TestGetDefaultDiskIOPSReadWrite(t *testing.T) {
	tests := []struct {
		requestGiB int
		expected   int
	}{
		{
			requestGiB: 1,
			expected:   500,
		},
		{
			requestGiB: 512,
			expected:   512,
		},
		{
			requestGiB: 51200000,
			expected:   160000,
		},
	}

	for _, test := range tests {
		result := getDefaultDiskIOPSReadWrite(test.requestGiB)
		if result != test.expected {
			t.Errorf("Unexpected result: %v, expected result: %v, input: %d", result, test.expected, test.requestGiB)
		}
	}
}

func TestGetDefaultDiskMBPSReadWrite(t *testing.T) {
	tests := []struct {
		requestGiB int
		expected   int
	}{
		{
			requestGiB: 1,
			expected:   100,
		},
		{
			requestGiB: 512,
			expected:   100,
		},
		{
			requestGiB: 51200,
			expected:   200,
		},
		{
			requestGiB: 51200000,
			expected:   625,
		},
		{
			requestGiB: 512000000,
			expected:   625,
		},
		{
			requestGiB: 65535,
			expected:   256,
		},
	}

	for _, test := range tests {
		result := getDefaultDiskMBPSReadWrite(test.requestGiB)
		if result != test.expected {
			t.Errorf("Unexpected result: %v, expected result: %v, input: %d", result, test.expected, test.requestGiB)
		}
	}
}
