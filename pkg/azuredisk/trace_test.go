/*
Copyright 2023 The Kubernetes Authors.

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

package azuredisk

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"k8s.io/klog/v2"
)

// klogFlagsOnce ensures klog flags are registered on the default flag set only
// once across all tests in this file (registering twice panics).
var klogFlagsOnce sync.Once

// captureKlogAtSpanVerbosity redirects klog output to a buffer and raises the
// verbosity so span log lines (emitted at klogSpanVerbosity) are captured. It
// returns the buffer and a restore func the caller should defer.
func captureKlogAtSpanVerbosity(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	klogFlagsOnce.Do(func() {
		klog.InitFlags(nil)
	})
	if err := flag.Set("logtostderr", "false"); err != nil {
		t.Fatal(err)
	}
	if err := flag.Set("v", fmt.Sprintf("%d", klogSpanVerbosity)); err != nil {
		t.Fatal(err)
	}
	flag.Parse()

	buf := new(bytes.Buffer)
	klog.SetOutput(buf)

	return buf, func() {
		klog.Flush()
		klog.SetOutput(nil)
	}
}

// TestTracingSpansVisibleInLogs is an end-to-end check that, with tracing
// enabled, a span produced via startSpan (with attributes, an event and a
// status) is exported to the container logs by the klogSpanExporter.
func TestTracingSpansVisibleInLogs(t *testing.T) {
	buf, restore := captureKlogAtSpanVerbosity(t)
	defer restore()

	tp, err := InitOtelTracing()
	if err != nil {
		t.Fatalf("InitOtelTracing failed: %v", err)
	}
	defer func() {
		_ = tp.Shutdown(context.Background())
	}()

	// Simulate a handler: start a span, label it, record a throttle event,
	// then finish it successfully.
	ctx, span := startSpan(context.Background(), "AttachDisk",
		attribute.String(attrDiskName, "pvc-123"),
		attribute.String(attrNode, "aks-node-42"))
	recordThrottleEvent(ctx, eventThrottled, "30s")
	recordSpanResult(span, nil)
	span.End()

	// Batch span processor exports asynchronously; force a synchronous flush.
	if err := tp.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush failed: %v", err)
	}
	klog.Flush()

	out := buf.String()
	t.Logf("captured klog output:\n%s", out)

	for _, want := range []string{
		"otel trace span",
		`span="AttachDisk"`,
		`disk.name="pvc-123"`,
		`node.name="aks-node-42"`,
		eventThrottled,
		"retry_after=30s",
		"traceID=",
		"durationMs=",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected span log output to contain %q, got:\n%s", want, out)
		}
	}
}

// TestTracingDisabledEmitsNoSpans verifies that when InitOtelTracing has not
// been called (default no-op tracer), startSpan produces no log output, so
// tracing has zero impact when disabled.
func TestTracingDisabledEmitsNoSpans(t *testing.T) {
	buf, restore := captureKlogAtSpanVerbosity(t)
	defer restore()

	_, span := startSpan(context.Background(), "AttachDisk")
	span.End()
	klog.Flush()

	if strings.Contains(buf.String(), "otel trace span") {
		t.Errorf("expected no span output when tracing is disabled, got:\n%s", buf.String())
	}
}

// TestRecordThrottleIfThrottled verifies that recordThrottleIfThrottled records
// a "throttled" event carrying the parsed Retry-After back-off when the error
// is an Azure ARM throttling error, and records nothing for nil/non-throttling
// errors.
func TestRecordThrottleIfThrottled(t *testing.T) {
	buf, restore := captureKlogAtSpanVerbosity(t)
	defer restore()

	tp, err := InitOtelTracing()
	if err != nil {
		t.Fatalf("InitOtelTracing failed: %v", err)
	}
	defer func() {
		_ = tp.Shutdown(context.Background())
	}()

	throttlingErr := errors.New("could not list storage accounts for account type Premium_LRS: Retriable: true, RetryAfter: 217s, HTTPStatusCode: 429, RawError: azure cloud provider throttled for operation StorageAccountListByResourceGroup with reason \"TooManyRequests\"")

	// Throttling error: expect a "throttled" event with retry_after=217s.
	ctx, span := startSpan(context.Background(), "AttachDisk")
	recordThrottleIfThrottled(ctx, throttlingErr)
	recordSpanResult(span, nil)
	span.End()

	// Non-throttling error and nil error: expect no throttle event.
	ctx2, span2 := startSpan(context.Background(), "DetachDisk")
	recordThrottleIfThrottled(ctx2, errors.New("some transient network error"))
	recordThrottleIfThrottled(ctx2, nil)
	recordSpanResult(span2, nil)
	span2.End()

	if err := tp.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush failed: %v", err)
	}
	klog.Flush()

	out := buf.String()
	t.Logf("captured klog output:\n%s", out)

	for _, want := range []string{
		`span="AttachDisk"`,
		eventThrottled,
		"retry_after=217s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected throttled span log to contain %q, got:\n%s", want, out)
		}
	}

	// The DetachDisk span must not carry a throttle event.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, `span="DetachDisk"`) && strings.Contains(line, eventThrottled) {
			t.Errorf("did not expect a throttle event on DetachDisk span, got:\n%s", line)
		}
	}
}
