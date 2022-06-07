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
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	volerr "k8s.io/cloud-provider/volume/errors"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

const (
	GiB                  = 1024 * 1024 * 1024
	TagsDelimiter        = ","
	TagKeyValueDelimiter = "="
)

var (
	strToCode = map[azdiskv1beta2.AzErrorCode]codes.Code{
		azdiskv1beta2.AzErrorCodeOK:                 codes.OK,
		azdiskv1beta2.AzErrorCodeCanceled:           codes.Canceled,
		azdiskv1beta2.AzErrorCodeUnknown:            codes.Unknown,
		azdiskv1beta2.AzErrorCodeInvalidArgument:    codes.InvalidArgument,
		azdiskv1beta2.AzErrorCodeDeadlineExceeded:   codes.DeadlineExceeded,
		azdiskv1beta2.AzErrorCodeNotFound:           codes.NotFound,
		azdiskv1beta2.AzErrorCodeAlreadyExists:      codes.AlreadyExists,
		azdiskv1beta2.AzErrorCodePermissionDenied:   codes.PermissionDenied,
		azdiskv1beta2.AzErrorCodeResourceExhausted:  codes.ResourceExhausted,
		azdiskv1beta2.AzErrorCodeFailedPrecondition: codes.FailedPrecondition,
		azdiskv1beta2.AzErrorCodeAborted:            codes.Aborted,
		azdiskv1beta2.AzErrorCodeOutOfRange:         codes.OutOfRange,
		azdiskv1beta2.AzErrorCodeUnimplemented:      codes.Unimplemented,
		azdiskv1beta2.AzErrorCodeInternal:           codes.Internal,
		azdiskv1beta2.AzErrorCodeUnavailable:        codes.Unavailable,
		azdiskv1beta2.AzErrorCodeDataLoss:           codes.DataLoss,
		azdiskv1beta2.AzErrorCodeUnauthenticated:    codes.Unauthenticated,
	}

	codeToStr = map[codes.Code]azdiskv1beta2.AzErrorCode{
		codes.OK:                 azdiskv1beta2.AzErrorCodeOK,
		codes.Canceled:           azdiskv1beta2.AzErrorCodeCanceled,
		codes.Unknown:            azdiskv1beta2.AzErrorCodeUnknown,
		codes.InvalidArgument:    azdiskv1beta2.AzErrorCodeInvalidArgument,
		codes.DeadlineExceeded:   azdiskv1beta2.AzErrorCodeDeadlineExceeded,
		codes.NotFound:           azdiskv1beta2.AzErrorCodeNotFound,
		codes.AlreadyExists:      azdiskv1beta2.AzErrorCodeAlreadyExists,
		codes.PermissionDenied:   azdiskv1beta2.AzErrorCodePermissionDenied,
		codes.ResourceExhausted:  azdiskv1beta2.AzErrorCodeResourceExhausted,
		codes.FailedPrecondition: azdiskv1beta2.AzErrorCodeFailedPrecondition,
		codes.Aborted:            azdiskv1beta2.AzErrorCodeAborted,
		codes.OutOfRange:         azdiskv1beta2.AzErrorCodeOutOfRange,
		codes.Unimplemented:      azdiskv1beta2.AzErrorCodeUnimplemented,
		codes.Internal:           azdiskv1beta2.AzErrorCodeInternal,
		codes.Unavailable:        azdiskv1beta2.AzErrorCodeUnavailable,
		codes.DataLoss:           azdiskv1beta2.AzErrorCodeDataLoss,
		codes.Unauthenticated:    azdiskv1beta2.AzErrorCodeUnauthenticated,
	}

	azureRetryErrorRegEx     = regexp.MustCompile("Retriable: (true|false), RetryAfter: ([0-9]+)s, HTTPStatusCode: ([0-9]+), RawError: ")
	httpConflictStatusString = fmt.Sprintf("%d", http.StatusConflict)
)

const (
	_ int = iota // Full match index - unused.
	retriableValueIndex
	_ // Retry after value index - unused.
	httpStatusCodeValueIndex
	azureRetryErrorValueCount
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
	return roundUpSize(volumeSizeBytes, GiB) * GiB
}

// RoundUpGiB rounds up the volume size in bytes up to multiplications of GiB
// in the unit of GiB
func RoundUpGiB(volumeSizeBytes int64) int64 {
	return roundUpSize(volumeSizeBytes, GiB)
}

