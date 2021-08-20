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

package testutil

import (
	"fmt"
	"os"
	"reflect"
	"runtime"
)

func IsRunningInProw() bool {
	_, ok := os.LookupEnv("AZURE_CREDENTIALS")
	return ok
}

// TestError is used to define the errors given by different kinds of OS
// Implements the `error` interface
type TestError struct {
	DefaultError error
	WindowsError error
}

// Error returns the error on the basis of the platform
func (t TestError) Error() string {
	if t.WindowsError == nil || !isWindows() {
		if t.DefaultError == nil {
			return ""
		}
		return t.DefaultError.Error()
	}
	if t.WindowsError == nil {
		return ""
	}
	return t.WindowsError.Error()
}

// AssertError checks if the TestError matches with the actual error
// on the basis of the platform on which it is running
func AssertError(expected *TestError, actual error) bool {
	if isWindows() {
		if expected.WindowsError == nil {
			return reflect.DeepEqual(expected.DefaultError, actual)
		}
		return reflect.DeepEqual(expected.WindowsError, actual)
	}
	return reflect.DeepEqual(expected.DefaultError, actual)
}

// IsErrorEquivalent checks for error equivalence
// TODO Update to check for error equivalence instead of equality
func IsErrorEquivalent(actual, expected error) bool {
	return reflect.DeepEqual(expected, actual)
}

// GetWorkDirPath returns the path to the current working directory
func GetWorkDirPath(dir string) (string, error) {
	path, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%c%s", path, os.PathSeparator, dir), nil
}
func isWindows() bool {
	return runtime.GOOS == "windows"
}
