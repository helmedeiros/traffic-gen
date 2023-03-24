# 4. OTel root spans + W3C trace context emission per outbound POST

## Status

Accepted — `--otel-enabled` bootstraps the OTel SDK (OTLP gRPC exporter + batched TracerProvider + detected resource + W3C TraceContext+Baggage propagator), wraps the poster's HTTP client `Transport` with an `InstrumentedTransport` that opens one root span per outbound RoundTrip named `traffic.request` and injects the W3C `traceparent` header before the request goes out the wire. traffic-gen becomes the trace root of the platform pipeline: the gateway extracts the propagated traceparent and continues the chain; markup-svc extracts it from the gateway's proxied request; the whole `traffic-gen → decision-gateway → markup-svc` waterfall renders as a single trace in Jaeger UI, with each component's per-request cost visible as adjacent spans.

## Context

decision-gateway ADR-0002 + markup-svc ADR-0017 closed the gateway-side + engine-side spans + W3C trace context propagation across those two services. With both shipped, a `curl /decide` against the gateway produced a 5-span trace (gateway.request → gateway.proxy.upstream → markup.decider.decide → markup.guardrails.check (when active) → markup.engine.evaluate). That answers "where is the time spent" for one-off operator requests.

The operator's actual question, though, is what happens at sustained load — the traffic-gen rate ramps from 10 QPS to 500 QPS over five minutes (the default profile in the platform compose) and the question becomes "how does the cost shift as load changes." That question needs each generated request to be a trace, with traffic-gen as the root, so Jaeger's trace-list view shows the load shape and Jaeger's individual-trace view shows the per-request waterfall. Today traffic-gen emits no spans; Jaeger sees only the gateway as the trace root for any traffic-gen-originated request, and the trace doesn't carry the traffic-gen-side signal (which preset, which rate-profile time slice, which Generator-seeded request body shape).

Three downstream needs gate on this ADR:

1. **Sustained-load bottleneck investigation.** The user explicitly named "performance of markup-svc and api gateway so we can improve bottlenecks." That investigation IS the under-load behavior. Per-request traces rooted at traffic-gen give the operator the load-vs-latency view that one-off curls cannot.
2. **Trace-list selectivity on traffic-gen attributes.** A future operator wanting to see traces JUST for the `stress-no-match` preset filters by `traffic.preset=stress-no-match` in Jaeger. That attribute exists only if traffic-gen emits the span. Same for rate-profile slice (`traffic.rate.kind=exp`, `traffic.rate.qps_at_start=10`, `traffic.rate.qps_at_end=500`).
3. **Cross-platform trace correlation reaches the origin.** Today's chain stops at the gateway. With traffic-gen-side root spans, the chain is complete: every span in every request shares one trace ID derived from the traffic-gen client-span ID. Operators reading Jaeger never have to guess at "which request produced this span."

Two design questions.

### 1. Span emission at the poster layer vs at the HTTP Transport layer

The poster's `Run` loop builds the `http.Request`, calls `client.Do(req)`, classifies the response. Two reasonable places to emit a span:

- **Poster layer**: in `Run`'s `post()` closure, around the `client.Do` call. Pros: the span lifetime exactly covers the operator-visible request behavior, including pre-send marshal time + post-receive classify time. Cons: the poster package now depends on `go.opentelemetry.io/otel/trace`, which it didn't before. Tests for the poster grow OTel setup.
- **Transport layer**: wrap the HTTP client's `Transport` with an `InstrumentedTransport` that opens a span at RoundTrip time. Pros: the poster stays OTel-free; the cmd wires the OTel-aware client into `poster.Config.Client` exactly as it wires the timeout-aware client today. The span lifetime matches the wire-level RoundTrip window (which is what operators want — the on-wire cost is what dominates).
- The marshal/classify time is sub-microsecond compared to the wire RoundTrip; bundling it into the span is OK but not necessary.

