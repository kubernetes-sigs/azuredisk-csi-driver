/*
Copyright 2026 The Kubernetes Authors.

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
	"testing"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
)

func TestWrapConfigRateLimiterWithTracing(t *testing.T) {
	t.Run("preserves custom limiter", func(t *testing.T) {
		custom := flowcontrol.NewFakeAlwaysRateLimiter()
		config := &rest.Config{RateLimiter: custom}

		WrapConfigRateLimiterWithTracing(config)

		wrapped, ok := config.RateLimiter.(*tracingRateLimiter)
		if !ok || wrapped.RateLimiter != custom {
			t.Fatalf("custom limiter was not wrapped: %#v", config.RateLimiter)
		}
		WrapConfigRateLimiterWithTracing(config)
		if config.RateLimiter != wrapped {
			t.Fatal("rate limiter was wrapped more than once")
		}
	})

	t.Run("preserves disabled throttling", func(t *testing.T) {
		config := &rest.Config{QPS: -1}
		WrapConfigRateLimiterWithTracing(config)
		if config.RateLimiter != nil {
			t.Fatalf("negative QPS created a rate limiter: %#v", config.RateLimiter)
		}
	})

	t.Run("wraps default limiter", func(t *testing.T) {
		config := &rest.Config{}
		WrapConfigRateLimiterWithTracing(config)
		if _, ok := config.RateLimiter.(*tracingRateLimiter); !ok {
			t.Fatalf("default limiter was not wrapped: %#v", config.RateLimiter)
		}
	})
}
