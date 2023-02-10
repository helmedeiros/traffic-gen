# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
