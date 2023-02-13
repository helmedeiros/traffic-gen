# 2. Named Persona-Mix Presets

## Status

Proposed — proposes an `internal/traffic/randommix/presets` package shipping three named `[]Bias` configurations operators select via a `--preset` cmd flag. Custom biases stay reachable through the wrapper-main pattern; a config-file route stays deferred until a real operator asks. The default preset preserves the v0.0.1 `defaultBiases()` shape so an operator running `traffic-gen` with no `--preset` flag sees the same wire mix as before.

## Context

v0.0.1 ships one hardcoded `defaultBiases()` inside `cmd/traffic-gen`. Operators who want a different mix today either edit the Go source and rebuild or write a wrapper main that constructs its own `randommix.New(...)`. Both paths are documented in the ADR-0001 NOT-closed list as the v0.0.x posture.

In practice the most common operator question after the v0.0.1 release is "how do I stress just the enterprise rule" or "how do I shape traffic to maximize no-matches so I can measure the 404 path under load". Each of those is a small bias change — different weights on `customer_tier` or `country` — that does not warrant a wrapper-main build.

Three options to close that gap.

### 1. A configuration file (YAML / JSON / TOML) the cmd reads at boot

Pro: maximum operator flexibility; no rebuild needed for any persona shape.

Con: introduces a parser + schema versioning + a `--preset-file` flag + tests covering "what happens when the file is malformed / missing / unsupported version". Substantial surface for v0.0.2; the operator value is real but bounded — every shape the file can express is reachable today by a wrapper main.

### 2. A flag-driven inline bias (`--bias=country:BR=50,DE=30,FR=20`)

Pro: no file at all; operators construct the mix on the command line.

Con: the flag syntax is a mini-DSL the cmd must parse and validate. Multiple biases need repeatable `--bias` flags; weights become positional and operators get them wrong. The `--route` flag from markup-svc shows the bug surface (string-splitting on `:` and `,`); the same posture applies here but with the additional axis of weights-per-value.

### 3. Named presets baked into a Go package, selected by `--preset=NAME`

Pro: zero parsing surface; the preset list is the operator-facing menu. Adding a new preset is a five-line Go change that lands through the normal PR / ADR flow. Operators who need a shape outside the menu still have the wrapper-main path.

Con: the menu is small; the v0.0.2 release only ships three presets. Operators outside the menu pay the wrapper-main cost the v0.0.1 ADR already documented.

**Pick the named-preset route for v0.0.2.** The operator value (a quick `--preset=enterprise-heavy` instead of editing Go) is real for the three or four shapes most often requested. The dependency surface stays empty. A config-file route can land in a later release if the menu proves too narrow.

### Which presets ship?

Three named presets cover the operator scenarios v0.0.1 surfaced:

- **`default`** — preserves the v0.0.1 `defaultBiases()` shape exactly (customer_tier 20/30/30/20, country 30/25/20/10/10/5, time_window 30/40/30, channel 60/30/10). Operators running `traffic-gen` with no `--preset` flag (or `--preset=default`) see the same wire mix as v0.0.1, so the upgrade is a no-op.

- **`uniform`** — same value lists as `default` on every axis, with every weight flattened to `1`. So `customer_tier` draws uniformly from `{enterprise, gold, silver, consumer}`, `country` from `{BR, DE, FR, NL, ES, IT}`, `time_window` from `{peak, off, normal}`, `channel` from `{web, app, partner}`. The closest shape to "draw any value with equal probability" within the value menu the project's testdata supports. Useful for surfacing rule paths the `default` preset underweights and for stress-testing the engine's per-Decide latency across a uniform fact-map distribution.

- **`stress-no-match`** — weights biased toward values that miss every rule in markup-svc's `testdata/rules.csv` (the rules are `enterprise` on `customer_tier == 'enterprise'`, `br_peak` on `country == 'BR' AND time_window == 'peak'`, and `default_consumer` on `customer_tier == 'consumer'`). So `customer_tier: gold/silver` dominate (miss `enterprise` and `default_consumer`), `country: IT/ES/NL` dominate (miss the `br_peak` country filter). The expected 404 / `not_matches` rate is >80%. Useful for measuring the no-match path under load — that path skips the guardrails decorator entirely (an inner `ErrNoMatch` passes through guardrails without consulting any Rule per markup-svc ADR-0014) so a 404-heavy run isolates the engine's no-match cost from the wrapper-stack cost.

