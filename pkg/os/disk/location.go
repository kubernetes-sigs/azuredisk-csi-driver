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

package disk

import (
	"strings"

	"k8s.io/klog/v2"
)

// Location definition
type Location struct {
	Adapter string
	Bus     string
	Target  string
	LUNID   string
}

// parseDiskLocation parses a Windows disk Location string, e.g.
// "PCI Slot 3 : Adapter 1 : Port 0 : Target 0 : LUN 2", into a Location.
// It returns false if the string has no location fields, or if the disk is on
// adapter 0, which hosts the OS and temp disks rather than Azure data disks.
// Adapter 0 disks must be excluded because their LUN numbers overlap with data
// disk LUNs (e.g. the OS disk reports LUN 0).
func parseDiskLocation(diskNum uint32, location string) (Location, bool) {
	var d Location
	s := strings.Split(location, ":")
	if len(s) < 5 {
		return d, false
	}

	found := false
	for _, item := range s {
		itemSplit := strings.Split(strings.TrimSpace(item), " ")
		if len(itemSplit) != 2 {
			continue
		}
		found = true
		key, value := strings.TrimSpace(itemSplit[0]), strings.TrimSpace(itemSplit[1])
		switch key {
		case "Adapter":
			d.Adapter = value
		case "Target":
			d.Target = value
		case "LUN":
			d.LUNID = value
		default:
			klog.V(6).Infof("Got unknown field : %s=%s", key, value)
		}
	}

	if !found {
		return d, false
	}
	if d.Adapter == "0" {
		klog.V(2).Infof("skipping adapter 0 disk, number: %d, location: %s", diskNum, location)
		return d, false
	}
	return d, true
}
