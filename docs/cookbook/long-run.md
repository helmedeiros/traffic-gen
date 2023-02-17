# Run both services overnight with a ramping rate profile

## Problem

You want traffic-gen to drive markup-svc for hours under a varying load shape — a slow ramp from 10 to 500 QPS over five minutes, then steady at 500 until you stop it — to surface streaming-transformation behavior, memory leaks, or goroutine leaks that single-fixture-row benchmarks cannot. You want both services in containers so the stack survives a laptop sleep and the JSON logs from the generator are pipe-able into `jq`.

## Recipe — one-command local stack

The repo ships a `docker-compose.yaml` at the root that pulls the public images for both services:

```sh
docker compose up
```

You should see:

```
markup-svc-1   | markup-server: listening on :8080 (3 rules, model v1, adapter inmemory, source /etc/markup/rules.csv)
traffic-gen-1  | {"time":"...","level":"info","msg":"traffic-gen.boot","attrs":{"target":"http://markup-svc:8080/decide","profile":{"kind":"exp","start_qps":10,"end_qps":500,"duration":"5m0s"}, ...}}
```

The default profile is `exp:10->500@5m` — a five-minute exponential ramp from 10 to 500 QPS, then `--profile`'s clamp-to-EndQPS behavior holds 500 QPS until `docker compose down`. The default persona mix is the `default` preset from ADR-0002.

## Recipe — change the rate profile

Override the generator's command with `docker compose run`:

```sh
# Linear ramp 100 -> 5000 over fifteen minutes, then hold 5000:
docker compose run --rm traffic-gen \
  --target=http://markup-svc:8080/decide \
  --profile=linear:100->5000@15m

# Slow steady-state overnight:
docker compose run --rm traffic-gen \
  --target=http://markup-svc:8080/decide \
  --profile=steady:500

# Ramp up then ramp down (run two processes back-to-back):
docker compose run --rm traffic-gen \
  --target=http://markup-svc:8080/decide \
  --profile=linear:100->1000@5m --duration=5m && \
docker compose run --rm traffic-gen \
  --target=http://markup-svc:8080/decide \
  --profile=linear:1000->100@5m --duration=5m
```

See [ADR-0003](../architecture/decisions/0003-rate-profiles.md) for the full grammar and the edge cases.

## Recipe — change the persona mix

Override `--preset` the same way:

```sh
# Stress markup-svc's no-match (404) path with a 5m ramp:
docker compose run --rm traffic-gen \
  --target=http://markup-svc:8080/decide \
  --profile=linear:50->500@5m \
  --preset=stress-no-match
```

See [ADR-0002](../architecture/decisions/0002-named-presets.md) for the three shipped presets and their bias shapes.

## Recipe — pipe JSON logs into jq

The generator's stdout is one JSON object per line. Filter with `jq`:

```sh
docker compose logs --no-log-prefix -f traffic-gen | jq -c '. | {msg, attrs}'
```

You should see the boot event at start, then `traffic-gen.done` when the run finishes. The poster's per-run summary lands on stderr (human-readable, not JSON) — `docker compose logs` shows both interleaved by default; pass `--no-log-prefix` and pipe through `grep -v "poster:"` if you want only the JSON.

## What's happening

`docker-compose.yaml` pulls the public images for markup-svc and traffic-gen from `ghcr.io/helmedeiros/`. The compose network puts both containers on the same internal DNS, so the generator's `--target=http://markup-svc:8080/decide` resolves to the markup-svc container without exposing 8080 to the host (the `ports: - "8080:8080"` mapping is for the operator to `curl` from the host).

The generator's rate profile is evaluated once per send in a sleep-until loop. On each send: read `elapsed`, ask `profile.QPS(elapsed)`, compute the next-send time, sleep, post. The pacing is not jitter-corrected — a slow markup-svc backs up the next-send computation so `AchievedQPS` in the per-run summary is the honest measured rate.

The boot JSON event describes the parsed profile under `attrs.profile` so an aggregator (Loki, Elasticsearch, CloudWatch) can group runs by `attrs.profile.kind` / `attrs.profile.start_qps` / `attrs.profile.end_qps` and slice latency dashboards by load shape.

## What to check after

- `docker compose ps` shows both containers `running`.
- `curl http://localhost:8080/healthz` from the host returns `200` with `{"status":"ok"}`.
- `docker compose logs --no-log-prefix traffic-gen | head -1 | jq .` parses cleanly and shows `msg: "traffic-gen.boot"` with `attrs.profile.kind` matching the spec you passed (`exp`, `linear`, or `steady`).
- The generator's stderr `poster: done` line (visible at the end of a `docker compose run --rm` invocation or via `docker compose logs traffic-gen`) shows `successes + not_matches + client_errors + server_errors + transport_errors == attempts`, `transport_errors == 0`, and `qps` within a few percent of the profile's instantaneous QPS at run end.
- For a ramp profile, the achieved-QPS-over-time can be inferred from `attempts` + duration: a `linear:50->500@5m` run for the full 5 minutes should attempt ~82,500 requests (the area under the ramp = average 275 QPS × 300s).

## Mistakes to avoid

- **Confusing `docker compose up traffic-gen` with `docker compose run traffic-gen`**: `up` uses the command from the compose file (the default ramp); `run` accepts a fresh command-line. For overrides, use `run --rm` so the container is removed when the operator stops it.
- **Forgetting `--rm` with `docker compose run`**: leaves a stopped container around per invocation. `docker compose ps -a` shows them; `docker compose rm` cleans up.
- **Targeting the wrong DNS name**: inside the compose network the markup-svc container is reachable as `http://markup-svc:8080/decide` (the compose service name), not `http://localhost:8080`. The `localhost` form works from the host (because of the port mapping) but NOT from inside the traffic-gen container.
- **Setting `--qps` and `--profile` at the same time**: boot fails with `--qps and --profile are mutually exclusive`. Pick one.
- **Reading stderr as JSON**: the poster's per-run summary is human-readable on stderr. The structured stream is stdout (`traffic-gen.boot` and `traffic-gen.done` events).

## Relevant ADRs and flags

- traffic-gen [ADR-0001](../architecture/decisions/0001-generator-port.md) — Generator + Poster ports.
- traffic-gen [ADR-0002](../architecture/decisions/0002-named-presets.md) — named persona-mix presets.
- traffic-gen [ADR-0003](../architecture/decisions/0003-rate-profiles.md) — rate profiles + DSL grammar + edge cases.
- markup-svc ADR-0013 — production deploy artifacts (the image and `/healthz` the compose stack uses).
- traffic-gen flags: `--target`, `--profile`, `--qps`, `--duration`, `--seed`, `--timeout`, `--preset`.
