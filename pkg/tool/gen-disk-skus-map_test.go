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

package main

import (
	"os"
	"testing"
)

func TestMain(t *testing.T) {
	// Set environment variable for test scenario
	os.Setenv("TEST_SCENARIO", "true")
	defer os.Unsetenv("TEST_SCENARIO")

	// Capture stdout
	old := os.Stdout
	_, w, _ := os.Pipe()
	os.Stdout = w

	// Replace exit function with mock function
	var exitCode int
	exit = func(code int) {
		exitCode = code
	}

	// Call main function
	main()

	// Restore stdout
	w.Close()
	os.Stdout = old
	exit = func(code int) {
		os.Exit(code)
	}

	if exitCode != 0 {
		t.Errorf("Expected exit code 0, but got %d", exitCode)
	}
}
