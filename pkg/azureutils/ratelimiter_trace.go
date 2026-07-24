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

package azureutils

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
	"k8s.io/client-go/util/flowcontrol"
)

// kubeRateLimitWaitThreshold is the minimum client-side rate-limiter wait
// duration worth recording as a span event. Shorter waits are normal
// token-bucket jitter and would only add noise to traces.
const kubeRateLimitWaitThreshold = 50 * time.Millisecond

// tracingRateLimiter wraps a client-go flowcontrol.RateLimiter and records a
// span event whenever a Kubernetes API request is held back by the client-side
// rate limiter (QPS/Burst exhaustion) for longer than
// kubeRateLimitWaitThreshold. This surfaces client-side back-pressure in
// traces, complementing the ARM server-side throttling ("throttled") events.
//
// It uses the OpenTelemetry API directly (rather than the azuredisk tracing
// helpers) so that this package does not import the azuredisk package, which
// would create an import cycle.
type tracingRateLimiter struct {
	flowcontrol.RateLimiter
}

func newTracingRateLimiter(inner flowcontrol.RateLimiter) *tracingRateLimiter {
	return &tracingRateLimiter{RateLimiter: inner}
}

// Wait times how long the underlying limiter blocks the request and, when the
// wait exceeds the threshold, adds a "kube_client_ratelimited" event to the
// currently active span. When tracing is disabled the active span is a no-op
// (IsRecording() == false) and nothing is recorded.
func (t *tracingRateLimiter) Wait(ctx context.Context) error {
	start := time.Now()
	err := t.RateLimiter.Wait(ctx)
	waited := time.Since(start)
	if waited >= kubeRateLimitWaitThreshold {
		if span := oteltrace.SpanFromContext(ctx); span.IsRecording() {
			span.AddEvent("kube_client_ratelimited", oteltrace.WithAttributes(
				attribute.String("wait", waited.String()),
			))
		}
	}
	return err
}
