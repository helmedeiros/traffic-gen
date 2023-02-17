# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.0.2] - 2023-02-17

Variable-rate long runs land. The v0.0.1 fixed-QPS Poster becomes a sleep-until loop driven by a `RateProfile` port so traffic-gen can drive markup-svc with linear and exponential ramps over arbitrary durations. The container image publishes alongside markup-svc on every tag push so the two-service stack comes up with one `docker compose up`. Named persona-mix presets give operators a `--preset=stress-no-match` shortcut for the 404 path. The structured boot log gains an `attrs.profile` object that aggregators slice on for per-shape latency dashboards.

### Added

- `internal/traffic/randommix/presets`: three named persona-mix configurations (`default`, `uniform`, `stress-no-match`) selected via `cmd/traffic-gen --preset=NAME`. `default` preserves the v0.0.1 mix byte-exact (regression-tested via `reflect.DeepEqual`); `uniform` flattens every weight to 1 while reusing the same value lists; `stress-no-match` biases toward values that miss every rule in markup-svc's testdata so the 404 path takes >80% of traffic (statistically tested at the 70% threshold to absorb seed jitter). `presets.Lookup` returns an error naming the menu when the operator typos the flag.
- `internal/traffic/rate`: a `RateProfile` port expressing target QPS as a pure function of elapsed time, plus three concrete profiles (`SteadyProfile`, `LinearProfile`, `ExponentialProfile`) and a `Parse` DSL covering `steady:N`, `linear:A->B@T`, `exp:A->B@T`. `ExponentialProfile`'s midpoint is the geometric mean `sqrt(StartQPS * EndQPS)` for log-linear interpolation. Downward ramps (`StartQPS > EndQPS`) are supported symmetrically; flat ramps are accepted as degenerate valid; `steady:0` is rejected at parse. Profiles are pure value types — safe to share across goroutines without synchronization.
- `internal/traffic/poster`: `Config.QPS int` becomes `Config.Profile rate.RateProfile`. The ticker-based loop is replaced by a sleep-until loop that recomputes the inter-send interval on every send by asking the profile for `QPS(elapsed)`. `QPS == 0` from a profile triggers a 100ms pause-and-recheck so a future control endpoint can adjust the rate to zero transiently without stopping the run.
- `cmd/traffic-gen` `--profile=spec` flag accepting the rate DSL. The existing `--qps` becomes the steady-profile alias for v0.0.1 backward compat; both flags at once is a boot error mirroring markup-svc's `--rules`/`--snapshot`/`--route` pattern. The boot JSON event describes the parsed profile under `attrs.profile.kind` + `attrs.profile.start_qps` + `attrs.profile.end_qps` + `attrs.profile.duration` so structured-log queries slice by shape.
- `cmd/traffic-gen/Dockerfile` mirroring `markup-svc`'s two-stage `golang:1.18` build + `gcr.io/distroless/static-debian11:nonroot` runtime; runs as user 65532 with no shell and a read-only filesystem outside the working directory; final image ~15MB.
- CI workflow gains an image-publish job mirroring markup-svc: builds on every push and PR, publishes to `ghcr.io/helmedeiros/traffic-gen` on main pushes (`:main` + `:sha-<8>`) and tag pushes (`:<tag>` + `:sha-<8>`). The `tags: ['v*']` trigger ships with the workflow on day one — no retroactive fix story like markup-svc v0.1.2.
- `docker-compose.yaml` at the repo root pulls both public images and brings the two-service stack up with one `docker compose up`. Default profile is `exp:10->500@5m` (a five-minute ramp then hold). `compose-fixtures/rules.csv` mirrors markup-svc's testdata so the markup-svc container has rules to evaluate.
- `docs/cookbook/long-run.md` walks operators through the docker-compose stack, profile overrides via `docker compose run --rm`, persona overrides via `--preset`, piping the JSON log stream through `jq`, and the common mistakes (wrong DNS name inside the compose network, `--qps` + `--profile` mutual exclusion, reading stderr as JSON).
- ADR-0002 (Accepted): named persona-mix presets selected by `--preset`.
- ADR-0003 (Accepted): rate profiles for variable-QPS long runs.

