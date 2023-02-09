# traffic-gen

Small Go binary that synthesizes `markup.Request` shapes at a configurable QPS / persona mix and POSTs them to a configurable target URL. Built to drive load against [markup-svc](https://github.com/helmedeiros/markup-svc) and any future service in the Pricing Decision Platform arc.

## Status

Pre-release. This is the day-one scaffold: ADR-0001 (Proposed) describes the Generator + Poster ports; the first adapter and `cmd/traffic-gen` land in subsequent commits of the same release window.

## Architecture

Hexagonal. `internal/traffic.Generator` is the one-method port through which the binary produces `Request` shapes. Concrete adapters wrap whichever distribution strategy fits the workload (`randommix`, future `replay`, etc.). The `Poster` port owns the HTTP push so the QPS knob and the generation knob tune independently.

| Package | Role |
|---|---|
| `internal/traffic` | domain: `Request`, `Generator`, `Poster` |

(More rows land as adapters and the poster ship.)

## Architecture decision records

See [`docs/architecture/decisions/`](docs/architecture/decisions/). ADR-0001 covers the port shape and the local-mirror-vs-import decision for the wire request type.

## Cookbook

See [`docs/cookbook/`](docs/cookbook/) for operator-level recipes. The day-one recipe walks through running markup-svc and traffic-gen on the same host and watching JSON logs flow through.

## Standing rules

This repo follows the same conventions as markup-svc:

- ADR for every architectural change (Status / Context / Decision / Consequences).
- `make ci-local` passes before every commit.
- 80% coverage floor enforced by CI.
- Conventional Commits (`type(scope): subject`).
- Annotated tags on every release; image publishing comes when the binary ships a Dockerfile.

## Building

```sh
go test ./...        # unit tests with -race
make ci-local        # the same checks CI runs
```

## License

MIT, matching the rest of the Pricing Decision Platform repos.
