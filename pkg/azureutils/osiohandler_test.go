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
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	nonExistingPath       = "/non-existing-path"
	errPathNotExist error = &fs.PathError{
		Op:   "open",
		Path: nonExistingPath,
		Err:  syscall.ENOENT,
	}

	sampleText = "The quick brown fox jumps over the lazy dog."
)

func TestOSIOHandler_ReadDir(t *testing.T) {
	tmpDir := t.TempDir()
	files := []string{"A", "B", "C"}

	for _, file := range files {
		f, err := os.Create(filepath.Join(tmpDir, file))
		require.NoError(t, err)
		f.Close()
	}

	handler := NewOSIOHandler()

	finfs, err := handler.ReadDir(tmpDir)
	require.NoError(t, err)

	for _, finf := range finfs {
		assert.Contains(t, files, finf.Name())
	}

	_, err = handler.ReadDir(nonExistingPath)
	require.Equal(t, err, errPathNotExist)
}

func TestOSIOHandler_ReadWriteFile(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "testfile.txt")

	handler := NewOSIOHandler()

	err := handler.WriteFile(tmpFile, []byte(sampleText), 0666)
	require.NoError(t, err)

	readBytes, err := handler.ReadFile(tmpFile)
	require.NoError(t, err)
	require.Equal(t, sampleText, string(readBytes))

	_, err = handler.ReadFile(nonExistingPath)
	require.Equal(t, err, errPathNotExist)
}

func TestOSIOHandler_StatAndCheckOpenable(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "testfile.txt")
	require.NoError(t, os.WriteFile(tmpFile, []byte(sampleText), 0600))

	handler := NewOSIOHandler()

	_, err := handler.Stat(tmpFile)
	require.NoError(t, err)
	require.NoError(t, handler.CheckOpenable(tmpFile))

	_, err = handler.Stat(nonExistingPath)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.ErrorIs(t, handler.CheckOpenable(nonExistingPath), os.ErrNotExist)
}
