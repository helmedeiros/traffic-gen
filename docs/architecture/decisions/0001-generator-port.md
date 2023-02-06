# 1. Generator Port for Synthetic markup.Request Traffic

## Status

Proposed — proposes a `Generator` port at the heart of traffic-gen with a single `Next() Request` method. The first adapter, `randommix.Generator`, ships in the same release window and produces requests with operator-configurable weighted distributions per field. A second port — `Poster` — owns the HTTP push so the QPS knob and the generation knob are tunable independently.

## Context

The wider Pricing Decision Platform sketch has a "Different Clients" box that represents the traffic shapes that real markup decisions are made against. The markup-svc service has shipped through `v0.1.4` with a scientific harness measuring per-`Decide` latency at known fixture shapes — but that is steady-state, single-goroutine measurement against one fixture row. Production-shaped traffic differs in two axes:

- **Distributional shape**. Real upstream calls land with a biased mix of `customer_tier`, `country`, `channel`, `inventory`, and `time_window`. A bench fixture row that matches one rule exactly tells you the engine's per-`Decide` cost but says nothing about how the *cache* of compiled `parser.Condition` trees performs across a realistic mix of fact maps.
- **Concurrency shape**. Real callers arrive across N goroutines per second, not 1. The latency dashboard in production reflects connection-pool contention and HTTP-server overhead the scientific harness intentionally excludes.

`traffic-gen` exists to close both gaps. It is a tiny Go binary that synthesizes `markup.Request` shapes at a configurable QPS / persona mix and POSTs them to a configurable target URL. Two things motivate giving it its own repo rather than landing it under `cmd/markup-svc-bench/` inside markup-svc:

- **Lifecycle independence**. The generator's tagged releases evolve on a different cadence than the service. A change to the persona mix is not a markup-svc API change.
- **Cross-target reuse**. Once the next service in the platform arc ships (the API gateway, the metrics sink, the model registry), traffic-gen should be able to point at any of them. A library that lives inside markup-svc makes that awkward.

Three design questions.

### 1. What is the `Request` shape on the wire?

`traffic-gen` needs to produce requests that `markup-svc`'s `/decide` accepts. The markup-svc `Request` type lives in `internal/markup` and is not importable from outside the module. Two candidates for closing the loop:

- **Mirror the JSON shape locally**. traffic-gen defines its own `Request` struct with the same JSON tags. Pro: no coupling to markup-svc internals; traffic-gen can also target an HTTP gateway that translates a different upstream shape into the markup-svc body. Con: a future markup-svc field rename would not break the generator's build — only a runtime POST would fail.
- **Have markup-svc expose a public client package**. markup-svc adds `pkg/markupclient` (or moves the `Request` type to `pkg/markup`). traffic-gen imports it. Pro: a single source of truth for the wire shape. Con: changes the markup-svc API surface for one consumer's convenience; couples the two repos at the build level.

**Pick the mirror.** traffic-gen owns its `Request` struct locally; a fidelity test in the cookbook walks operators through booting both services and confirming a real round-trip. The decoupling cost of the mirror is small (each repo evolves independently) and the gain is real (the generator can later target a gateway whose downstream shape differs).

If the wire shape proves a frequent source of mismatch in practice, the second ADR (in a later release window) can promote the markup-svc shape to a public package. That decision is not closed today.

### 2. What does the `Generator` port look like?

A single-method port keeps the implementation easy to swap and easy to test:

```go
type Generator interface {
    Next() Request
}
```

`Next()` returns one fully-populated `Request` shape. Stateless on the surface; implementations are free to hold internal state (a `*rand.Rand`, a counter, a token bucket) as long as `Next()` is safe to call from many goroutines OR the operator wraps the generator in a per-goroutine `sync.Mutex`. The shipped adapters document their concurrency posture.

A `Generator` that returns one `Request` per call separates the *what to generate* concern from the *how fast to send* concern. The `Poster` (below) handles the throughput knob; the `Generator` only owns the distribution.

### 3. What does the `Poster` port look like?

POST throughput should be tunable independently of generator complexity. Operators run "high-QPS, simple mix" or "low-QPS, complex persona mix" without touching generator code:

```go
type Poster interface {
    Run(ctx context.Context, gen Generator) error
}
```

