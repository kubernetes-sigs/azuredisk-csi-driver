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
	"strings"
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

func TestFormatInt(t *testing.T) {
	tests := []struct {
		name     string
		input    int
		expected string
	}{
		{
			name:     "positive integer",
			input:    123,
			expected: "123",
		},
		{
			name:     "zero",
			input:    0,
			expected: "0",
		},
		{
			name:     "negative integer",
			input:    -5,
			expected: "0",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := formatInt(test.input)
			if result != test.expected {
				t.Errorf("formatInt(%d) = %s, expected %s", test.input, result, test.expected)
			}
		})
	}
}

func TestAppendWithErrCheck(t *testing.T) {
	tests := []struct {
		name         string
		initialText  string
		appendText   string
		expectedText string
	}{
		{
			name:         "append to empty builder",
			initialText:  "",
			appendText:   "hello",
			expectedText: "hello",
		},
		{
			name:         "append to existing text",
			initialText:  "hello",
			appendText:   " world",
			expectedText: "hello world",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var sb strings.Builder
			sb.WriteString(test.initialText)
			
			appendWithErrCheck(&sb, test.appendText)
			
			result := sb.String()
			if result != test.expectedText {
				t.Errorf("appendWithErrCheck result = %s, expected %s", result, test.expectedText)
			}
		})
	}
}

func TestGetAllSkus(t *testing.T) {
	tests := []struct {
		name        string
		testScenario string
		expectError bool
	}{
		{
			name:        "test scenario enabled",
			testScenario: "true",
			expectError: false, // Should succeed without calling az command
		},
		{
			name:        "test scenario disabled",
			testScenario: "",
			expectError: true, // Will likely fail since az command is not available in test
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Set test environment
			original := os.Getenv("TEST_SCENARIO")
			os.Setenv("TEST_SCENARIO", test.testScenario)
			defer os.Setenv("TEST_SCENARIO", original)

			err := getAllSkus()
			
			if test.expectError && err == nil {
				t.Errorf("Expected error but got nil")
			}
			if !test.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}
