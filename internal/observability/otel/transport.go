package otel

import (
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// InstrumentedTransport wraps an http.RoundTripper and:
//
//  1. Opens a span named "traffic.request" per outbound RoundTrip,
//     CLIENT kind. The span has no parent (traffic-gen is the trace
//     root in the platform compose) so it appears in Jaeger as the
//     root of a new trace; downstream services (gateway + markup-svc)
//     join the same trace via the injected traceparent header.
//  2. Injects W3C trace context onto the outbound request via the
//     global TextMapPropagator (set up in Bootstrap).
//  3. Records the upstream response status code as
//     http.status_code; marks the span Error on 5xx + transport
//     errors so Jaeger highlights bad batches in the trace list.
type InstrumentedTransport struct {
	Tracer trace.Tracer
	Inner  http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t *InstrumentedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	inner := t.Inner
	if inner == nil {
		inner = http.DefaultTransport
	}
	if t.Tracer == nil {
		return inner.RoundTrip(req)
	}

	ctx, span := t.Tracer.Start(req.Context(), "traffic.request",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.method", req.Method),
			attribute.String("http.url", req.URL.String()),
			attribute.String("upstream.host", req.URL.Host),
		),
	)
	defer span.End()

	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	resp, err := inner.RoundTrip(req.WithContext(ctx))
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return resp, err
	}
	span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))
	if resp.StatusCode >= 500 {
		span.SetStatus(codes.Error, resp.Status)
	}
	return resp, nil
}
