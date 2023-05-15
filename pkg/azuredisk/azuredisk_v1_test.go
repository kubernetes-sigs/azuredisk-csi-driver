//go:build !azurediskv2
// +build !azurediskv2

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
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2022-08-01/compute"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/cloud-provider-azure/pkg/azureclients/diskclient/mockdiskclient"
)

func TestCheckDiskCapacity_V1(t *testing.T) {
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

	d.setDiskThrottlingCache(consts.ThrottlingKey, "")
	flag, _ := d.checkDiskCapacity(context.TODO(), "", resourceGroup, diskName, 11)
	assert.Equal(t, flag, true)
}

func TestDriver_checkDiskExists_V1(t *testing.T) {
	d, _ := NewFakeDriver(t)
	d.setDiskThrottlingCache(consts.ThrottlingKey, "")
	_, err := d.checkDiskExists(context.TODO(), "testurl/subscriptions/12/resourceGroups/23/providers/Microsoft.Compute/disks/name")
	assert.Equal(t, err, nil)
}
