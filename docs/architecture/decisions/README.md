# Architecture Decision Records

Each file in this folder captures one architecture decision made on the traffic-gen codebase, following the standard ADR shape (Status / Context / Decision / Consequences).

New decisions get the next number and a short kebab-case slug:

```
NNNN-short-decision-name.md
```

`scripts/check-adrs.sh` (wired into `make ci-local`) verifies that:

1. Every ADR file is indexed in this README.
2. Every README link points at a file that exists.
3. Every ADR file has a `## Status` line with one of: `Proposed`, `Accepted`, `Superseded by ADR-NNNN`, `Deprecated`.
4. Every ADR file has the four standard sections: `## Status`, `## Context`, `## Decision`, `## Consequences`.

## Index

| # | Title | Status |
|---|---|---|
| [0001](0001-generator-port.md) | Generator port for synthetic markup.Request traffic | ✅ Accepted |
| [0002](0002-named-presets.md) | Named persona-mix presets selected by --preset | ✅ Accepted |
| [0003](0003-rate-profiles.md) | Rate profiles for variable-QPS long runs | ✅ Accepted |
| [0004](0004-otel-root-spans.md) | OTel root spans + W3C trace context emission per outbound POST | ✅ Accepted |
