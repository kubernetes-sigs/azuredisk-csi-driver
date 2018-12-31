/*
Copyright 2017 The Kubernetes Authors.

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
	"fmt"
	"io/ioutil"
	"os"
	"regexp"
	"strconv"
	libstrings "strings"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2018-10-01/compute"

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	api "k8s.io/kubernetes/pkg/apis/core"
)

const (
	defaultStorageAccountType       = compute.StandardLRS
	defaultAzureDiskKind            = v1.AzureManagedDisk
	defaultAzureDataDiskCachingMode = v1.AzureDataDiskCachingNone
)

var (
	supportedCachingModes = sets.NewString(
		string(api.AzureDataDiskCachingNone),
		string(api.AzureDataDiskCachingReadOnly),
		string(api.AzureDataDiskCachingReadWrite))

	supportedDiskKinds = sets.NewString(
		string(api.AzureSharedBlobDisk),
		string(api.AzureDedicatedBlobDisk),
		string(api.AzureManagedDisk))

	lunPathRE = regexp.MustCompile(`/devhost/disk/azure/scsi(?:.*)/lun(.+)`)
)

func normalizeKind(kind string) (v1.AzureDataDiskKind, error) {
	if kind == "" {
		return defaultAzureDiskKind, nil
	}

	if !supportedDiskKinds.Has(kind) {
		return "", fmt.Errorf("azureDisk - %s is not supported disk kind. Supported values are %s", kind, supportedDiskKinds.List())
	}

	return v1.AzureDataDiskKind(kind), nil
}

func normalizeStorageAccountType(storageAccountType string) (compute.DiskStorageAccountTypes, error) {
	if storageAccountType == "" {
		return defaultStorageAccountType, nil
	}

	sku := compute.DiskStorageAccountTypes(storageAccountType)
	supportedSkuNames := compute.PossibleDiskStorageAccountTypesValues()
	for _, s := range supportedSkuNames {
		if sku == s {
			return sku, nil
		}
	}

	return "", fmt.Errorf("azureDisk - %s is not supported sku/storageaccounttype. Supported values are %s", storageAccountType, supportedSkuNames)
}

func normalizeCachingMode(cachingMode v1.AzureDataDiskCachingMode) (v1.AzureDataDiskCachingMode, error) {
	if cachingMode == "" {
		return defaultAzureDataDiskCachingMode, nil
	}

	if !supportedCachingModes.Has(string(cachingMode)) {
		return "", fmt.Errorf("azureDisk - %s is not supported cachingmode. Supported values are %s", cachingMode, supportedCachingModes.List())
	}

	return cachingMode, nil
}

type ioHandler interface {
	ReadDir(dirname string) ([]os.FileInfo, error)
	WriteFile(filename string, data []byte, perm os.FileMode) error
	Readlink(name string) (string, error)
	ReadFile(filename string) ([]byte, error)
}

//TODO: check if priming the iscsi interface is actually needed

type osIOHandler struct{}

func (handler *osIOHandler) ReadDir(dirname string) ([]os.FileInfo, error) {
	return ioutil.ReadDir(dirname)
}

func (handler *osIOHandler) WriteFile(filename string, data []byte, perm os.FileMode) error {
	return ioutil.WriteFile(filename, data, perm)
}

func (handler *osIOHandler) Readlink(name string) (string, error) {
	return os.Readlink(name)
}

func (handler *osIOHandler) ReadFile(filename string) ([]byte, error) {
	return ioutil.ReadFile(filename)
}

func strFirstLetterToUpper(str string) string {
	if len(str) < 2 {
		return str
	}
	return libstrings.ToUpper(string(str[0])) + str[1:]
}

// getDiskLUN : deviceInfo could be a LUN number or a device path, e.g. /dev/disk/azure/scsi1/lun2
func getDiskLUN(deviceInfo string) (int32, error) {
	var diskLUN string
	if len(deviceInfo) <= 2 {
		diskLUN = deviceInfo
	} else {
		// extract the LUN num from a device path
		matches := lunPathRE.FindStringSubmatch(deviceInfo)
		if len(matches) == 2 {
			diskLUN = matches[1]
		} else {
			return -1, fmt.Errorf("cannot parse deviceInfo: %s", deviceInfo)
		}
	}

	lun, err := strconv.Atoi(diskLUN)
	if err != nil {
		return -1, err
	}
	return int32(lun), nil
}
