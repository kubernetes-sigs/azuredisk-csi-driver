//go:build linux
// +build linux

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

package mounter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/sys/unix"
	"k8s.io/mount-utils"
)

func TestRemountReadOnly(t *testing.T) {
	const path = "/var/lib/kubelet/plugins/kubernetes.io/csi/pv/globalmount"
	type mountCall struct {
		source, target, fstype string
		flags                  uintptr
		data                   string
	}
	remount := mountCall{target: path, flags: unix.MS_REMOUNT | unix.MS_RDONLY}
	tests := []struct {
		name      string
		disable   bool
		mountErr  error
		wantCalls []mountCall
		wantErr   error
	}{
		{
			name:      "remounts the file system read-only",
			wantCalls: []mountCall{remount},
		},
		{
			name:      "path that no longer exists is not an error",
			mountErr:  unix.ENOENT,
			wantCalls: []mountCall{remount},
		},
		{
			name:      "file system that's already read-only is not an error",
			mountErr:  unix.EROFS,
			wantCalls: []mountCall{remount},
		},
		{
			name:      "path that isn't a mount point is not an error",
			mountErr:  unix.EINVAL,
			wantCalls: []mountCall{remount},
		},
		{
			name:      "other failures are errors",
			mountErr:  unix.EBUSY,
			wantCalls: []mountCall{remount},
			wantErr:   unix.EBUSY,
		},
		{
			name:    "disabled remount read-only does nothing",
			disable: true,
		},
	}
	origMount := doMount
	t.Cleanup(func() { doMount = origMount })
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls []mountCall
			doMount = func(source, target, fstype string, flags uintptr, data string) error {
				calls = append(calls, mountCall{source, target, fstype, flags, data})
				return test.mountErr
			}
			m := &mount.SafeFormatAndMount{Interface: mount.NewFakeMounter(nil)}
			if test.disable {
				DisableReadOnlyRemount(m)
			}

			err := RemountReadOnly(path, m)
			if test.wantErr != nil {
				assert.ErrorIs(t, err, test.wantErr)
				assert.ErrorContains(t, err, path)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, test.wantCalls, calls)
		})
	}
}
