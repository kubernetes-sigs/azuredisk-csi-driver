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

	directvolume "github.com/kata-containers/kata-containers/src/runtime/pkg/direct-volume"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestNewFakeDriver(t *testing.T) {
	cntl := gomock.NewController(t)
	defer cntl.Finish()
	d, err := NewFakeDriver(cntl)
	assert.NotNil(t, d)
	assert.Nil(t, err)
}

func TestFakeDirectVolume(t *testing.T) {
	dv := newFakeKataDirectVolume()

	require.NoError(t, dv.AddMountInfo("target", directvolume.MountInfo{Device: "/dev/sda"}))
	mounted, err := dv.IsVolumeMounted("target")
	require.NoError(t, err)
	assert.True(t, mounted)

	require.NoError(t, dv.Remove("target"))
	mounted, err = dv.IsVolumeMounted("target")
	require.NoError(t, err)
	assert.False(t, mounted)
}