// BytesToGiB conversts Bytes to GiB
func BytesToGiB(volumeSizeBytes int64) int64 {
	return volumeSizeBytes / GiB
}

// GiBToBytes converts GiB to Bytes
func GiBToBytes(volumeSizeGiB int64) int64 {
	return volumeSizeGiB * GiB
}

// roundUpSize calculates how many allocation units are needed to accommodate
// a volume of given size. E.g. when user wants 1500MiB volume, while AWS EBS
// allocates volumes in gibibyte-sized chunks,
// RoundUpSize(1500 * 1024*1024, 1024*1024*1024) returns '2'
// (2 GiB is the smallest allocatable volume that can hold 1500MiB)
func roundUpSize(volumeSizeBytes int64, allocationUnitBytes int64) int64 {
	roundedUp := volumeSizeBytes / allocationUnitBytes
	if volumeSizeBytes%allocationUnitBytes > 0 {
		roundedUp++
	}
	return roundedUp
}

// ConvertTagsToMap convert the tags from string to map
// the valid tags format is "key1=value1,key2=value2", which could be converted to
// {"key1": "value1", "key2": "value2"}
func ConvertTagsToMap(tags string) (map[string]string, error) {
	m := make(map[string]string)
	if tags == "" {
		return m, nil
	}
	s := strings.Split(tags, TagsDelimiter)
	for _, tag := range s {
		kv := strings.Split(tag, TagKeyValueDelimiter)
		if len(kv) != 2 {
			return nil, fmt.Errorf("Tags '%s' are invalid, the format should like: 'key1=value1,key2=value2'", tags)
		}
		key := strings.TrimSpace(kv[0])
		if key == "" {
			return nil, fmt.Errorf("Tags '%s' are invalid, the format should like: 'key1=value1,key2=value2'", tags)
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

func azErrorCodeFromRPCCode(c codes.Code) azdiskv1beta2.AzErrorCode {
	if val, ok := codeToStr[c]; ok {
		return val
	}
	return azdiskv1beta2.AzErrorCodeUnknown
}

func rpcCodeFromAzErrorCode(errorCode azdiskv1beta2.AzErrorCode) codes.Code {
	if val, ok := strToCode[errorCode]; ok {
		return val
	}
	return codes.Unknown
}

// NewAzError returns a new AzError object representing the specified error.
func NewAzError(err error) *azdiskv1beta2.AzError {
	var (
		errorCode    azdiskv1beta2.AzErrorCode
		errorMessage = err.Error()
		parameters   = make(map[string]string)
	)

	if derr, ok := err.(*volerr.DanglingAttachError); ok {
		errorCode = azdiskv1beta2.AzErrorCodeDanglingAttach
		parameters[azureconstants.CurrentNodeParameter] = string(derr.CurrentNode)
		parameters[azureconstants.DevicePathParameter] = derr.DevicePath
	} else {
		code := status.Code(err)

		if code == codes.Unknown {
			if values := azureRetryErrorRegEx.FindStringSubmatch(errorMessage); len(values) == azureRetryErrorValueCount {
				if strings.EqualFold(values[retriableValueIndex], "false") {
					code = codes.FailedPrecondition
				} else if strings.EqualFold(values[httpStatusCodeValueIndex], httpConflictStatusString) {
					code = codes.Aborted
				} else {
					code = codes.Unavailable
				}
			}
		}

		errorCode = azErrorCodeFromRPCCode(code)
	}

	return &azdiskv1beta2.AzError{
		Code:       errorCode,
		Message:    errorMessage,
		Parameters: parameters,
	}
}

// ErrorFromAzError returns an error object for the specified AzError instance.
func ErrorFromAzError(azError *azdiskv1beta2.AzError) error {
	if azError == nil {
		return nil
	}

	if azError.Code == azdiskv1beta2.AzErrorCodeDanglingAttach {
		currentName := ""
		devicePath := ""

		if azError.Parameters != nil {
			var ok bool

			if currentName, ok = azError.Parameters[azureconstants.CurrentNodeParameter]; !ok {
				currentName = ""
			}

			if devicePath, ok = azError.Parameters[azureconstants.DevicePathParameter]; !ok {
				devicePath = ""
			}
		}

		return volerr.NewDanglingError(azError.Message, types.NodeName(currentName), devicePath)
	}

	return status.Error(rpcCodeFromAzErrorCode(azError.Code), azError.Message)
}
