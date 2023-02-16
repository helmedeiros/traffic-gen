# 3. Rate Profiles for Variable-QPS Long Runs

## Status

Accepted — `internal/traffic/rate` ships the `RateProfile` port plus three concrete profiles (`SteadyProfile`, `LinearProfile`, `ExponentialProfile`) with a `Parse` DSL covering `steady:N`, `linear:A->B@T`, and `exp:A->B@T`. `internal/traffic/poster.Config` swaps `QPS int` for `Profile rate.RateProfile`; the Poster's ticker-based loop becomes a sleep-until loop that recomputes the inter-send interval on every send by asking the profile for `QPS(elapsed)`. `cmd/traffic-gen` gains `--profile=spec`; the existing `--qps=N` becomes the steady-profile alias for backward compat. Both flags at once produce a boot error mirroring markup-svc's `--rules`/`--snapshot`/`--route` mutual-exclusion pattern. The boot JSON event describes the parsed profile under `attrs.profile.kind` / `attrs.profile.start_qps` / `attrs.profile.end_qps` / `attrs.profile.duration` so structured-log queries identify the shape that produced any subsequent traffic. `TestRunHonorsLinearRamp` (poster) and `TestRunCmdDurationTruncatesProfile` (cmd) pin the asymmetric end-to-end proof that the rate profile drives the loop and that `Profile.Duration` composes with cmd `--duration` on whichever-fires-first.

## Context

traffic-gen v0.0.1 ships a single-goroutine Poster paced by a `time.NewTicker(1s / QPS)`. That works for steady-state load measurement but does not exercise markup-svc the way real production traffic does. Production traffic shapes operators care about:

- **Linear ramps**: a marketing campaign goes live and request volume climbs from 100 to 5,000 RPS over fifteen minutes. The interesting question is whether markup-svc's per-rule latency holds steady through the ramp.
- **Exponential ramps**: a thundering-herd event after a partial outage — load climbs from 10 to 10,000 RPS in five minutes. The interesting question is at what RPS the indexed engine's cache warms up and at what RPS the swap-decider's lock pair starts showing in dashboards.
- **Long-run steady-state**: leave the system at 500 RPS overnight. Surface memory leaks, goroutine leaks, file-descriptor exhaustion that the single-fixture-row scientific harness cannot see.

All three shapes share one property: the QPS knob varies over time. The v0.0.1 Poster's `time.NewTicker(1s/QPS)` is the wrong abstraction because the ticker fires at a fixed rate; restarting the ticker on every QPS change is awkward and produces a measurable spike at each change. The cleaner shape is to recompute the next-send time on every send, asking the profile for the current QPS at the current elapsed time.

Three design questions.

### 1. What shape does the `RateProfile` port have?

Two candidates:

- **Per-tick callback**: the Poster calls back into `profile.OnTick(t)` and the profile mutates Poster state. Pro: maximum flexibility. Con: opaque coupling; the Poster's lock-pair budget becomes a profile-author concern.
- **Pure function**: the profile is a function from elapsed time to current QPS. The Poster computes the next-send interval from the returned QPS. Pro: testable in isolation; profile authors cannot accidentally couple to Poster internals. Con: rules out profiles whose state evolves on per-send signals (e.g., "back off when target's latency climbs"). That class of profile is out of scope for v0.0.2; load-based adaptive control belongs in its own ADR.

**Pick the pure function shape:**

```go
type RateProfile interface {
    QPS(elapsed time.Duration) int
    Duration() time.Duration
}
```

`QPS` returns the target requests-per-second at `elapsed` time since profile start. Implementations clamp to non-negative QPS; the Poster treats `QPS == 0` as "stop sending and sleep until the next QPS check". `Duration` returns the profile's total length; `0` means "infinite" (the profile reports a steady QPS forever, so the run ends only on context cancellation or the cmd-level `--duration`).

### 2. Which concrete profiles ship?

The user's stated need names linear and exponential explicitly. Plus steady-state for backward compat with v0.0.1 semantics:

