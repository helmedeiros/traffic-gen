// Package otel bootstraps the OTel SDK for traffic-gen and provides
// an HTTP RoundTripper that opens a root span per outbound request +
// injects the W3C trace context header so downstream services join
// the same trace. See ADR-0004.
package otel

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Shutdown is the cleanup function returned by Bootstrap.
type Shutdown func(ctx context.Context) error

// Bootstrap initialises an OTel TracerProvider with an OTLP gRPC
// exporter, sets it as the global provider, and registers the W3C
// TraceContext + Baggage propagator globally. traffic-gen is the
// trace root in the platform compose: when the InstrumentedTransport
// opens a span for each outbound POST, the gateway extracts the
// resulting traceparent header and continues the trace; markup-svc
// extracts it from the gateway's proxied request and the whole chain
// renders as one trace in Jaeger.
//
// Reads the standard OTel SDK env vars (OTEL_EXPORTER_OTLP_ENDPOINT,
// OTEL_SERVICE_NAME, OTEL_RESOURCE_ATTRIBUTES, etc.) so an operator
// configuring the target collector + service name works without code
// changes.
func Bootstrap(ctx context.Context, instrumentationName string) (trace.Tracer, Shutdown, error) {
	exp, err := otlptrace.New(ctx, otlptracegrpc.NewClient())
	if err != nil {
		return nil, nil, fmt.Errorf("otlptrace gRPC exporter: %w", err)
	}
	res, err := resource.New(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("resource detection: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	return tp.Tracer(instrumentationName), tp.Shutdown, nil
}
