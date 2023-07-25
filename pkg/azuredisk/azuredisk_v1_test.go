// go:build !azurediskv2
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
	"net/http"
	"testing"

	armcompute "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	azfake "github.com/Azure/azure-sdk-for-go/sdk/azcore/fake"
	"github.com/stretchr/testify/assert"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

func TestCheckDiskCapacity_V1(t *testing.T) {
	d, _ := NewFakeDriver(t)
	size := int32(10)
	diskName := "unit-test"
	resourceGroup := "unit-test"
	subID := "unit-test"
	disk := armcompute.Disk{
		Properties: &armcompute.DiskProperties{
			DiskSizeGB: &size,
		},
	}

	fget := func(ctx context.Context, resourceGroupName string, diskName string, options *armcompute.DisksClientGetOptions) (resp azfake.Responder[armcompute.DisksClientGetResponse], errResp azfake.ErrorResponder) {
		resp.SetResponse(http.StatusOK, armcompute.DisksClientGetResponse{
			Disk:	disk,
		}, nil)
		errResp.SetError(nil)
		return resp, errResp
	}

	client := d.getCloud().CreateDisksClientWithFunction(d.getCloud().SubscriptionID, fget, nil, nil, nil, nil)
	client.Get(nil, "", "", nil)


	flag, err := d.checkDiskCapacity(context.TODO(), subID, resourceGroup, diskName, 10)

	assert.Equal(t, flag, true)
	assert.NoError(t, err)

	flag, err = d.checkDiskCapacity(context.TODO(), subID, resourceGroup, diskName, 11)
	assert.Equal(t, flag, false)
	expectedErr := status.Errorf(codes.AlreadyExists, "the request volume already exists, but its capacity(10) is different from (11)")
	assert.Equal(t, err, expectedErr)
}

func TestDriver_checkDiskExists_V1(t *testing.T) {
	d, _ := NewFakeDriver(t)
	d.setDiskThrottlingCache(consts.ThrottlingKey, "")
	_, err := d.checkDiskExists(context.TODO(), "testurl/subscriptions/12/resourceGroups/23/providers/Microsoft.Compute/disks/name")
	assert.Equal(t, err, nil)
}
