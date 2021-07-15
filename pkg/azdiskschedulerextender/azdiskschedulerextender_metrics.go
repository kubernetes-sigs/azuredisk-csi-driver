// +build azurediskv2

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

package main

import (
	"time"

	"k8s.io/component-base/metrics"
	"k8s.io/component-base/metrics/legacyregistry"
	"k8s.io/klog/v2"
)

const (
	scheduler = "schedulerExtender"
)

var (
	FilterNodesDuration = metrics.NewHistogramVec(
		&metrics.HistogramOpts{
			Namespace:      scheduler,
			Name:           "extender_filter_nodes_duration_seconds",
			Help:           "Latency for handling filter nodes request.",
			Buckets:        metrics.ExponentialBuckets(0.0001, 2, 12),
			StabilityLevel: metrics.ALPHA,
		},
		[]string{"filter"})

	PrioritizeNodesDuration = metrics.NewHistogramVec(
		&metrics.HistogramOpts{
			Namespace:      scheduler,
			Name:           "extender_prioritize_nodes_duration_seconds",
			Help:           "Latency for handling prioritize nodes request.",
			Buckets:        metrics.ExponentialBuckets(0.0001, 2, 12),
			StabilityLevel: metrics.ALPHA,
		},
		[]string{"filter"})

	// TODO add with more advanced caching
	// CacheSize = metrics.NewGaugeVec(
	// 	&metrics.GaugeOpts{
	// 		Namespace:      scheduler,
	// 		Name:           "extender_cache_size",
	// 		Help:           "Number of azDrivers and azVolumeAttachments in the cache.",
	// 		StabilityLevel: metrics.ALPHA,
	// 	}, []string{"type"})

	Goroutines = metrics.NewGaugeVec(
		&metrics.GaugeOpts{
			Namespace:      scheduler,
			Name:           "extender_goroutines",
			Help:           "Number of running goroutines accessing kube api cache.",
			StabilityLevel: metrics.ALPHA,
		}, []string{"work"})

	metricsList = []metrics.Registerable{
		FilterNodesDuration,
		PrioritizeNodesDuration,
		Goroutines,
		//CacheSize,
	}
)

func RegisterMetrics(schedulerMetrics ...metrics.Registerable) {
	klog.V(2).Infof("Registering metrics.")
	for _, metric := range schedulerMetrics {
		legacyregistry.MustRegister(metric)
	}
}

func DurationSince(functionName string, start time.Time) float64 {
	requestDuration := time.Since(start).Seconds()
	klog.V(2).Infof("Call to %v took: %f ms\n", functionName, requestDuration/1000)
	return requestDuration
}
