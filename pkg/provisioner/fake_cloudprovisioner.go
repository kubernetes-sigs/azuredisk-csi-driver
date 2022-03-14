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

package provisioner

import (
	"time"

	"github.com/golang/mock/gomock"
	azcache "sigs.k8s.io/cloud-provider-azure/pkg/cache"
	"sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

type FakeCloudProvisioner struct {
	CloudProvisioner
}

func NewFakeCloudProvisioner(ctrl *gomock.Controller) (*FakeCloudProvisioner, error) {
	fakeCloud := provider.GetTestCloud(ctrl)

	cache, err := azcache.NewTimedcache(time.Minute, func(key string) (interface{}, error) {
		return nil, nil
	})
	if err != nil {
		return nil, err
	}

	return &FakeCloudProvisioner{
		CloudProvisioner: CloudProvisioner{cloud: fakeCloud, getDiskThrottlingCache: cache},
	}, nil
}

func (fake *FakeCloudProvisioner) GetCloud() *provider.Cloud {
	return fake.cloud
}

func (fake *FakeCloudProvisioner) SetCloud(cloud *provider.Cloud) {
	fake.cloud = cloud
}

func (fake *FakeCloudProvisioner) GetPerfOptimizationEnabled() bool {
	return fake.perfOptimizationEnabled
}

func (fake *FakeCloudProvisioner) SetPerfOptimizationEnabled(enabled bool) {
	fake.perfOptimizationEnabled = enabled
}
