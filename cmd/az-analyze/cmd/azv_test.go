/*
Copyright 2022 The Kubernetes Authors.

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

package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetAzVolumesByPod(t *testing.T) {
	fakeClientsetK8s := NewTestK8sClientset()
	fakeClientsetAzDisk := NewTestAzDiskClientset()

	tests := []struct {
		description string
		verifyFunc  func()
	}{
		{
			description: "specified pod name with more than one pv",
			verifyFunc: func() {
				result := GetAzVolumesByPod(fakeClientsetK8s, fakeClientsetAzDisk, "test-pod-0", "default")
				expect := []AzvResource{azvResource1, azvResource2}

				for i := 0; i < len(result); i++ {
					require.Equal(t, result[i], expect[i])
				}
			},
		},
		{
			description: "empty pod name with multiple pods have same pvc",
			verifyFunc: func() {
				result := GetAzVolumesByPod(fakeClientsetK8s, fakeClientsetAzDisk, "", "default")
				expect := []AzvResource{azvResource1, azvResource3, azvResource2}

				for i := 0; i < len(result); i++ {
					require.Equal(t, result[i], expect[i])
				}
			},
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			tt.verifyFunc()
		})
	}
}
