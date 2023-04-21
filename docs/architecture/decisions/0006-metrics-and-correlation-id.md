# 6. Prometheus `/metrics` endpoint + outbound `X-Correlation-ID`

## Status

Accepted — `--metrics-listen` (default empty / off) starts a small HTTP server exposing `/metrics` with `trafficgen_requests_total{outcome}` counter, `trafficgen_request_duration_seconds{outcome}` histogram, and `trafficgen_target_qps` / `trafficgen_achieved_qps` gauges (refreshed on a 1 s tick). `--run-id` (default empty / off) makes the poster stamp `X-Correlation-ID: <run-id>:<seq>` on every outbound POST, where `seq` is an atomic counter; operators filter Kibana for `attrs.correlation_id : "<run-id>:*"` to see every request in a single load run.

## Context

Two operator-visibility gaps were on the platform PLAN.md menu:

1. **No traffic-gen `/metrics`.** The achieved-vs-requested QPS was visible only in the poster's per-run stderr summary at the end of the run — useless for live dashboards or for detecting "the load generator can't keep up with its own rate profile" mid-run.
2. **No outbound correlation ID.** Today the gateway mints a UUID v4 per request (see decision-gateway/ADR-0001). A long run distributes thousands of orphan UUIDs across Kibana — operators can find a single request by its ID but cannot answer "show me every request from the 14:30 run." Setting a per-run prefix and a monotonic sequence makes runs trivially groupable.

Both items pair naturally — they're operator-side fact-collection that the load generator owns and that the rest of the platform already understands the shape of (Prometheus exposition + the existing `X-Correlation-ID` propagation contract).

## Decision

`internal/observability/metrics` ships a Sink + handler:

- `trafficgen_requests_total{outcome}` — counter, 5 outcomes (`success` / `no_match` / `client_error` / `server_error` / `transport_error`) matching the poster Summary buckets verbatim.
- `trafficgen_request_duration_seconds{outcome}` — histogram, 13 buckets covering 0.5 ms – 5 s.
- `trafficgen_target_qps` — gauge, refreshed every 1 s with `profile.QPS(elapsed)`.
- `trafficgen_achieved_qps` — gauge, refreshed every 1 s as `(total - prev_total)` from the request counter.

`poster.Config` gains `Metrics metricsSink` (interface) + `CorrelationIDPrefix string`. The Sink is plugged in only when the operator enables `--metrics-listen`; the legacy `nilSink{}` is the zero-value path so existing consumers keep working without changes.

`cmd/traffic-gen` adds:

- `--metrics-listen` — when non-empty, starts a goroutine HTTP server on that address and a gauge-refresh ticker.
- `--run-id` — when non-empty, threads through to `Config.CorrelationIDPrefix`. The poster's per-request `post()` closure sets `X-Correlation-ID: <prefix>:<atomicCounter>` before the outbound `Do`.

The achieved-QPS gauge uses `Sink.Total()` to sum the counter vector across outcome labels — a single Prometheus collector dance per tick.

## Consequences

### Closed

- Live operator visibility into achieved-vs-requested QPS. A Grafana panel plotting both gauges side by side immediately surfaces "the generator is falling behind its profile" (slow target, exhausted file descriptors, etc.) without waiting for the run to end.
- Per-run correlation. `attrs.correlation_id : "build-1234:*"` in Kibana shows every request emitted by run `build-1234`. Combined with the existing trace_id → Jaeger URL template (pricing-observability/ADR-0010), an operator can pick any one and click through to the matching trace.
- Backward compat preserved. Both flags default off; binaries upgrading from v0.0.4 with no flag changes behave identically.

### Not closed

- Sampling on the histogram. At 500 QPS for 5 min, the histogram absorbs 150k observations on 5 outcomes × 13 buckets = ~10k series writes — still well below any production limit, but a future tail-sampling option (`--metrics-histogram-sampling=0.1`) lands when the volume genuinely costs.
- Connection-level metrics (in-flight requests, queue depth at the rate-pacing loop). Out of scope today.
- Auto-generating a `run-id` when not set (e.g., from `$BUILD_ID`, `$HOSTNAME`). Trivial to do at the operator's shell level; resist adding it to the binary because the legacy "let the gateway mint correlation IDs" path is still useful for one-off curl probes against the gateway.
