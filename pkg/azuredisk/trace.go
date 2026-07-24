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
	"context"
	"fmt"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
	"k8s.io/klog/v2"
	azureutils "sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
)

const (
	// tracerName is attached to every span as the instrumentation scope so
	// spans produced by this driver can be distinguished from library spans.
	tracerName = "sigs.k8s.io/azuredisk-csi-driver"

	// otelExporterEndpointEnv is the standard OpenTelemetry environment
	// variable that points at an OTLP collector. When it is set the driver
	// additionally exports spans to that collector; otherwise spans are only
	// written to the container logs.
	otelExporterEndpointEnv = "OTEL_EXPORTER_OTLP_ENDPOINT"

	// klogSpanVerbosity is the klog verbosity level at which spans are logged.
	// It is intentionally high so spans never appear at default logging levels
	// and only surface when an operator explicitly raises verbosity, keeping
	// trace volume controlled.
	klogSpanVerbosity = 4
)

// Span attribute keys used across handlers so root-span labeling stays
// consistent and traces are easy to correlate by disk, node or volume.
const (
	attrDiskURI    = "disk.uri"
	attrDiskName   = "disk.name"
	attrNode       = "node.name"
	attrVolumeID   = "volume.id"
	attrRetryAfter = "retry_after"

	// eventThrottled marks an ARM/library throttling back-off inside a span.
	eventThrottled = "throttled"
)

// noisySpanNames are health-check and capability-discovery RPCs that the
// kubelet and liveness-probe sidecar call on a timer. They carry no useful
// tracing signal and would otherwise flood the logs, so they are not exported
// to klog. Real operations (CreateVolume, ControllerPublishVolume, etc.) are
// unaffected.
var noisySpanNames = map[string]struct{}{
	"csi.v1.Identity/Probe":                       {},
	"csi.v1.Identity/GetPluginInfo":               {},
	"csi.v1.Identity/GetPluginCapabilities":       {},
	"csi.v1.Node/NodeGetCapabilities":             {},
	"csi.v1.Node/NodeGetVolumeStats":              {},
	"csi.v1.Controller/ControllerGetCapabilities": {},
}

// tracer returns the driver's tracer. Until InitOtelTracing installs a real
// TracerProvider this resolves to the global no-op tracer, so calling startSpan
// before (or when tracing is disabled) is safe and free.
func tracer() oteltrace.Tracer {
	return otel.Tracer(tracerName)
}

// startSpan starts a child span named name off of ctx and returns the derived
// context (carrying the new span) together with the span itself. Callers must
// call span.End() when the traced work completes, typically via defer.
//
// When tracing is disabled the global provider is a no-op, so the returned span
// does nothing and the call is effectively free. This lets handlers instrument
// I/O boundaries unconditionally without guarding every call site.
func startSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, oteltrace.Span) {
	ctx, span := tracer().Start(ctx, name)
	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}
	return ctx, span
}

// recordSpanResult sets the span status from err. On error it also records the
// error on the span. It is safe to call on a no-op span.
func recordSpanResult(span oteltrace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, err.Error())
		return
	}
	span.SetStatus(otelcodes.Ok, "")
}

// recordThrottleEvent records a library-level throttling/back-off delay as an
// event on the span currently associated with ctx, so waits caused by ARM
// "Retry-After" or client-side rate limiting are visible inside the trace.
func recordThrottleEvent(ctx context.Context, eventName, retryAfter string) {
	span := oteltrace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	attrs := []attribute.KeyValue{}
	if retryAfter != "" {
		attrs = append(attrs, attribute.String(attrRetryAfter, retryAfter))
	}
	span.AddEvent(eventName, oteltrace.WithAttributes(attrs...))
}

// recordThrottleIfThrottled inspects err and, when it is an Azure ARM
// throttling error, records a "throttled" event on the span in ctx together
// with the parsed Retry-After back-off. It is a no-op for nil/non-throttling
// errors and when tracing is disabled (non-recording span).
func recordThrottleIfThrottled(ctx context.Context, err error) {
	if err == nil || !azureutils.IsThrottlingError(err) {
		return
	}
	retryAfter := ""
	if s := azureutils.GetRetryAfterSeconds(err); s > 0 {
		retryAfter = fmt.Sprintf("%ds", s)
	}
	recordThrottleEvent(ctx, eventThrottled, retryAfter)
}

