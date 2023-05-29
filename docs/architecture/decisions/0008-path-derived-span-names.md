# 8. Path-derived span names in the instrumented transport

## Status

Accepted — `internal/observability/otel.SpanNameFor(path)` maps an outbound URL path to an operation name that distinguishes business-rule decisions from admin operations. `/decide → traffic.decide`; `/admin/* → traffic.admin.*`; unknown paths keep the legacy `traffic.request`. The `InstrumentedTransport.RoundTrip` calls `SpanNameFor(req.URL.Path)` instead of the constant `"traffic.request"` when starting the client span. Same span shape — just a different name.

## Context

Per-trace audit work (see decision-gateway/ADR-0009, markup-svc/ADR-0028, pricing-observability/ADR-0018) made admin paths fully observable end-to-end. But the Jaeger search-results list still grouped every traffic-gen-originated trace under the single operation name `traffic.request`, regardless of whether the outbound was `/decide` or `/admin/reload`. The operator-visible consequence:

- Sorting by Most Recent in Jaeger surfaces a mix of /decide and /admin traces with no visible distinguisher in the list itself — only the span-count + service-badge mix gives a hint (5 spans + markup-svc(2) → /decide; 4 spans + markup-svc(1) → /admin/reload post-v0.1.18). That's archaeology, not observability.
- Service Performance Monitoring (SPM) tracked one timeseries `traffic.request` aggregating both flows. /admin/reload's higher tail (disk + Diagnose + Swap, observed 23 ms cold-read) was averaged with /decide's sub-millisecond engine cost into a single p99 number that misrepresents both. An admin-path regression would never surface as a separate SPM signal.

The fix is one line at the span-start site: derive the operation name from `req.URL.Path` instead of using a constant.

### Mapping choice

```
/decide        → traffic.decide
/admin/<x>/<y> → traffic.admin.<x>.<y>
everything else → traffic.request   (legacy name preserved)
```

- `/decide` is the dominant business-rule path; promoting it to its own operation makes the SPM `traffic.decide` p99 line the customer-visible latency signal.
- `/admin/*` paths split into one operation per endpoint (`traffic.admin.reload`, `traffic.admin.diagnose`, `traffic.admin.guardrails`, `traffic.admin.routes`). The slash → dot conversion preserves the hierarchy as it reads.
- Unknown paths fall through to `traffic.request` so legacy dashboards / saved searches keep working. A future operator who hits `/healthz` or any new endpoint sees the legacy name until an explicit mapping is added.

### Considered alternative

Instead of mapping paths to span names, leave the span name as `traffic.request` and rely on the `http.url` attribute in Jaeger search. Rejected: SPM doesn't slice by tag values; only by operation name. The tag-only approach would have kept the SPM aggregation problem.

## Decision

`internal/observability/otel/transport.go` gains:

```go
func SpanNameFor(path string) string {
    switch {
    case path == "/decide":
        return "traffic.decide"
    case strings.HasPrefix(path, "/admin/"):
        return "traffic.admin" + strings.ReplaceAll(strings.TrimPrefix(path, "/admin"), "/", ".")
    default:
        return "traffic.request"
    }
}
```

`InstrumentedTransport.RoundTrip` invokes `SpanNameFor(req.URL.Path)` when starting the span. All other attributes (`http.method`, `http.url`, `upstream.host`, `http.status_code`) are unchanged. SpanKind stays Client. W3C trace-context propagation unchanged.

### Tests

`internal/observability/otel/transport_test.go`:

- `TestSpanNameFor` — table-driven over `/decide`, `/admin/reload`, `/admin/diagnose`, `/admin/routes`, `/admin/guardrails`, `/healthz`, `/`, `""`.
- `TestInstrumentedTransport_SpanNameMatchesPath` — wires the InstrumentedTransport against a stub upstream serving both `/decide` and `/admin/reload`, asserts the recorded spans carry the path-derived names.
- Existing `Test_InstrumentedTransport_EmitsRootSpan_InjectsTraceparent` still passes: it POSTs to `upstream.URL` (path `/`) which falls through to `traffic.request`, preserving the original assertion.

## Consequences

### Closed

- Jaeger search-results list shows distinct operation names per flow. Operators sorting by Most Recent can tell admin from /decide at a glance.
- Jaeger SPM tracks each path's p99 / p95 / error rate separately. An admin-path regression (slow CSV parse, Diagnose slowdown, Swap contention) surfaces on its own timeseries instead of being averaged into /decide.
- Runbook Jaeger deep-links (per pricing-observability/ADR-0017) become more useful: a runbook can link to `operation=traffic.decide` for the business-rule view and `operation=traffic.admin.reload` for the admin view — each loads pre-filtered Discover-style.
- The pricing-observability tail-sampling policy (ADR-0018) keeps working: the policy matches on the `http.url` attribute, not the span name, so the 100%-of-`/admin*` behaviour is preserved.

### Not closed

- New admin endpoints get a new operation name automatically. At our 3-endpoint admin surface that's fine; at >20 admin endpoints, SPM's per-operation cardinality might become noisy. Mitigation if/when it bites: an allowlist of named operations, with everything else falling back to `traffic.request`.
- Span-name changes don't migrate old data. Historical traces in Elasticsearch keep their `traffic.request` name; new traces use the new names. Jaeger UI shows both during the transition window.
- Cookbook recipes referencing the legacy `traffic.request` operation name need a sweep. Out of scope for this ADR; a doc-only follow-up updates the references.

### Performance impact

Zero meaningful overhead. `SpanNameFor` is a path comparison + at most one string concatenation; no allocations for `/decide` (returns a constant), one small allocation for `/admin/*` (concatenation result). At traffic-gen's 500 QPS the additional work is a few microseconds total per second.
