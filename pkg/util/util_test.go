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
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRoundUpBytes(t *testing.T) {
	var sizeInBytes int64 = 1024
	actual := RoundUpBytes(sizeInBytes)
	if actual != 1*GiB {
		t.Fatalf("Wrong result for RoundUpBytes. Got: %d", actual)
	}
}

func TestRoundUpGiB(t *testing.T) {
	var sizeInBytes int64 = 1
	actual := RoundUpGiB(sizeInBytes)
	if actual != 1 {
		t.Fatalf("Wrong result for RoundUpGiB. Got: %d", actual)
	}
}

func TestBytesToGiB(t *testing.T) {
	var sizeInBytes int64 = 5 * GiB

	actual := BytesToGiB(sizeInBytes)
	if actual != 5 {
		t.Fatalf("Wrong result for BytesToGiB. Got: %d", actual)
	}
}

func TestGiBToBytes(t *testing.T) {
	var sizeInGiB int64 = 3

	actual := GiBToBytes(sizeInGiB)
	if actual != 3*GiB {
		t.Fatalf("Wrong result for GiBToBytes. Got: %d", actual)
	}
}

func TestConvertTagsToMap(t *testing.T) {
	testCases := []struct {
		desc           string
		tags           string
		expectedOutput map[string]string
		expectedError  bool
	}{
		{
			desc:           "should return empty map when tag is empty",
			tags:           "",
			expectedOutput: map[string]string{},
			expectedError:  false,
		},
		{
			desc: "sing valid tag should be converted",
			tags: "key=value",
			expectedOutput: map[string]string{
				"key": "value",
			},
			expectedError: false,
		},
		{
			desc: "multiple valid tags should be converted",
			tags: "key1=value1,key2=value2",
			expectedOutput: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
			expectedError: false,
		},
		{
			desc: "whitespaces should be trimmed",
			tags: "key1=value1, key2=value2",
			expectedOutput: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
			expectedError: false,
		},
		{
			desc:           "should return error for invalid format",
			tags:           "foo,bar",
			expectedOutput: nil,
			expectedError:  true,
		},
		{
			desc:           "should return error for when key is missed",
			tags:           "key1=value1,=bar",
			expectedOutput: nil,
			expectedError:  true,
		},
	}

	for i, c := range testCases {
		m, err := ConvertTagsToMap(c.tags)
		if c.expectedError {
			assert.NotNil(t, err, "TestCase[%d]: %s", i, c.desc)
		} else {
			assert.Nil(t, err, "TestCase[%d]: %s", i, c.desc)
			if !reflect.DeepEqual(m, c.expectedOutput) {
				t.Errorf("got: %v, expected: %v, desc: %v", m, c.expectedOutput, c.desc)
			}
		}
	}
}

func TestMakeDir(t *testing.T) {
	testCases := []struct {
		desc          string
		setup         func()
		targetDir     string
		expectedError bool
		cleanup       func()
	}{
		{
			desc:          "should create directory",
			targetDir:     "./target_test",
			expectedError: false,
		},
		{
			desc: "should not return error if path already exists",
			setup: func() {
				os.Mkdir("./target_test", os.FileMode(0755))
			},
			targetDir:     "./target_test",
			expectedError: false,
		},
		{
			desc: "[Error] existing file in target path",
			setup: func() {
				os.Create("file_exists")
			},
			targetDir:     "./file_exists",
			expectedError: true,
			cleanup: func() {
				os.Remove("./file_exists")
			},
		},
	}
	for i, testCase := range testCases {
		if testCase.setup != nil {
			testCase.setup()
		}
		err := MakeDir(testCase.targetDir)
		if testCase.expectedError {
			fmt.Print(err)
			assert.NotNil(t, err, "TestCase[%d]: %s", i, testCase.desc)
		} else {
			assert.Nil(t, err, "TestCase[%d]: %s", i, testCase.desc)
			err = os.RemoveAll(testCase.targetDir)
			assert.NoError(t, err)
		}
		if testCase.cleanup != nil {
			testCase.cleanup()
		}
	}
}

func TestMakeFile(t *testing.T) {
	testCases := []struct {
		desc          string
		setup         func()
		targetFile    string
		expectedError bool
		cleanup       func()
	}{
		{
			desc:          "should create a file",
			targetFile:    "./target_test",
			expectedError: false,
		},
		{
			desc: "[Error] directory exists with the target file name",
			setup: func() {
				os.Mkdir("./target_test", os.FileMode(0755))
			},
			targetFile:    "./target_test",
			expectedError: true,
			cleanup: func() {
				os.Remove("./target_test")
			},
		},
	}
	for i, testCase := range testCases {
		if testCase.setup != nil {
			testCase.setup()
		}
		err := MakeFile(testCase.targetFile)
		if testCase.expectedError {
			fmt.Print(err)
			assert.NotNil(t, err, "TestCase[%d]: %s", i, testCase.desc)
		} else {
			assert.Nil(t, err, "TestCase[%d]: %s", i, testCase.desc)
			err = os.RemoveAll(testCase.targetFile)
			assert.NoError(t, err)
		}
		if testCase.cleanup != nil {
			testCase.cleanup()
		}
	}
}

func TestVolumeLock(t *testing.T) {
	volumeLocks := NewVolumeLocks()
	testCases := []struct {
		desc           string
		setup          func()
		targetID       string
		expectedOutput bool
		cleanup        func()
	}{
		{
			desc:           "should acquire lock",
			targetID:       "test-lock",
			expectedOutput: true,
		},
		{
			desc: "should fail to acquire lock if lock is held by someone else",
			setup: func() {
				volumeLocks.TryAcquire("test-lock")
			},
			targetID:       "test-lock",
			expectedOutput: false,
		},
	}
	for i, testCase := range testCases {
		if testCase.setup != nil {
			testCase.setup()
		}
		output := volumeLocks.TryAcquire(testCase.targetID)
		if testCase.expectedOutput {
			assert.True(t, output, "TestCase[%d]: %s", i, testCase.desc)
		} else {
			assert.False(t, output, "TestCase[%d]: %s", i, testCase.desc)
		}
		volumeLocks.Release(testCase.targetID)
		if testCase.cleanup != nil {
			testCase.cleanup()
		}
	}
}
