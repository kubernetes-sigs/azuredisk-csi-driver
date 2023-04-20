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

package azureutils

import (
	"os"
)

type IOHandler interface {
	ReadDir(dirname string) ([]os.DirEntry, error)
	WriteFile(filename string, data []byte, perm os.FileMode) error
	Readlink(name string) (string, error)
	ReadFile(filename string) ([]byte, error)
}

type osIOHandler struct{}

func NewOSIOHandler() IOHandler {
	return &osIOHandler{}
}

func (handler *osIOHandler) ReadDir(dirname string) ([]os.DirEntry, error) {
	return os.ReadDir(dirname)
}

func (handler *osIOHandler) WriteFile(filename string, data []byte, perm os.FileMode) error {
	return os.WriteFile(filename, data, perm)
}

func (handler *osIOHandler) Readlink(name string) (string, error) {
	return os.Readlink(name)
}

func (handler *osIOHandler) ReadFile(filename string) ([]byte, error) {
	return os.ReadFile(filename)
}
