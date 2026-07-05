# 9. Session driver: `--session=N` funnel-oriented state machine

## Status

Accepted — traffic-gen grows a session-mode driver alongside the existing rate profiles (steady / linear / exp per ADR-0003). When `--session=N` is set, traffic-gen drives `N` concurrent virtual users through a full search → book → purchase (or timeout) journey against a `funnel-sim` instance, emitting no events of its own but populating the funnel-events bucket via funnel-sim's schemas. The rate profiles remain the mode of choice for pricing-throughput measurement against markup-svc directly.

## Context

Arcs 1 and 2 of the Learning Loop arc landed markup-svc ADR-0037 (`decision_id` in `/decide` response + `journey_id` in `request_context`) and the funnel-sim service (five endpoints, in-memory state, `search.v1` + `booking.v1` typed sinks). Both are wire-contract complete but neither can prove itself against a realistic customer flow without a driver that speaks the funnel-sim contract.

Options considered:
1. **New standalone binary (`funnel-driver`).** Cleanest separation; heaviest yak (new repo, new build, new docker image).
2. **Extend traffic-gen with a session mode.** traffic-gen is already the load driver for the platform. Its OTel, metrics, and jsonlog scaffolding transfer wholesale. Reuses one docker image, one operator learning curve, one metrics registry.
3. **Have funnel-sim self-driven with an internal load generator.** Blurs the load-driver / service-under-test boundary. Rejected — funnel-sim IS the service under test; nothing about it should generate traffic.

Choice (2). This ADR.

## Decision

### The session profile

`--session=N` runs N concurrent virtual users. Mutually exclusive with `--qps` and `--profile`. Each user runs the following state machine independently:

```
1. mint journey_id (32-char hex)
2. POST /search  (returns N offers, each with decision_id)
3. with probability P_click, pick an offer (weighted by 1/position)
   — else END (silent no_click)
4. POST /booking/init  (returns booking_id)
5. sleep think_time_init (uniform in [ThinkInitMin, ThinkInitMax])
6. with probability P_walk_at_init:
     POST /booking/timeout  → END (walk_init)
7. POST /booking/reserve
8. sleep think_time_reserve
9. with probability P_walk_at_reserve:
     POST /booking/timeout  → END (walk_reserve)
10. POST /booking/purchase  → END (purchased)
```

### Configuration surface

```
--session=N                           # N concurrent virtual users
--session-funnel-url=URL              # funnel-sim base URL
--session-p-click=0.15                # P(user clicks any offer)
--session-p-walk-at-init=0.30         # P(user walks after init)
--session-p-walk-at-reserve=0.20      # P(user walks after reserve)
--session-think-init-min=500ms
--session-think-init-max=3s
--session-think-reserve-min=1s
--session-think-reserve-max=5s
--session-offers-per-search=8
--session-customer-tier=enterprise    # stamped on the search query
--session-country=DE
--session-route=BR-DE
```

Default probabilities yield ~8.4% end-to-end conversion (15% click × 70% survive init × 80% survive reserve), matching a mid-funnel range operators tune per-scenario.

### Zero-value semantics

Probability and think-time zeros are HONORED, not defaulted:
- `--session-p-click=0` → never click (all searches → no_click)
- `--session-p-walk-at-init=0` → never walk at init
- `--session-think-init-max=0` → no sleep between init and reserve

This is important for tests and for extreme-case scenarios. The CLI default flag values give the realistic-mixture baseline; the internal `applyDefaults` only fills the offers-per-search when unset.

### Offer selection: 1/position weighting

Once the user "clicks", one offer is picked with probability weighted by `1/position`. Rough model of eye-tracking bias toward the top of the page. Real search-svc will emit ranking scores; a future extension can honor those instead of the position heuristic.

### Statistics endpoint

On shutdown (SIGINT, SIGTERM, or `--duration` elapsed) traffic-gen emits `traffic-gen.session.done` with atomic counters for every terminal outcome:

```json
{
  "msg": "traffic-gen.session.done",
  "attrs": {
    "searches": 1234,
    "no_click": 1048,
    "init":     186,
    "walk_init": 55,
    "reserve":  131,
    "walk_reserve": 26,
    "purchased": 105,
    "errors": 0
  }
}
```

An operator computes the funnel drop-off percentages from those counters; Prometheus counters (per-request `traffic_gen_requests_total` from ADR-0006) provide the rate view.

## Consequences

### Positive
- Full customer-journey coverage without a new binary. `docker compose up traffic-gen` with `--session=N` immediately populates funnel-events + markup-decisions with correlated events. The DuckDB elasticity query becomes runnable end-to-end.
- Existing OTel + metrics + jsonlog scaffolding transfers verbatim. Each session's HTTP calls carry a fresh traceparent that funnel-sim + markup-svc join, so the traffic-gen → funnel-sim → markup-svc trace renders in Jaeger.
- Session probabilities and think times are per-flag, so an operator running an A/B lift simulation can spin two traffic-gen instances at different `--session-p-walk-at-init` values against different `--target` routes and read the funnel drop-off signal directly.

### Negative
- One more mode to reason about. `--session` is orthogonal to `--profile` / `--qps` (mutually exclusive at parse time), which is a fifth top-level operating mode. Documented in this ADR and in the README profile-selection matrix.
- No per-user OTel span linking the whole session together yet. Each HTTP call inherits a fresh traceparent; a per-session parent span would need a `journey.session` span opened around `runOneJourney` and injected on every child call. Parked (see Not-closed).

### Not closed
- **Per-session parent span.** A `traffic.session` span wrapping the state machine so Jaeger shows one journey as one trace instead of five disconnected traces. Deferred with an explicit trigger: added when the arc lands a DuckDB EDA notebook that consumes trace_id for journey-level debugging.
- **Session-level Prometheus counters.** `traffic_gen_session_terminated_total{outcome}` reporting the same counters the shutdown log emits, but in real-time. Deferred; the shutdown log is enough for v1.
- **Multi-route / multi-tier session mixes.** A single traffic-gen instance today drives one (customer_tier, country, route) tuple. A future extension takes a JSON mix so one process simulates diversified customer populations. Parked.

## References
- funnel-sim ADR-0001 — the service this driver targets.
- funnel-sim ADR-0002, ADR-0003 — the wire contracts the driver produces.
- markup-svc ADR-0037 — `decision_id` in response, the load-bearing join key each session picks up from funnel-sim's `/search` response.
- `~/Code/workspaceBRE/learning-loop-plan.local.md` §traffic-gen session driver.
