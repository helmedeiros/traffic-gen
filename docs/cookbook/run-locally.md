# Run markup-svc and traffic-gen together on one host

## Problem

You want to confirm the two services talk on the wire, watch JSON-shaped logs flow through both, and see the per-run outcome bucket counts the poster reports. This is the smoke test before any docker-compose or deployed environment.

## Recipe

Build both binaries:

```sh
# in the markup-svc checkout
go build -o ./markup-server ./cmd/markup-server

# in the traffic-gen checkout
go build -o ./traffic-gen ./cmd/traffic-gen
```

Start markup-svc on a non-default port so you don't collide with anything else, and point it at the shipped testdata:

```sh
./markup-server \
  --rules=./cmd/markup-server/testdata/rules.csv \
  --listen=:18080
```

You should see:

```
markup-server: listening on :18080 (3 rules, model v1, adapter inmemory, source ./cmd/markup-server/testdata/rules.csv)
```

In another terminal, run traffic-gen for two seconds at 200 requests per second:

```sh
./traffic-gen \
  --target=http://localhost:18080/decide \
  --qps=200 \
  --duration=2s \
  --seed=42
```

You should see, on stdout (JSON):

```
{"time":"...","level":"info","msg":"traffic-gen.boot","attrs":{"qps":200,"target":"http://localhost:18080/decide", ...}}
{"time":"...","level":"info","msg":"traffic-gen.done"}
```

And on stderr (human-readable summary):

```
poster: done attempts=400 duration=2.0s qps=199.9 successes=186 not_matches=214 client_errors=0 server_errors=0 transport_errors=0
```

The split is intentional — pipe stdout to a structured log aggregator and keep stderr for the human watching the terminal.

## What's happening

`traffic-gen` is a single-goroutine loop paced by a `1s/qps` ticker. On each tick it calls the configured `randommix.Generator` which draws each Request field independently from a weighted distribution (see `cmd/traffic-gen/main.go:defaultBiases` for the shipped v0.0.1 mix), encodes the Request as JSON, and POSTs it to the configured `--target`. The response status code lands in one of five mutually-exclusive buckets per [ADR-0001](../architecture/decisions/0001-generator-port.md): `successes` (2xx), `not_matches` (404 from markup-svc's no-rule-matched path), `client_errors` (other 4xx), `server_errors` (5xx), `transport_errors` (no response received OR out-of-range status).

The example proportions (186 successes / 214 no-matches at the shipped seed and mix) come from markup-svc's `testdata/rules.csv` which has three rules: an `enterprise` tier rule, a `BR + peak` AND-condition rule, and a `default_consumer` rule. The default persona mix produces requests that hit those rules sometimes and miss sometimes — both paths exercised, which is the point.

## What to check after

- `markup-server: listening on :18080` is visible in the markup-svc terminal before you start traffic-gen.
- `curl http://localhost:18080/healthz` returns `200` with `{"status":"ok"}`.
- Stdout from `traffic-gen` contains exactly two JSON objects with `"msg":"traffic-gen.boot"` and `"msg":"traffic-gen.done"`.
- Stderr from `traffic-gen` contains a `poster: done` line whose `attempts` field is approximately `qps × duration` (e.g., 200 × 2 = 400, with single-digit-ms scheduling jitter).
- `successes + not_matches + client_errors + server_errors + transport_errors == attempts` (the buckets are mutually exclusive).
- `transport_errors == 0` (otherwise markup-svc was not reachable).
- `client_errors == 0` (otherwise the request body shape drifted from what markup-svc accepts — see the next section).
- `qps` in the summary is within a few percent of the requested `--qps`.

## Mistakes to avoid

- **Pointing traffic-gen at the wrong path**: `--target` must end in `/decide`. Pointing at the root URL produces `client_errors=N` (markup-svc returns `405 method not allowed` from the implicit GET handling).
- **Forgetting `--duration`**: without it traffic-gen runs until `SIGINT`. Useful for a long-soak run, surprising in a quick smoke test.
- **Sharing port `:8080`** between markup-svc and something else on the host. Use `--listen=:18080` (or any free port) for the smoke; markup-svc defaults to `:8080` in production.
- **Reading the stderr summary as JSON**: it is intentionally human-readable. The structured stream is stdout.
- **Looking for traffic-gen JSON in markup-svc's logs**: markup-svc currently emits plain text on its own stdout (see `cmd/markup-server/main.go`). A future ADR can promote markup-svc to structured logs once a real consumer needs the cross-service correlation.

## Relevant ADRs and flags

- traffic-gen [ADR-0001](../architecture/decisions/0001-generator-port.md) — Generator + Poster ports.
- markup-svc ADR-0003 — HTTP transport (`POST /decide` route and body shape).
- markup-svc ADR-0013 — `/healthz` + `/readyz` probes.
- traffic-gen flags: `--target`, `--qps`, `--duration`, `--seed`, `--timeout`.
- markup-svc flags relevant to the smoke: `--rules`, `--listen`.