### Deferred to v0.0.3

- `/metrics` endpoint exposing requested-vs-achieved QPS as Prometheus-text-format gauges. Moved out of v0.0.2 because the gauge wire shape benefits from being designed after the rate-profile semantics are committed; landing it in v0.0.3 means dashboards can name `traffic_gen_target_qps` / `traffic_gen_achieved_qps` over a meaningful axis.

## [0.0.1] - 2023-02-10

First public release. traffic-gen ships as a tiny Go binary that synthesizes `markup.Request` shapes at a configurable QPS / persona mix and POSTs them to a configurable target URL. Built to drive load against [markup-svc](https://github.com/helmedeiros/markup-svc) and any future service in the Pricing Decision Platform arc.

### Added

- `internal/traffic`: domain port + types (`Request`, `Generator`, `Poster`). `Request` is the local mirror of markup-svc's `/decide` JSON body with snake_case omitempty tags; `Generator` is the one-method port for synthetic Request production; `Poster` is the one-method port for HTTP push at a configured rate. Two contract tests pin the JSON shape against markup-svc's expected body so a drift fails the build before it fails the wire.
- `internal/traffic/randommix`: first `Generator` adapter. Operators describe each Request field's distribution via `Bias{Field, []WeightedValue}`; New validates the configuration (rejects unknown fields, duplicates, empty value lists, non-positive weights) and precomputes cumulative weights so `Next()` is O(log N) per field via binary search. A seeded `*rand.Rand` makes the output deterministic for tests. Not safe for concurrent use; the documented wrapper pattern is a per-goroutine instance from different seeds.
- `internal/traffic/poster`: first `Poster` adapter. Single-goroutine loop paced by a `1s/QPS` ticker. POSTs the generated JSON body to `cfg.TargetURL` with `Content-Type: application/json`. Five mutually-exclusive outcome buckets: `Successes` (2xx), `NotMatches` (404 from markup-svc's no-rule-matched path), `ClientErrors` (other 4xx), `ServerErrors` (5xx), `TransportErrors` (no response received OR out-of-range status). `Run` returns gracefully on `ctx.Done` or `cfg.Duration` elapsed; the in-flight POST is canceled via the request context so the generator stops pushing the target immediately.
- `internal/jsonlog`: minimal one-JSON-object-per-line logger with a fixed `{time RFC3339Nano, level, msg, attrs?}` shape suitable for ingestion by Loki / Elasticsearch / CloudWatch. Concurrency-safe via `sync.Mutex` around the encode + write window. Homegrown encoder keeps the dependency surface empty since `slog` requires Go 1.21 and the project baseline is 1.18.
- `cmd/traffic-gen` with five flags: `--target` (default `http://localhost:8080/decide`), `--qps` (default 100), `--duration` (default 0 = until SIGINT), `--seed` (default current nanos, non-deterministic), `--timeout` (default 5s). Ships a `defaultBiases()` v0.0.1 persona mix targeting markup-svc's `testdata/rules.csv`. Boot configuration logs as a `traffic-gen.boot` JSON event on stdout; the poster's per-run summary writes to stderr so the structured stream stays clean.
- `docs/cookbook/run-locally.md` walks operators through building both binaries, starting markup-svc on a non-default port, running traffic-gen for two seconds against it, and watching the structured log events flow. The recipe documents the expected bucket math (`successes + not_matches + client_errors + server_errors + transport_errors == attempts`) as a verifiable assertion.
- CI workflow with `tags: ['v*']` from day one so the future image-publish trigger does not repeat the markup-svc v0.1.2 retroactive-fix story. 80% coverage floor enforced; `make ci-local` runs the same checks locally.
- ADR-0001 (Accepted): Generator port for synthetic markup.Request traffic. Three design questions answered: local mirror of markup-svc's Request shape, single-method Generator + Poster ports keeping distribution and throughput knobs orthogonal, single-goroutine Poster for v0.0.x with worker-pool deferred. End-to-end smoke against a running markup-svc confirmed the wire-shape contract: 199.9 measured QPS at a 200 request target with 186 successes + 214 no-match responses + zero transport errors.

- ADR-0001 (Proposed): Generator port for synthetic markup.Request traffic.
