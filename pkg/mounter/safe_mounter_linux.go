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
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
	"k8s.io/mount-utils"
)

var doMount = unix.Mount

// RemountReadOnly attempts to remount the given path as read-only for all associated mount namespaces.
// If remount read-only is disabled or not applicable for the given path, it does nothing and returns nil.
func RemountReadOnly(path string, m *mount.SafeFormatAndMount) error {
	if toInterfaceWithOpts(m).disableReadOnlyRemount {
		return nil
	}
	err := doMount("", path, "", unix.MS_REMOUNT|unix.MS_RDONLY, "")
	switch {
	case err == nil, errors.Is(err, os.ErrNotExist), errors.Is(err, unix.EROFS), errors.Is(err, unix.EINVAL):
		// ErrNotExist means the mountpoint was fully cleaned up
		// EROFS means the mountpoint was already read-only
		// EINVAL means any of the following:
		//    1. the path wasn't the root of a mountpoint (e.g. mountpoint only partially cleaned up)
		//    2. the mountpoint actually belonged to another mount namespace
		// or 3. a kernel-blacklisted flag like MS_NOUSER was passed (we don't pass any)
		return nil
	default:
		return fmt.Errorf("failed to make %s read-only: %w", path, err)
	}
}
