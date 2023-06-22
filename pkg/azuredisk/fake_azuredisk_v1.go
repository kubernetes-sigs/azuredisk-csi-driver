// go:build !azurediskv2
//go:build !azurediskv2
// +build !azurediskv2

/*
Copyright 2020 The Kubernetes Authors.

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
	"testing"

	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
)

// NewFakeDriver returns a driver implementation suitable for use in unit tests.
func NewFakeDriver(t *testing.T) (FakeDriver, error) {
	return newFakeDriverV1(t, newFakeDriverConfig())
}

// NewFakeDriverWithConfig returns a driver implementation with custom configuration suitable for use in unit tests.
func NewFakeDriverWithConfig(t *testing.T, config *azdiskv1beta2.AzDiskDriverConfiguration) (FakeDriver, error) {
	return newFakeDriverV1(t, config)
}

func skipIfTestingDriverV2(t *testing.T) {

}

func isTestingDriverV2() bool {
	return false
}