- **`SteadyProfile{QPS int, Duration time.Duration}`** — constant QPS for the whole run. Equivalent to v0.0.1's `--qps=N --duration=T`. Default when neither `--profile` nor `--qps` is set (so the binary still has a sensible default).
- **`LinearProfile{StartQPS, EndQPS int, Duration time.Duration}`** — QPS varies linearly from `StartQPS` at elapsed=0 to `EndQPS` at elapsed=Duration. Operators ramp 100→5000 over 15m by setting StartQPS=100, EndQPS=5000, Duration=15m. Ramp-down works by setting StartQPS > EndQPS.
- **`ExponentialProfile{StartQPS, EndQPS int, Duration time.Duration}`** — QPS varies exponentially (log-linearly) from `StartQPS` to `EndQPS`. The thundering-herd shape. The exponent is computed once at profile construction so per-send cost is one `math.Pow` call.

Beyond-`Duration` behavior: the profile clamps to `EndQPS` and reports that indefinitely. Operators who want the run to end at the same moment the ramp ends set the cmd-level `--duration` to the profile's Duration; operators who want a "ramp then hold" pattern leave `--duration=0` and the profile holds `EndQPS` until SIGINT.

`Profile.Duration` and the cmd `--duration` flag compose: the run ends on whichever fires first. `--duration=1m --profile=linear:100->500@10m` exits at 1m with the ramp truncated at the elapsed-1m position (QPS still climbing). `--duration=0 --profile=linear:100->500@10m` runs the 10m ramp and then holds at 500 until SIGINT. `--duration=15m --profile=linear:100->500@10m` runs 10m of ramp followed by 5m of steady 500. Operators get all three behaviors without per-flag special-casing.

**Degenerate and edge cases.**

- `linear:A->A@T` and `exp:A->A@T` (flat ramps): accepted. Operators may script profile specs from a range and a degenerate range is a valid input; rejecting forces special-casing at every script call site.
- `steady:0`: rejected at parse with `non-positive QPS`. The Poster's mid-run `currentQPS == 0` path (the 100ms pause-and-recheck loop) exists for adaptive profiles that might dip to zero transiently; a boot-time `steady:0` is an ambiguous operator request (did they mean "do nothing" or "I will adjust this via a future control endpoint"?) and is better surfaced as a parse error.
- `linear:500->100@5m` and `exp:1000->10@5m` (downward ramps): both accepted. The exponential profile interpolates log-linearly in either direction; the math is symmetric and operators stress recovery scenarios by ramping back down to a baseline after a spike.

Stair-step, sine-wave, and sawtooth profiles are not ruled out by this ADR but are not shipped in v0.0.2; the menu can extend without changing the port.

### 3. How do operators specify a profile on the command line?

Three candidates compared analogously to ADR-0002's preset DSL:

- **A configuration file**: `--profile-file=profile.json` reads a typed object. Most flexible but introduces a schema + parser + version surface for one operator's typo.
- **Multiple typed flags**: `--profile=linear --start-qps=100 --end-qps=5000 --ramp-duration=15m`. Verbose, brittle (operators forget one flag).
- **A small DSL** baked into one flag: `--profile=linear:100->5000@15m`. Compact, copy-pasteable in chat messages and cookbook recipes, single point of validation.

**Pick the DSL**. The grammar:

```
PROFILE   := KIND ':' SPEC
KIND      := 'steady' | 'linear' | 'exp'
SPEC      := STEADY_SPEC | RAMP_SPEC
STEADY_SPEC := INT
RAMP_SPEC := INT '->' INT '@' DURATION
DURATION  := Go time.ParseDuration-format ('15m', '90s', '1h30m')
```

Examples:

```
--profile=steady:500             # constant 500 QPS until SIGINT
--profile=linear:100->5000@15m   # 100 to 5000 QPS over 15 minutes, then hold 5000
--profile=exp:10->10000@5m       # exponential 10 to 10000 over 5 minutes
```