The first adapter ships in W18 (this ADR commits to a Generator + first random-mix adapter; the Poster impl is its own commit later in the release window). It honors `--qps` (steady-state target requests per second) and `--duration` (how long to run before returning) flags. Ctx cancellation aborts the run cleanly.

## Decision

`internal/traffic` ships:

```go
// Request is traffic-gen's local mirror of the markup-svc /decide
// body. JSON tags match the markup-svc shape exactly so a POST
// validates without translation.
type Request struct {
    ProductID    string  `json:"product_id,omitempty"`
    Category     string  `json:"category,omitempty"`
    CustomerTier string  `json:"customer_tier,omitempty"`
    Channel      string  `json:"channel,omitempty"`
    Country      string  `json:"country,omitempty"`
    Inventory    string  `json:"inventory,omitempty"`
    TimeWindow   string  `json:"time_window,omitempty"`
    Amount       float64 `json:"amount,omitempty"`
}

// Generator produces one synthetic markup Request per call. See
// adapter packages for concurrency posture.
type Generator interface {
    Next() Request
}
```

The first adapter — `internal/traffic/randommix` — ships in the same release window (a separate commit). It accepts a `[]Bias` slice that names a field, a weighted set of values, and produces a `Generator` that draws each field independently per `Next()`. A seeded `*rand.Rand` makes the output deterministic for tests.

The `Poster` port and its first adapter land in a later commit of this release window, along with the cmd/traffic-gen flag wiring.

A cookbook recipe (`docs/cookbook/run-locally.md`) walks operators through booting markup-svc + traffic-gen on the same host with structured logs so the round-trip is visible.

## Consequences

### Closed by this ADR

- The "different clients" box in the Pricing Decision Platform sketch has a project that owns it.
- traffic-gen evolves independently of markup-svc; a markup-svc rename does not break the generator at build time (it may break a runtime POST, which the fidelity test in the cookbook surfaces).
- The Generator and Poster ports separate concerns: distribution and throughput tune independently.
- A seeded `*rand.Rand` in the shipped adapter makes the generator deterministic under test, which means a future scientific harness for traffic-gen itself can produce reproducible numbers.

### NOT closed by this ADR

- Promoting markup-svc's `Request` type to a public package. Deferred until repeated wire-shape mismatches in practice motivate it. The mirror + fidelity test is the v0.0.x posture.
- A persona-mix DSL or config file. The first adapter is configured via Go code (operators link traffic-gen and supply a `[]Bias`). A YAML / JSON config file is its own ADR once a real consumer needs it.
- Concurrent goroutines from the Poster. The first Poster adapter is single-goroutine — sends sequentially against the QPS budget. A worker-pool Poster lands in its own ADR if the single-goroutine throughput proves insufficient against real measurement.
- A built-in TLS / mTLS client. The Poster talks plain HTTP. Operators terminating TLS at an API gateway add the gateway between traffic-gen and the target.

### Performance impact

The generator is a load-shaping client, not a hot-path component of the system under test. Per-`Next()` cost matters only insofar as it limits the achievable QPS on the generator's own machine. For the v0.0.x random-mix adapter:

- `Next()` does N independent `rand.Intn` calls (one per configured `Bias`) plus a slice index lookup per field. For N=7 fields the cost is ~50 ns on amd64. The HTTP POST per request — connection-pool dial, JSON encode, body write, response read — dominates by three to four orders of magnitude (microseconds, not nanoseconds).
- The Poster's single-goroutine loop can sustain ~10k QPS against a localhost target on commodity hardware, limited by the HTTP round-trip rather than generator cost. A worker-pool Poster would push that higher; the v0.0.x adapter does not because the steady-state shape of single-digit-thousand QPS against a local markup-svc is enough to surface the distributional-shape concerns this project exists to study.

A scientific harness for traffic-gen itself is out of scope until at least one optimization decision needs to be made against measured numbers.

### Validation strategy

- `internal/traffic`: domain types only, no behaviour to test on this ADR.
- `internal/traffic/randommix` (shipped in the next commit): unit tests pin the distribution. Given a `Bias{Field: "country", Values: [(BR, 50), (DE, 30), (FR, 20)]}` and a seeded RNG, the cumulative distribution over 10k draws is within 1% of the expected proportions per field.
- A cookbook fidelity check confirms a single `Next()` POST against a running markup-svc returns one of the expected status codes (200 / 404 / 500) — never a 400 from JSON-shape drift.