// klogSpanExporter is a trace.SpanExporter that writes finished spans to klog.
// This makes traces visible through standard container-log collection without
// requiring an OTLP collector to be deployed.
type klogSpanExporter struct{}

var _ trace.SpanExporter = (*klogSpanExporter)(nil)

// ExportSpans formats each finished span as a single structured klog line
// describing its name, duration, identifiers, attributes, events and status.
func (e *klogSpanExporter) ExportSpans(_ context.Context, spans []trace.ReadOnlySpan) error {
	logger := klog.V(klogSpanVerbosity)
	if !logger.Enabled() {
		return nil
	}
	for _, span := range spans {
		if _, noisy := noisySpanNames[span.Name()]; noisy {
			continue
		}
		logger.InfoS("otel trace span", spanKeysAndValues(span)...)
	}
	return nil
}

// Shutdown is a no-op; there is nothing to flush or close for klog output.
func (e *klogSpanExporter) Shutdown(_ context.Context) error { return nil }

// spanKeysAndValues renders a ReadOnlySpan into a flat list of key/value pairs
// suitable for klog structured logging.
func spanKeysAndValues(span trace.ReadOnlySpan) []interface{} {
	sc := span.SpanContext()
	kv := []interface{}{
		"span", span.Name(),
		"traceID", sc.TraceID().String(),
		"spanID", sc.SpanID().String(),
	}
	if parent := span.Parent(); parent.HasSpanID() {
		kv = append(kv, "parentSpanID", parent.SpanID().String())
	}
	kv = append(kv, "durationMs", span.EndTime().Sub(span.StartTime()).Milliseconds())

	for _, attr := range span.Attributes() {
		kv = append(kv, string(attr.Key), attr.Value.Emit())
	}

	if events := span.Events(); len(events) > 0 {
		var b strings.Builder
		for i, ev := range events {
			if i > 0 {
				b.WriteString("; ")
			}
			b.WriteString(ev.Name)
			for _, attr := range ev.Attributes {
				fmt.Fprintf(&b, " %s=%s", attr.Key, attr.Value.Emit())
			}
		}
		kv = append(kv, "events", b.String())
	}

	if status := span.Status(); status.Code != 0 {
		kv = append(kv, "status", status.Code.String())
		if status.Description != "" {
			kv = append(kv, "statusMessage", status.Description)
		}
	}
	return kv
}

// InitOtelTracing initializes and registers a global OpenTelemetry
// TracerProvider for the driver. Spans are always exported to the container
// logs via klog; if an OTLP endpoint is configured they are additionally
// exported to that collector. The returned TracerProvider must be shut down on
// exit to flush any buffered spans. It is only called when tracing is enabled,
// so there is zero cost when tracing is disabled.
func InitOtelTracing() (*trace.TracerProvider, error) {
	ctx := context.Background()

	// Resource will auto populate spans with common attributes.
	res, err := resource.New(ctx,
		resource.WithFromEnv(), // pull attributes from OTEL_RESOURCE_ATTRIBUTES and OTEL_SERVICE_NAME environment variables
		resource.WithProcess(),
		resource.WithOS(),
		resource.WithContainer(),
		resource.WithHost(),
	)
	if err != nil {
		klog.ErrorS(err, "Failed to create the otel resource, spans will lack some metadata")
	}

	opts := []trace.TracerProviderOption{
		trace.WithResource(res),
		// Honor OTEL_TRACES_SAMPLER* env vars when set, otherwise sample based
		// on the parent decision, keeping volume controlled.
		trace.WithSampler(trace.ParentBased(trace.AlwaysSample())),
		// Always write spans to the container logs.
		trace.WithBatcher(&klogSpanExporter{}),
	}

	// Optionally add an OTLP exporter when a collector endpoint is configured.
	// This is the one-line path to a real tracing backend.
	if endpoint := strings.TrimSpace(os.Getenv(otelExporterEndpointEnv)); endpoint != "" {
		exporter, err := otlptracegrpc.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to create the OTLP exporter: %w", err)
		}
		opts = append(opts, trace.WithBatcher(exporter))
		klog.V(2).Infof("otel tracing: exporting spans to OTLP endpoint %s", endpoint)
	}

	traceProvider := trace.NewTracerProvider(opts...)

	// Register the trace provider as global.
	otel.SetTracerProvider(traceProvider)

	return traceProvider, nil
}
