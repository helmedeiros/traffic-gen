# traffic-gen cookbook

Operator-level recipes for driving load against markup-svc and the broader Pricing Decision Platform. Each recipe is one page, names the relevant ADRs and `cmd/traffic-gen` flags, and ends with a "what to check after" section so a reader who follows the commands has an obvious sanity check.

## Recipes

| Recipe | When to use |
|---|---|
| [run-locally.md](run-locally.md) | Run markup-svc and traffic-gen on the same host and watch the JSON logs flow through |

## How these recipes are written

Each recipe answers one operational question. The format is:

1. **Problem** — one sentence stating what the operator is trying to do.
2. **Recipe** — the commands and config, copy-paste-ready.
3. **What's happening** — one paragraph explaining the mechanism so the recipe is not a black box.
4. **What to check after** — concrete signals (log lines, response shapes, dashboard values) that confirm the recipe worked.
5. **Relevant ADRs and flags** — pointers into the design docs and the binary's flag list.

If a recipe and an ADR disagree, the ADR is the source of truth — file a follow-up to fix the recipe.
