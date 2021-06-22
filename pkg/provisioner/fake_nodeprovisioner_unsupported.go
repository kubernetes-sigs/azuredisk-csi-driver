// +build !linux

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
	"k8s.io/klog/v2"
)

// Resize resizes the filesystem of the specified volume.
func (fake *FakeNodeProvisioner) Resize(source, target string) error {
	klog.Warningln("Resize not supported on this build")

	// Simulate the exec of "blkid" performed by the Linux implementation in order to keep unit test happy on
	// unsupported platforms.
	args := []string{"-p", "-s", "TYPE", "-s", "PTTYPE", "-o", "export", source}
	_, err := fake.mounter.Exec.Command("blkid", args...).CombinedOutput()

	return err
}
