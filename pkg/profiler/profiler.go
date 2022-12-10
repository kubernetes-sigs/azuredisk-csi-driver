/*
Copyright 2022 The Kubernetes Authors.

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
package profiler

import (
	"net/http"
	"net/http/pprof"

	"k8s.io/klog/v2"
)

// ListenAndServeOrDie begins listening on the specified TCP address and serving
// requests for profiling data from the pprof package. If the server cannot start,
// the process will exit.
func ListenAndServeOrDie(addr string) {
	klog.V(2).Infof("Starting pprof server on %s", addr)

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	if err := http.ListenAndServe(addr, mux); err != nil {
		klog.Fatalf("Failed to start profiler server: %s", err.Error())
	}
}
