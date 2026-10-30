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

package azureutils

import (
	"io/fs"
	"maps"
	"os"
	"path"
	"sync"
	"syscall"
	"time"
)

type fakeDirEntry struct {
	name string
}

type fakeFileInfo struct {
	name string
}

func (f fakeFileInfo) Name() string {
	return f.name
}

func (f fakeFileInfo) Size() int64 {
	return 0
}

func (f fakeFileInfo) Mode() os.FileMode {
	return 777
}

func (f fakeFileInfo) ModTime() time.Time {
	return time.Now()
}
func (f fakeFileInfo) IsDir() bool {
	return false
}

func (f fakeFileInfo) Sys() interface{} {
	return nil
}

func (fd *fakeDirEntry) Type() fs.FileMode {
	return 777
}

func (fd *fakeDirEntry) Info() (fs.FileInfo, error) {
	return fakeFileInfo{
		name: fd.name,
	}, nil
}

func (fd *fakeDirEntry) Name() string {
	return fd.name
}

func (fd *fakeDirEntry) IsDir() bool {
	return false
}

var (
	lunStr   = "1"
	diskPath = "2:0:0:" + lunStr
	devName  = "sdd"
	// Azure attaches SCSI data disks to this controller (the GUID is a fixed constant)
	dataDiskControllerPath = "/sys/bus/vmbus/devices/f8b3781b-1e82-4818-a1c3-63d806ec15bb"
)

// FakeIOHandlerOptions describes the files a FakeIOHandler serves.
type FakeIOHandlerOptions struct {
	// Dirs lists each directory's entries; a path exists if its parent directory lists it.
	Dirs map[string][]string
	// Files holds the contents ReadFile returns for each file.
	Files map[string][]byte
	// Errors holds the error that ReadDir, Stat, ReadFile and CheckOpenable return for a path.
	Errors map[string]error
	// OpensNeeded holds the number of times CheckOpenable should return ENXIO before succeeding.
	OpensNeeded map[string]int
	// WriteError is returned by every WriteFile call; the write is still recorded.
	WriteError error
	// OnWrite runs after each successful write so a test can mimic the kernel, e.g. by adding a disk after a SCSI scan.
	OnWrite func(dirs map[string][]string)
}

// FakeWrite is a WriteFile call recorded by a FakeIOHandler.
type FakeWrite struct {
	Path string
	Data string
	Perm os.FileMode
}

// FakeIOHandler is an IOHandler that serves files from in-memory tables and records writes.
type FakeIOHandler struct {
	mutex   sync.Mutex
	options FakeIOHandlerOptions
	writes  []FakeWrite
}

// NewFakeIOHandler returns a FakeIOHandler for a healthy node whose SCSI data disk at LUN 1 is /dev/sdd.
func NewFakeIOHandler() IOHandler {
	scsiTarget := dataDiskControllerPath + "/host2/target2:0:0"
	scsiDevice := scsiTarget + "/" + diskPath
	return NewFakeIOHandlerWithOptions(FakeIOHandlerOptions{
		Dirs: map[string][]string{
			dataDiskControllerPath: {"channels", "host2"},
			scsiTarget:             {diskPath},
			scsiDevice + "/block":  {devName},
			"/dev":                 {devName},
		},
	})
}

// NewFakeIOHandlerWithOptions returns a FakeIOHandler that serves files as the options describe.
func NewFakeIOHandlerWithOptions(options FakeIOHandlerOptions) *FakeIOHandler {
	// Copy maps that can be changed by the handler, so the inputs are left alone.
	dirs := make(map[string][]string, len(options.Dirs))
	for dirname, names := range options.Dirs {
		dirs[dirname] = append([]string(nil), names...)
	}
	options.Dirs = dirs
	options.OpensNeeded = maps.Clone(options.OpensNeeded)
	return &FakeIOHandler{options: options}
}

// Writes returns the WriteFile calls made so far.
func (handler *FakeIOHandler) Writes() []FakeWrite {
	handler.mutex.Lock()
	defer handler.mutex.Unlock()
	return append([]FakeWrite(nil), handler.writes...)
}

func (handler *FakeIOHandler) ReadDir(dirname string) ([]os.DirEntry, error) {
	handler.mutex.Lock()
	defer handler.mutex.Unlock()
	if err, ok := handler.options.Errors[dirname]; ok {
		return nil, err
	}
	names, ok := handler.options.Dirs[dirname]
	if !ok {
		return nil, os.ErrNotExist
	}
	entries := make([]os.DirEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, &fakeDirEntry{name: name})
	}
	return entries, nil
}

func (handler *FakeIOHandler) WriteFile(filename string, data []byte, perm os.FileMode) error {
	handler.mutex.Lock()
	defer handler.mutex.Unlock()
	handler.writes = append(handler.writes, FakeWrite{Path: filename, Data: string(data), Perm: perm})
	if handler.options.WriteError != nil {
		return handler.options.WriteError
	}
	if handler.options.OnWrite != nil {
		handler.options.OnWrite(handler.options.Dirs)
	}
	return nil
}

func (handler *FakeIOHandler) ReadFile(filename string) ([]byte, error) {
	if err, ok := handler.options.Errors[filename]; ok {
		return nil, err
	}
	data, ok := handler.options.Files[filename]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}

func (handler *FakeIOHandler) Stat(filename string) (os.FileInfo, error) {
	handler.mutex.Lock()
	defer handler.mutex.Unlock()
	if err, ok := handler.options.Errors[filename]; ok {
		return nil, err
	}
	name := path.Base(filename)
	for _, entry := range handler.options.Dirs[path.Dir(filename)] {
		if entry == name {
			return fakeFileInfo{name: name}, nil
		}
	}
	return nil, os.ErrNotExist
}

func (handler *FakeIOHandler) CheckOpenable(filename string) error {
	_, err := handler.Stat(filename)
	if err == nil {
		if handler.options.OpensNeeded != nil {
			handler.mutex.Lock()
			defer handler.mutex.Unlock()
			if handler.options.OpensNeeded[filename] > 0 {
				handler.options.OpensNeeded[filename]--
				return syscall.ENXIO
			}
		}
	}
	return err
}
