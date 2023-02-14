package presets

import (
	"fmt"
	"strings"

	"github.com/helmedeiros/traffic-gen/internal/traffic/randommix"
)

// Preset bundles a name with the []randommix.Bias it expands to.
type Preset struct {
	Name   string
	Biases []randommix.Bias
}

// All returns the shipped presets in a fixed order so the --preset
// flag's error message, the cookbook, and any future operator menu
// stay in sync.
func All() []Preset {
	return []Preset{Default, Uniform, StressNoMatch}
}

// Lookup returns the named preset or an error whose message lists
// the menu so operators see the available choices without having
// to grep the source.
func Lookup(name string) (Preset, error) {
	for _, p := range All() {
		if p.Name == name {
			return p, nil
		}
	}
	names := make([]string, 0, len(All()))
	for _, p := range All() {
		names = append(names, p.Name)
	}
	return Preset{}, fmt.Errorf("unknown preset %q (want one of: %s)", name, strings.Join(names, ", "))
}

// Default preserves the v0.0.1 defaultBiases() shape exactly so
// operators upgrading from v0.0.1 with no --preset flag see the
// same wire mix. A drift here would surprise upgrades; the test
// suite asserts value-equality against the literal v0.0.1 output.
var Default = Preset{
	Name: "default",
	Biases: []randommix.Bias{
		{
			Field: "customer_tier",
			Values: []randommix.WeightedValue{
				{Value: "enterprise", Weight: 20},
				{Value: "gold", Weight: 30},
				{Value: "silver", Weight: 30},
				{Value: "consumer", Weight: 20},
			},
		},
		{
			Field: "country",
			Values: []randommix.WeightedValue{
				{Value: "BR", Weight: 30},
				{Value: "DE", Weight: 25},
				{Value: "FR", Weight: 20},
				{Value: "NL", Weight: 10},
				{Value: "ES", Weight: 10},
				{Value: "IT", Weight: 5},
			},
		},
		{
			Field: "time_window",
			Values: []randommix.WeightedValue{
				{Value: "peak", Weight: 30},
				{Value: "off", Weight: 40},
				{Value: "normal", Weight: 30},
			},
		},
		{
			Field: "channel",
			Values: []randommix.WeightedValue{
				{Value: "web", Weight: 60},
				{Value: "app", Weight: 30},
				{Value: "partner", Weight: 10},
			},
		},
	},
}

// Uniform reuses Default's value lists with every weight flattened
// to 1. Drawing any value with equal probability surfaces rule
// paths the Default preset underweights and stress-tests engine
// per-Decide latency across a uniform fact-map distribution.
var Uniform = Preset{
	Name:   "uniform",
	Biases: flattenWeights(Default.Biases),
}

// StressNoMatch biases toward values that miss every rule in
// markup-svc's testdata/rules.csv (enterprise on customer_tier ==
// 'enterprise', br_peak on country == 'BR' AND time_window ==
// 'peak', default_consumer on customer_tier == 'consumer').
// gold/silver tiers miss enterprise + default_consumer; IT/ES/NL
// countries miss the br_peak country filter. Expected 404 /
// not_matches rate is >80%.
var StressNoMatch = Preset{
	Name: "stress-no-match",
	Biases: []randommix.Bias{
		{
			Field: "customer_tier",
			Values: []randommix.WeightedValue{
				{Value: "enterprise", Weight: 5},
				{Value: "gold", Weight: 45},
				{Value: "silver", Weight: 45},
				{Value: "consumer", Weight: 5},
			},
		},
		{
			Field: "country",
			Values: []randommix.WeightedValue{
				{Value: "BR", Weight: 5},
				{Value: "DE", Weight: 10},
				{Value: "FR", Weight: 10},
				{Value: "NL", Weight: 25},
				{Value: "ES", Weight: 25},
				{Value: "IT", Weight: 25},
			},
		},
		{
			Field: "time_window",
			Values: []randommix.WeightedValue{
				{Value: "peak", Weight: 10},
				{Value: "off", Weight: 60},
				{Value: "normal", Weight: 30},
			},
		},
		{
			Field: "channel",
			Values: []randommix.WeightedValue{
				{Value: "web", Weight: 60},
				{Value: "app", Weight: 30},
				{Value: "partner", Weight: 10},
			},
		},
	},
}

// flattenWeights returns a deep copy of biases with every weight
// rewritten to 1. The deep copy keeps the source preset immutable
// so Default.Biases is not mutated as a side effect of constructing
// Uniform.
func flattenWeights(biases []randommix.Bias) []randommix.Bias {
	out := make([]randommix.Bias, len(biases))
	for i, b := range biases {
		values := make([]randommix.WeightedValue, len(b.Values))
		for j, wv := range b.Values {
			values[j] = randommix.WeightedValue{Value: wv.Value, Weight: 1}
		}
		out[i] = randommix.Bias{Field: b.Field, Values: values}
	}
	return out
}
