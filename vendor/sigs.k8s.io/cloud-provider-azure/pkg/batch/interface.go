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

package batch

type Logger interface {
	// Logs to the verbose log. It handles the arguments in the manner of fmt.Printf.
	Verbosef(format string, v ...interface{})

	// Logs to the informational log. It handles the arguments in the manner of fmt.Printf.
	Infof(format string, v ...interface{})

	// Logs to the warning log. It handles the arguments in the manner of fmt.Printf.
	Warningf(format string, v ...interface{})

	// Logs to the error log. It handles the arguments in the manner of fmt.Printf.
	Errorf(format string, v ...interface{})
}