**Pick Transport layer.** Keeps the poster package OTel-free (matches the project's hexagonal posture — adapters wrap transports, not domain logic). Matches the decision-gateway pattern (its outbound spans live in `InstrumentedTransport` too). The span lifetime is right for the operator's question. The cmd wiring change is one line (wrap `http.DefaultTransport` with `&tgotel.InstrumentedTransport{Tracer, Inner: ...}` when `--otel-enabled` is set).

### 2. Root span per request vs one root per Run + one child per request

A long-running traffic-gen process emits many requests. Two structures:

- **One root per request**: each outbound POST creates a new root span. Pros: each trace is one request, matching Jaeger's "trace = one transaction" mental model; trace-list view shows N traces for N requests; the gateway extracts the traceparent and continues each trace independently. Cons: no parent-child link between requests issued in the same Run; an operator wanting "all requests from this ramp" filters by `service.name=traffic-gen` + a time window.
- **One root per Run + one child per request**: the Run starts a long-lived root span; each request becomes a child. Pros: structural grouping in Jaeger ("here are all the traces from one ramp"). Cons: Jaeger's UI shows one giant trace (potentially with thousands of children) for a typical 5-minute ramp; the rendering breaks down past a few hundred spans; the gateway becomes a grandchild instead of a child of the immediate caller, which is the wrong mental model (the gateway is a hop, not a leaf inside traffic-gen).

**Pick one root per request.** Jaeger's trace = one transaction model wins. The grouping-by-ramp question is answered by filtering on `service.name + time window` in the trace list; that filter is cheap and Jaeger's UI is built for it. The structural-grouping benefit doesn't survive contact with Jaeger's UI at typical traffic volumes.

## Decision

`internal/observability/otel` is the new package. Two files:

- `bootstrap.go`: `Bootstrap(ctx, instrumentationName) (trace.Tracer, Shutdown, error)`. Same shape as decision-gateway and markup-svc's Bootstrap functions — OTLP gRPC exporter, batched TracerProvider, detected resource, W3C TraceContext+Baggage propagator. Lifted as-is rather than dep'd from one of the other repos (each binary owns its OTel boot path; cross-repo Go module dependencies for 50-line bootstrap helpers are not worth the version-coupling).
- `transport.go`: `InstrumentedTransport{Tracer, Inner}` implements `http.RoundTripper`. Opens a root `traffic.request` span (CLIENT kind), injects W3C traceparent via the global propagator, records `http.method` / `http.url` / `upstream.host` / `http.status_code` attributes, marks 5xx + transport-error responses as Error span status.

`cmd/traffic-gen/main.go` gains the `--otel-enabled` bool flag. When set, the cmd:

1. Calls `tgotel.Bootstrap` and registers the 5-second shutdown defer.
2. Wraps `http.DefaultTransport` with `&tgotel.InstrumentedTransport{Tracer: t, Inner: http.DefaultTransport}`.
3. Passes the wrapped transport into the `http.Client` that goes into `poster.Config.Client`.

When `--otel-enabled` is not set, the cmd uses the plain `http.DefaultTransport` and no OTel imports are reached at runtime (the package is imported but the Bootstrap path is not invoked — the OTel dep tree's gRPC init does run at process start but only the parts needed for stdlib HTTP).

Docker compose (in the decision-gateway repo's `docker-compose.yaml`) wires the traffic-gen service with the standard OTel env vars: `OTEL_SERVICE_NAME=traffic-gen`, `OTEL_EXPORTER_OTLP_ENDPOINT=http://host.docker.internal:4317`, `OTEL_EXPORTER_OTLP_PROTOCOL=grpc`, `OTEL_TRACES_SAMPLER=parentbased_always_on`, `extra_hosts: host.docker.internal:host-gateway`. The command-line passes `--otel-enabled`. This is a follow-up commit in the decision-gateway repo since that's where the canonical platform compose lives.

## Consequences

### Closed by this ADR

- The Jaeger UI service list completes: `[traffic-gen, decision-gateway, markup-svc]`. Every traffic-gen-originated request renders as one trace with traffic-gen as the root and the full waterfall (gateway → gateway upstream → markup-svc decider → guardrails → engine) underneath.
- Sustained-load investigation. The trace-list view shows the load shape (request count over time) and per-trace latencies. An operator filtering by `service.name=traffic-gen` sees only the synthetic traffic; filtering by `name=traffic.request AND duration > 100ms` highlights the slow requests at this QPS slice.
- Cross-component cost attribution complete from origin. The operator's "where is the cost" question is answerable end-to-end: traffic-gen-side request build (negligible, the span starts at RoundTrip) → network → gateway routing → upstream call → markup-svc engine → upstream response → network → traffic-gen response classification.
- Pricing-observability ADR-0002's recipe — bring up both stacks, send requests, see traces — works against the synthetic load too; operators do not have to know about traffic-gen's separate OTel wiring.

### NOT closed by this ADR

- Per-rate-profile slice attributes on the span. The current span emits HTTP-level attrs only; an operator wanting `traffic.profile=exp:10->500@5m` as a span attribute (so trace queries can filter by profile shape) needs a follow-up that adds traffic-gen-domain attributes to the span before Inject. The wire-level attrs cover the immediate need.
- Per-preset attributes (`traffic.preset=default`, `traffic.preset.persona=enterprise`). Same shape as the previous item; ships when an operator's trace query motivates it.
- Trace sampling at the head. v0.0.3 samples 100% (the SDK default + the env var `OTEL_TRACES_SAMPLER=parentbased_always_on`). A real production ramp at 500 QPS for 5 minutes = 150k spans; the OTel Collector's batched exporter handles this, but at 5000 QPS for an hour = 18M spans and the volume motivates head sampling. Lands when an operator's load profile exceeds the dev posture.
- Cross-trace baggage. The Bootstrap installs the Baggage propagator, but traffic-gen does not set any baggage today. An operator wanting to tag all requests in one ramp with `ramp_id=<uuid>` would use baggage; ships when needed.
- traffic-gen's own JSON access log (the per-request log line) does not yet include `trace_id` + `span_id`. Phase 4 of the cross-service tracing rollout (the log-trace correlation ADR in pricing-observability) covers this; the work is the same shape as in decision-gateway + markup-svc.

### Performance impact

- `--otel-enabled` not set: zero ns delta vs the pre-ADR binary. The default `http.Transport` is used; no OTel code in the request path.
- `--otel-enabled` set, OTLP collector reachable: per request adds one span open + close pair (~50 ns) + one propagator Inject on the request headers (~50 ns). Aggregate ~100 ns per request. The batched span processor's submit is async; the hot path does not block on the gRPC export.
- `--otel-enabled` set, collector unreachable: the batched processor's queue grows to `MaxQueueSize` (default 2048 spans) then drops with a warning. traffic-gen's send loop keeps running; the operator sees lost spans in Jaeger.

At the platform's default profile (500 QPS for 5 minutes), the volume is ~150k spans. The OTel Collector's default `batch` processor (no processors at v0.0.2 in pricing-observability) handles this transparently; Jaeger's Elasticsearch backend writes ~150k docs which fits in the default `jaeger-span-*` index shard.

### Validation strategy

- Unit test in `internal/observability/otel/transport_test.go`: outbound request gets a `traceparent` header derived from the emitted span's trace ID; the span is a root (no valid parent context); span name is `traffic.request`; `http.status_code` attribute matches the upstream response.
- Integration smoke against the live platform stack: bring up pricing-observability + decision-gateway compose with traffic-gen `--otel-enabled` + OTel env vars; let the default 5-min ramp run; open Jaeger UI service `traffic-gen`; observe many traces, each with the 5-span waterfall through gateway + markup-svc; click any trace and see `traffic.request → gateway.request → gateway.proxy.upstream → markup.decider.decide → markup.guardrails.check (when on) → markup.engine.evaluate`.
- The smoke is documented in pricing-observability's next release notes as the v0.0.2 + traffic-gen-v0.0.3 verification recipe.
