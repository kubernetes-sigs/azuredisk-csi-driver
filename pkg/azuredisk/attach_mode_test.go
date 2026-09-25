/*
Copyright 2026 The Kubernetes Authors.

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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

func TestGetAttachMode(t *testing.T) {
	tests := []struct {
		name       string
		parameters map[string]string
		want       string
		wantErr    bool
	}{
		{name: "default", want: consts.AttachModeControllerDriven},
		{name: "controller driven", parameters: map[string]string{"attachMode": "controllerdriven"}, want: consts.AttachModeControllerDriven},
		{name: "node driven", parameters: map[string]string{"ATTACHMODE": "NodeDriven"}, want: consts.AttachModeNodeDriven},
		{name: "unsupported mode", parameters: map[string]string{"attachMode": "automatic"}, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := getAttachMode(test.parameters)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestNodeDrivenAttachDetachFeatureGate(t *testing.T) {
	gate := NewDriverFeatureGate()
	assert.False(t, gate.Enabled(NodeDrivenAttachDetach))
	require.NoError(t, gate.Set("NodeDrivenAttachDetach=true"))
	assert.True(t, gate.Enabled(NodeDrivenAttachDetach))
	assert.Error(t, gate.Set("UnknownAzureDiskFeature=true"))
}
