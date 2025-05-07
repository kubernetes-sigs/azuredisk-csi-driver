/*
Copyright 2019 The Kubernetes Authors.

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

package util

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/util/sets"
)

const (
	GiB                        = 1024 * 1024 * 1024
	TagKeyValueDelimiter       = "="
	MaximumDataDiskExceededMsg = "attached to a VM of this size is"
)

// IsWindowsOS decides whether the driver is running on windows OS.
func IsWindowsOS() bool {
	return strings.EqualFold(runtime.GOOS, "windows")
}

// IsLinuxOS decides whether the driver is running on linux OS.
func IsLinuxOS() bool {
	return strings.EqualFold(runtime.GOOS, "linux")
}

// RoundUpBytes rounds up the volume size in bytes up to multiplications of GiB
// in the unit of Bytes
func RoundUpBytes(volumeSizeBytes int64) int64 {
	return RoundUpSize(volumeSizeBytes, GiB) * GiB
}

// RoundUpGiB rounds up the volume size in bytes up to multiplications of GiB
// in the unit of GiB
func RoundUpGiB(volumeSizeBytes int64) int64 {
	return RoundUpSize(volumeSizeBytes, GiB)
}

// BytesToGiB conversts Bytes to GiB
func BytesToGiB(volumeSizeBytes int64) int64 {
	return volumeSizeBytes / GiB
}

// GiBToBytes converts GiB to Bytes
func GiBToBytes(volumeSizeGiB int64) int64 {
	return volumeSizeGiB * GiB
}

// RoundUpSize calculates how many allocation units are needed to accommodate
// a volume of given size. E.g. when user wants 1500MiB volume, while AWS EBS
// allocates volumes in gibibyte-sized chunks,
// RoundUpSize(1500 * 1024*1024, 1024*1024*1024) returns '2'
// (2 GiB is the smallest allocatable volume that can hold 1500MiB)
func RoundUpSize(volumeSizeBytes int64, allocationUnitBytes int64) int64 {
	roundedUp := volumeSizeBytes / allocationUnitBytes
	if volumeSizeBytes%allocationUnitBytes > 0 {
		roundedUp++
	}
	return roundedUp
}

// ConvertTagsToMap convert the tags from string to map, default tagDelimiter is ","
// the valid tags format is "key1=value1,key2=value2", which could be converted to
// {"key1": "value1", "key2": "value2"}
func ConvertTagsToMap(tags string, tagsDelimiter string) (map[string]string, error) {
	m := make(map[string]string)
	if tags == "" {
		return m, nil
	}
	if tagsDelimiter == "" {
		tagsDelimiter = ","
	}
	s := strings.Split(tags, tagsDelimiter)
	for _, tag := range s {
		kv := strings.SplitN(tag, TagKeyValueDelimiter, 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("tags '%s' are invalid, the format should like: 'key1=value1%skey2=value2'", tags, tagsDelimiter)
		}
		key := strings.TrimSpace(kv[0])
		if key == "" {
			return nil, fmt.Errorf("tags '%s' are invalid, the format should like: 'key1=value1%skey2=value2'", tags, tagsDelimiter)
		}
		// <>%&?/. are not allowed in tag key
		if strings.ContainsAny(key, "<>%&?/.") {
			return nil, fmt.Errorf("tag key '%s' contains invalid characters", key)
		}
		value := strings.TrimSpace(kv[1])
		m[key] = value
	}

	return m, nil
}

func MakeDir(pathname string) error {
	err := os.MkdirAll(pathname, os.FileMode(0755))
	if err != nil {
		if !os.IsExist(err) {
			return err
		}
	}
	return nil
}

func MakeFile(pathname string) error {
	f, err := os.OpenFile(pathname, os.O_CREATE|os.O_RDWR, os.FileMode(0755))
	if err != nil {
		return fmt.Errorf("failed to open file %s: %v", pathname, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to close file %s: %v", pathname, err)
	}
	return nil
}

type VolumeLocks struct {
	locks sets.String
	mux   sync.Mutex
}

func NewVolumeLocks() *VolumeLocks {
	return &VolumeLocks{
		locks: sets.NewString(),
	}
}

func (vl *VolumeLocks) TryAcquire(volumeID string) bool {
	vl.mux.Lock()
	defer vl.mux.Unlock()
	if vl.locks.Has(volumeID) {
		return false
	}
	vl.locks.Insert(volumeID)
	return true
}

func (vl *VolumeLocks) Release(volumeID string) {
	vl.mux.Lock()
	defer vl.mux.Unlock()
	vl.locks.Delete(volumeID)
}

func GetElementsInArray1NotInArray2(arr1 []int, arr2 []int) []int {
	sort.Ints(arr1)
	sort.Ints(arr2)

	i, j := 0, 0
	result := []int{}
	for i < len(arr1) && j < len(arr2) {
		if arr1[i] < arr2[j] {
			result = append(result, arr1[i])
			i++
		} else if arr1[i] > arr2[j] {
			j++
		} else {
			i++
			j++
		}
	}
	for i < len(arr1) {
		result = append(result, arr1[i])
		i++
	}
	return result
}
