/*
Copyright 2024 The Kubernetes Authors.

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

package filewatcher

import (
	"os"
	"testing"
	"time"
)

func TestWatchFileForChanges(t *testing.T) {
	// Capture stdout
	old := os.Stdout
	_, w, _ := os.Pipe()
	os.Stdout = w

	// Replace exit function with mock function
	var exitCode int
	exit.Store(func(code int) {
		exitCode = code
	})

	// Create a temporary file to watch
	tmpfile, err := os.CreateTemp("", "testfile")
	if err != nil {
		t.Fatal(err)
	}

	// Test the WatchFileForChanges function
	err = WatchFileForChanges(tmpfile.Name())
	if err != nil {
		t.Errorf("Failed to watch file: %v", err)
	}

	// Simulate a file change
	err = os.WriteFile(tmpfile.Name(), []byte("new content"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	if exitCode != 0 {
		t.Errorf("Expected exit code 0, but got %d", exitCode)
	}

	os.Remove(tmpfile.Name())

	time.Sleep(1 * time.Second)

	// Restore stdout
	w.Close()
	os.Stdout = old
	defer func() {
		exit.Store(func(code int) {
			os.Exit(code)
		})
	}()
}
