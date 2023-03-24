package otel_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tgotel "github.com/helmedeiros/traffic-gen/internal/observability/otel"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// Test_InstrumentedTransport_EmitsRootSpan_InjectsTraceparent asserts
// the round-tripper opens a root span (no parent) named traffic.request,
// injects a traceparent matching that span's trace ID + span ID, and
// records the upstream status code.
func Test_InstrumentedTransport_EmitsRootSpan_InjectsTraceparent(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tracer := tp.Tracer("test")

	captured := ""
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Get("traceparent")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	rt := &tgotel.InstrumentedTransport{Tracer: tracer, Inner: http.DefaultTransport}
	client := &http.Client{Transport: rt}

	resp, err := client.Post(upstream.URL, "application/json", strings.NewReader(`{"k":"v"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	span := spans[0]
	if span.Name != "traffic.request" {
		t.Errorf("span name = %q, want traffic.request", span.Name)
	}
	if span.SpanKind != oteltrace.SpanKindClient {
		t.Errorf("span kind = %v, want Client", span.SpanKind)
	}
	if span.Parent.IsValid() {
		t.Errorf("expected root span (no valid parent), got parent %v", span.Parent)
	}
	if !strings.HasPrefix(captured, "00-"+span.SpanContext.TraceID().String()+"-") {
		t.Errorf("traceparent on upstream = %q, want 00-%s-...", captured, span.SpanContext.TraceID().String())
	}
	got := map[attribute.Key]attribute.Value{}
	for _, a := range span.Attributes {
		got[a.Key] = a.Value
	}
	if got["http.status_code"] != attribute.IntValue(200) {
		t.Errorf("http.status_code = %v, want 200", got["http.status_code"])
	}
}
