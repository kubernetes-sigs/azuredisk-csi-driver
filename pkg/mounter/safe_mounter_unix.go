//go:build linux || darwin
// +build linux darwin

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

package mounter

import (
	"time"

	"k8s.io/mount-utils"
	utilexec "k8s.io/utils/exec"
)

func NewSafeMounter(_, _, _ bool, maxConcurrentFormat int, concurrentFormatTimeout time.Duration) (*mount.SafeFormatAndMount, error) {
	opt := mount.WithMaxConcurrentFormat(maxConcurrentFormat, concurrentFormatTimeout)
	return mount.NewSafeFormatAndMount(mount.New(""), utilexec.New(), opt), nil
}

type interfaceWithOpts struct {
	mount.Interface
	disableReadOnlyRemount bool
}

func toInterfaceWithOpts(m *mount.SafeFormatAndMount) interfaceWithOpts {
	if iface, ok := m.Interface.(interfaceWithOpts); ok {
		return iface
	}
	return interfaceWithOpts{Interface: m.Interface}
}

// DisableReadOnlyRemount stops the mounter's file systems from being remounted as read-only before they're unmounted.
func DisableReadOnlyRemount(m *mount.SafeFormatAndMount) {
	iface := toInterfaceWithOpts(m)
	iface.disableReadOnlyRemount = true
	m.Interface = iface
}