A fourth preset (e.g., `enterprise-heavy`) is easy to add in a follow-up commit if an operator asks; the package keeps the door open.

## Decision

`internal/traffic/randommix/presets` ships:

```go
// Preset is one named persona-mix.
type Preset struct {
    Name   string
    Biases []randommix.Bias
}

// All returns the shipped presets in a fixed order so the
// --preset cmd-flag list and the cookbook stay in sync.
func All() []Preset

// Lookup returns the named preset or an error naming the
// available choices.
func Lookup(name string) (Preset, error)
```

Three named presets land in the same commit: `default`, `uniform`, `stress-no-match`. Each is constructed as a package-level variable so the test that asserts the v0.0.1 default shape stays unchanged can diff against `presets.Default.Biases`.

`cmd/traffic-gen` gains `--preset=NAME` (default `default`). On boot, `presets.Lookup` returns the chosen preset and the cmd hands its `Biases` to `randommix.New`. An unknown preset name fails boot with an error message naming the available choices (mirrors the `--adapter` / `--policy` posture in markup-svc).

The v0.0.1 `defaultBiases()` helper in `cmd/traffic-gen/main.go` is removed in favor of `presets.Default.Biases` so the source of truth is the presets package and no two-place drift can sneak in.

## Consequences

### Closed by this ADR

- Operators can switch persona shapes without editing Go and rebuilding.
- The three shipped presets cover the most common scenarios surfaced by v0.0.1: realistic mix (`default`), uniform spread (`uniform`), no-match path under load (`stress-no-match`).
- The presets package becomes the single source of truth for the wire mix; a future preset addition is a five-line Go change.
- Operators outside the menu still have the wrapper-main path; the ADR-0001 NOT-closed posture is unchanged.

### NOT closed by this ADR

- Configuration file for arbitrary biases. Deferred until the menu proves too narrow; the cookbook recipe will name the wrapper-main path for operators outside the menu.
- Inline `--bias` flag syntax. Rejected per the parsing-surface tradeoff above.
- Biased `Amount` field. Same as ADR-0001: markup-svc does not act on Amount today; operators wanting Amount distributions land a follow-up adapter or a decorator wrapping the shipped Generator.
- Per-preset `Amount` ranges or other field-specific decoration. The shipped presets target string fields only; the wrapper-main path covers custom-shape operators.

### Performance impact

`Lookup` is an O(N) walk over three entries; cost is sub-microsecond and runs once at boot. The Generator constructed from a preset's `Biases` has identical per-`Next()` cost to the v0.0.1 Generator (the picker is the same code path).

The `uniform` preset has more values per axis than `default`, so `AllowedCountries.Check`-equivalent picking does more comparisons; per ADR-0014's analysis of the same pattern in markup-svc's `AllowedCountries` rule, this is sub-nanosecond for the typical 2-letter country code. Not material on the generator side where the HTTP POST cost dominates by three orders of magnitude.

### Validation strategy

- `internal/traffic/randommix/presets`: unit tests for each shipped preset. Cover:
  - `TestDefaultPresetMatchesV001Shape` — asserts the default preset's Biases value-equal the literal v0.0.1 `defaultBiases()` output. A regression that drifts the default mix would fail loudly so operators upgrading from v0.0.1 are not surprised.
  - `TestAllPresetsAreUsable` — every preset returned by `All()` constructs a valid `randommix.Generator` (passes `randommix.New` without error). Catches a misconfigured weight or unknown field name in a preset.
  - `TestLookupErrorNamesAvailableChoices` — `Lookup("typo")` returns an error whose message lists `default / uniform / stress-no-match` so operators see the menu without having to grep the source.
- `cmd/traffic-gen`: an e2e test asserting `--preset=stress-no-match` against a stubbed markup-svc-like target produces materially more 404s than `--preset=default` against the same target. The asymmetric proof is the wire-level evidence that the preset flag does real work.
