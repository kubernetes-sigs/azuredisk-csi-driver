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

package main

import (
	"log"
	"net/http"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/klog/v2"
)

func main() {

	// Register prometheus metrics
	podDowntimeHistogram := prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "default",
		Name:      "pod_downtime_duration",
		Help:      "Downtime seen by the workload pod after failing",
		Buckets:   prometheus.LinearBuckets(10, 10, 20),
	})

	klog.Infof("Registering metrics")
	r := prometheus.NewRegistry()
	r.MustRegister(podDowntimeHistogram)

	promMux := http.NewServeMux()
	promMux.Handle("/metrics", promhttp.HandlerFor(r, promhttp.HandlerOpts{}))
	srv1 := &http.Server{Addr: ":9090", Handler: promMux}

	// Create an http server that listens to the timestamp and logs it
	podDowntimeMux := http.NewServeMux()
	podDowntimeMux.HandleFunc("/pod-failover", func(w http.ResponseWriter, r *http.Request) {
		downtime, _ := strconv.ParseFloat(r.URL.Query().Get("value"), 64)
		klog.Infof("The downtime observed by the logging pod is: %f", downtime)
		podDowntimeHistogram.Observe(downtime)
	})
	srv2 := &http.Server{Addr: ":9091", Handler: podDowntimeMux}

	// Push the metrics to prometheus
	go func() { log.Fatal(srv1.ListenAndServe()) }()
	log.Fatal(srv2.ListenAndServe())
}