The existing `--qps=N` flag stays for v0.0.1 backward compatibility: when `--qps` is set without `--profile`, the cmd builds `SteadyProfile{QPS: N, Duration: 0}` and runs as before. Passing both `--profile` and `--qps` fails boot with `--qps and --profile are mutually exclusive` (mirrors `--rules` / `--snapshot` / `--route` in markup-svc).

## Decision

`internal/traffic/rate` ships:

```go
// RateProfile expresses the target QPS as a function of elapsed
// time. Implementations are pure: QPS(t) for the same t always
// returns the same value. The Poster recomputes the next-send
// interval on every send by asking the profile for the QPS at
// the current elapsed time, so a profile that returns 0 quietly
// pauses sending.
type RateProfile interface {
    QPS(elapsed time.Duration) int
    Duration() time.Duration
}

type SteadyProfile struct {
    QPS      int
    Duration time.Duration
}

type LinearProfile struct {
    StartQPS int
    EndQPS   int
    Duration time.Duration
}

type ExponentialProfile struct {
    StartQPS int
    EndQPS   int
    Duration time.Duration
}

// Parse returns the RateProfile encoded in spec, or an error
// naming what went wrong. The spec grammar is documented in
// ADR-0003; the cookbook recipe shows the operator-facing form.
func Parse(spec string) (RateProfile, error)
```

`internal/traffic/poster` is updated:

- The `Config.QPS int` field is replaced by `Config.Profile RateProfile`. Operators wiring the Poster construct a `SteadyProfile` if they want v0.0.1 semantics; the cmd does this when `--qps` is passed without `--profile`.
- The per-tick `time.NewTicker` loop is replaced by a sleep-until loop: per send, compute `elapsed`, call `profile.QPS(elapsed)`, compute `interval := time.Second / time.Duration(currentQPS)`, sleep until the next send time. When `currentQPS == 0` sleep a fixed `100ms` poll interval and re-check (the profile is paused; the operator might un-pause it via a future control endpoint).
- The Summary gains a `RequestedAvgQPS float64` field computed from `profile.QPS` integrated over the run duration so operators can compare requested-vs-achieved over the whole run, not just at the steady-state value.

`cmd/traffic-gen` is updated:

- New flag `--profile=spec` (default empty).
- The existing `--qps` flag stays; semantics described above.
- When neither flag is set, the cmd builds `SteadyProfile{QPS: 100, Duration: 0}` so the binary's no-flag default still works.
- A new `traffic-gen.profile` JSON event lands on stdout at boot describing the parsed profile shape (kind, start/end QPS, duration) so the structured log stream identifies what shape produced any subsequent traffic.

The `/metrics` endpoint originally planned for v0.0.2 moves to v0.0.3. Justification: with a variable rate, a `/metrics` endpoint exposing `traffic_gen_requested_qps` and `traffic_gen_achieved_qps` as gauges becomes much more valuable when an operator can dashboard the ramp shape; designing the metrics now without the rate-profile semantics committed would invite re-cutting in v0.0.3.

## Consequences

### Closed by this ADR

- Operators can drive markup-svc with linear and exponential ramps over arbitrary durations without writing Go.
- Long-run steady-state is the default no-flag behavior; no operator regression from v0.0.1.
- The profile is a pure function of elapsed time; profile implementations are trivially unit-testable.
- The Poster recomputes pacing on every send, so a profile that varies smoothly produces a smooth send rate (no per-change spike).
- `RequestedAvgQPS` in the Summary lets operators diff requested vs achieved across the whole run, not just at a steady-state value.

### NOT closed by this ADR

- Profile chaining (`ramp → steady → ramp`). Operators run multiple traffic-gen processes back-to-back today; chaining lands in a follow-up ADR if operators need it programmatically.
- Stair-step, sine-wave, and sawtooth profiles. The port admits them but the v0.0.2 menu is three. Same posture as the preset menu in ADR-0002.
- Adaptive / closed-loop profiles (back off when target latency climbs). Out of scope for the pure-function port; lands in its own ADR if a real consumer asks.
- A `/metrics` endpoint exposing requested-vs-achieved QPS as time-series gauges. Deferred to v0.0.3 because designing the wire shape benefits from the rate-profile semantics being committed.
- Reading a profile from a config file. Same posture as ADR-0002: the DSL covers the common cases; a file lands when the menu proves too narrow.

