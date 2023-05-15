# 7. Admin-path background mix

## Status

Accepted — `cmd/traffic-gen` gains two flags `--admin-target=<URL>` (default empty) + `--admin-interval=<duration>` (default 30s). When `--admin-target` is set, a background goroutine POSTs to that URL once per interval for the lifetime of the run. Wired in `cmd` via `internal/traffic/adminmix.Run`, which validates the config, posts with empty body, and records per-outcome metrics through the same `metricsSink` the poster uses. Errors propagate as `ErrTargetRequired` / `ErrIntervalRequired`.

## Context

pricing-observability/ADR-0009 added `p99 by route` and `p95 by route` panels to the gateway dashboard. With traffic-gen only hitting `/decide`, the `/admin` lines on those panels render NaN — there is no admin traffic to chart. Operators reviewing the dashboard in steady state see a half-empty view; operators investigating an admin-path incident have no baseline.

Two ways to populate the `/admin` panels:

### 1. Real operator traffic during dashboard review

Wait for a real `/admin/reload` or `/admin/routes` POST to happen. The dashboard then shows that one event and decays to NaN until the next.

Pros: zero new infrastructure.
Cons: dashboard is empty most of the time; latency baseline never establishes; an actual admin-path slowdown is hard to spot against a blank panel.

### 2. Synthetic admin traffic from traffic-gen

A background loop posts to a configurable admin URL at a low fixed rate (default 30s, well below the `for: 1m` clock on `AdminHotReloadRejected`). The dashboards show a continuous baseline. Real operator traffic on top is still visible — the synthetic baseline is one or two events per minute, real bursts dwarf it.

Pros: dashboards always charted; baseline for latency p99; no risk of misfiring `AdminHotReloadRejected` (the synthetic posts hit a valid endpoint and return 200).
Cons: adds one background goroutine to traffic-gen; adds two operator-visible flags; the synthetic traffic shows up in production-equivalent compose logs.

**Pick option 2.** Operational continuity wins. The configurability (URL + interval) keeps it opt-in; the default empty URL preserves the existing behavior for operators who don't want the mix.

### Endpoint choice

The default operator path is `http://decision-gateway:8090/admin/reload`. The markup-svc reload endpoint re-reads its source CSV regardless of POST body, so an empty body is a valid no-op when the CSV hasn't changed. Returns 200. Carries the `/admin` route label on the gateway. Idempotent across millions of repeats.

Considered and rejected: posting to `/admin/diagnose` (GET-only) or `/admin/routes` (would replace the live routes table on every tick — bad). `/admin/reload` is the only endpoint whose semantics tolerate continuous re-firing.

## Decision

`internal/traffic/adminmix`:

```go
type Config struct {
    TargetURL string
    Interval  time.Duration
    Client    *http.Client
    Out       outcomeSink  // optional metricsSink-compatible interface
}

func Run(ctx context.Context, cfg Config) error
```

`Run` validates config, creates a ticker at `cfg.Interval`, and POSTs to `cfg.TargetURL` on each tick until ctx is done. Each POST is observed via `Out.RecordOutcome(<bucket>, <duration>)` where bucket is `success` / `client_error` / `server_error` / `transport_error` / `build_error`. The bucket strings match the poster's existing outcome vocabulary so a single `tgmetrics.Sink` collects both signals into the same `traffic_gen_requests_total{outcome=...}` counter.

`cmd/traffic-gen` wires:

```go
if *adminTarget != "" {
    go func() {
        _ = adminmix.Run(ctx, adminmix.Config{
            TargetURL: *adminTarget,
            Interval:  *adminInterval,
            Client:    httpClient,
            Out:       metricsSink,
        })
    }()
}
```

Reuses the existing `httpClient` (same timeout, transport, OTel instrumentation) so admin POSTs join the same trace context the main poster emits. The goroutine exits naturally when ctx cancels.

The compose-stack default in `docker-compose.yaml` (pricing-observability + decision-gateway) ships `--admin-target=http://decision-gateway:8090/admin/reload --admin-interval=30s` so dashboards have continuous data out of the box.

## Consequences

### Closed

- `p99 by route` and `p95 by route` panels on the gateway dashboard show continuous `/admin` lines. Operators reviewing the dashboard see real baselines.
- Admin-path latency regressions become visible (a markup-svc reload-handler slowdown was previously invisible until a real operator reload).
- Cost: ~2 requests per minute per traffic-gen instance. At the default 30s interval this is below the `for: 1m` AlertManager clock on every relevant alert.

### Not closed

- Admin endpoints other than `/admin/reload`. Posting to `/admin/routes` would replace the live route table on every tick; posting to `/admin/guardrails` would do the same for guardrails. Both are configuration writes, not no-ops, and need a different design (a snapshot of the current state, posted back to verify round-trip). Out of scope.
- 4xx generation for `AdminHotReloadRejected` exercise. The synthetic POSTs return 200 by design. Operators wanting to smoke-test the alert do it manually via a corrupted rules file (the existing recipe in the cookbook).
- Cross-traffic-gen coordination. Two traffic-gen instances posting at the same interval double the admin load. Today there is one instance per compose stack; if a future deployment runs multiple, the operator either uses different intervals or runs adminmix on only one instance.

### Performance impact

- adminmix overhead: one ticker, one in-flight POST every `Interval`. At the default 30s interval, ~2 RPM. Negligible against the main `--qps=100` poster.
- markup-svc reload cost: one CSV re-read per tick. The on-disk CSV is small (< 1 KB in the canonical fixture) and the parser is sub-millisecond. Diagnose runs in `--diagnose=on` mode but on a clean rule set returns Healthy immediately. Sub-ms per tick.
- Metric cardinality: zero new labels. The `outcome` label values already exist via the main poster's counters.
