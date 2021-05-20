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

package azuredisk

import (
	"fmt"
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_populateSkuMap(t *testing.T) {
	skuName := "Standard_DS14"
	zone := "1"
	region := "eastus"
	nodeInfo := &NodeInfo{skuName: &skuName, zone: &zone, region: &region}
	tests := []struct {
		name      string
		driver    *Driver
		wantErr   bool
		setUpFn   SetUpFn
		cleanUpFn CleanUpFn
	}{
		{
			name:      "Invalid skus file path",
			driver:    &Driver{DriverCore: DriverCore{nodeInfo: nodeInfo}},
			wantErr:   true,
			setUpFn:   nil,
			cleanUpFn: nil,
		},
		{
			name:      "Invalid json in skus file",
			driver:    &Driver{DriverCore: DriverCore{nodeInfo: nodeInfo}},
			wantErr:   true,
			setUpFn:   createInValidSkuFile,
			cleanUpFn: deleteSkuFile,
		},
		{
			name:      "Valid json in skus file",
			driver:    &Driver{DriverCore: DriverCore{nodeInfo: nodeInfo}},
			wantErr:   false,
			setUpFn:   createValidSkuFile,
			cleanUpFn: deleteSkuFile,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempFile, err := ioutil.TempFile("", "skusTemp.json")
			assert.NoError(t, err)
			tt.driver.setSkusFilePath(tempFile.Name())
			if tt.cleanUpFn != nil {
				defer tt.cleanUpFn(tt.driver.skusFilePath)
			}
			if tt.setUpFn != nil {
				assert.NoError(t, tt.setUpFn(tt.driver.skusFilePath))
			}

			if err = populateSkuMap(tt.driver.DriverCore); (err != nil) != tt.wantErr {
				t.Errorf("populateSkuMap() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

type SetUpFn func(string) error
type CleanUpFn func(string)

const skuString string = `[
	{
	 "resourceType": "virtualmachines",
	 "name": "Standard_DS14",
	 "tier": "Standard",
	 "size": "DS14",
	 "capabilities": [
	  {
	   "name": "MaxResourceVolumeMB",
	   "value": "229376"
	  },
	  {
	   "name": "OSVhdSizeMB",
	   "value": "1047552"
	  },
	  {
	   "name": "vCPUs",
	   "value": "16"
	  },
	  {
	   "name": "HyperVGenerations",
	   "value": "V1,V2"
	  },
	  {
	   "name": "MemoryGB",
	   "value": "112"
	  },
	  {
	   "name": "MaxDataDiskCount",
	   "value": "64"
	  },
	  {
	   "name": "LowPriorityCapable",
	   "value": "True"
	  },
	  {
	   "name": "PremiumIO",
	   "value": "True"
	  },
	  {
	   "name": "VMDeploymentTypes",
	   "value": "IaaS"
	  },
	  {
	   "name": "vCPUsAvailable",
	   "value": "8"
	  },
	  {
	   "name": "ACUs",
	   "value": "160"
	  },
	  {
	   "name": "vCPUsPerCore",
	   "value": "1"
	  },
	  {
	   "name": "CombinedTempDiskAndCachedIOPS",
	   "value": "64000"
	  },
	  {
	   "name": "CombinedTempDiskAndCachedReadBytesPerSecond",
	   "value": "536870912"
	  },
	  {
	   "name": "CombinedTempDiskAndCachedWriteBytesPerSecond",
	   "value": "536870912"
	  },
	  {
	   "name": "CachedDiskBytes",
	   "value": "618475290624"
	  },
	  {
	   "name": "UncachedDiskIOPS",
	   "value": "51200"
	  },
	  {
	   "name": "UncachedDiskBytesPerSecond",
	   "value": "536870912"
	  },
	  {
	   "name": "EphemeralOSDiskSupported",
	   "value": "True"
	  },
	  {
	   "name": "EncryptionAtHostSupported",
	   "value": "True"
	  },
	  {
	   "name": "CapacityReservationSupported",
	   "value": "False"
	  },
	  {
	   "name": "AcceleratedNetworkingEnabled",
	   "value": "False"
	  },
	  {
	   "name": "RdmaEnabled",
	   "value": "False"
	  },
	  {
	   "name": "MaxNetworkInterfaces",
	   "value": "8"
	  }
	 ]
	},
	{
	 "resourceType": "disks",
	 "name": "Premium_LRS",
	 "tier": "Premium",
	 "size": "P50",
	 "capabilities": [
	  {
	   "name": "MaxSizeGiB",
	   "value": "4096"
	  },
	  {
	   "name": "MinSizeGiB",
	   "value": "2048"
	  },
	  {
	   "name": "MaxIOps",
	   "value": "7500"
	  },
	  {
	   "name": "MinIOps",
	   "value": "7500"
	  },
	  {
	   "name": "MaxBandwidthMBps",
	   "value": "250"
	  },
	  {
	   "name": "MinBandwidthMBps",
	   "value": "250"
	  },
	  {
	   "name": "MaxValueOfMaxShares",
	   "value": "5"
	  }
	 ]
	}
   ]`

func createFile(filePath string, content string) error {
	fmt.Println("creating: " + filePath)

	data := []byte(content)

	err := ioutil.WriteFile(filePath, data, 0)

	if err != nil {
		return err
	}

	fmt.Println("done")

	return nil
}

func createValidSkuFile(filePath string) error {
	return createFile(filePath, skuString)
}

func createInValidSkuFile(filePath string) error {
	return createFile(filePath, "gibberish")
}

func deleteSkuFile(filePath string) {
	os.Remove(filePath)
}