### Performance impact

The per-send cost gains three operations: a `time.Since(start)` call (~50 ns), a `profile.QPS(elapsed)` call (one virtual dispatch + the profile's arithmetic — ~10 ns for Steady, ~20 ns for Linear, ~50 ns for Exponential which calls `math.Pow`), and a recomputed `time.Until(nextSend)` (~30 ns). Aggregate per-send overhead: ~90–130 ns on top of the existing JSON encode + HTTP POST. At 100 QPS this is 10 µs of generator-side budget per second (0.001%); at 10000 QPS it is 1 ms per second (0.1%). The HTTP POST cost dominates by three orders of magnitude.

A `BenchmarkPoster_Profile` would pin this overhead if rate-profile drift becomes a concern, but v0.0.2 does not commit to a scientific harness for traffic-gen — the bars are markup-svc's, not the generator's. The `RequestedAvgQPS` field in the Summary is the operator-facing signal; if it ever drifts materially from `AchievedQPS` for non-target reasons, the Summary surfaces it.

### Validation strategy

- `internal/traffic/rate`: unit tests for each shipped profile and for `Parse`. Cover:
  - `TestSteadyProfileQPSIsConstant` — QPS returns the configured value at any elapsed time.
  - `TestLinearProfileEndpoints` — QPS(0) == StartQPS, QPS(Duration) == EndQPS, QPS(Duration/2) == midpoint.
  - `TestLinearProfileBeyondDurationHoldsEndQPS` — QPS(Duration + 1s) == EndQPS.
  - `TestLinearProfileRampDown` — StartQPS=500, EndQPS=100, QPS at midpoint == 300.
  - `TestExponentialProfileEndpoints` — QPS(0) == StartQPS, QPS(Duration) == EndQPS, QPS(midpoint) == sqrt(StartQPS * EndQPS) (the geometric mean for log-linear interpolation).
  - `TestExponentialProfileBeyondDurationHoldsEndQPS` — clamp to EndQPS.
  - `TestExponentialProfileRampDown` — StartQPS=1000, EndQPS=10, QPS at midpoint == 100 (geometric mean, log-linear in either direction).
  - `TestParseRoundTrips` — every shipped profile spec parses to the expected type with the expected fields.
  - `TestParseAcceptsFlatRamp` — `linear:100->100@5m` and `exp:100->100@5m` parse to their respective profiles with StartQPS == EndQPS.
  - `TestParseRejectsMalformed` — unknown kind, missing parts, non-positive QPS (`steady:0`, `linear:0->5@1m`), non-positive duration on a non-steady profile, unparseable duration, unknown trailing text.
  - `TestProfileDurationAndCmdDurationCompose` — cmd-level e2e: `--duration=80ms --profile=linear:50->500@5m` exits at 80ms with the ramp truncated (achieved QPS never reaches 500); `--duration=200ms --profile=linear:50->500@100ms` exits at 200ms with 100ms of ramp + 100ms of steady-500.
- `internal/traffic/poster`: existing tests update to construct a `SteadyProfile` instead of `QPS: N`. One new test, `TestPosterHonorsLinearRamp`, sets a `LinearProfile{Start: 50, End: 500, Duration: 200ms}` against an httptest server, runs the Poster for 200ms, and asserts the achieved QPS in the second half of the run is materially higher than the first half (concrete check: `achievedQPS(t > 100ms) > 1.5 * achievedQPS(t < 100ms)`). The asymmetric proof is the wire-level evidence the profile drives the Poster.
- `cmd/traffic-gen`: one e2e test asserting `--profile=linear:50->500@200ms --duration=200ms` against an httptest counter produces more POSTs in the second half than the first.
