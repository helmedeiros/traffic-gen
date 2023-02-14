package presets_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/helmedeiros/traffic-gen/internal/traffic/randommix"
	"github.com/helmedeiros/traffic-gen/internal/traffic/randommix/presets"
)

// TestDefaultPresetMatchesV001Shape is the load-bearing regression
// guard for the no-op-upgrade promise from ADR-0002. The literal
// expected value below is the v0.0.1 defaultBiases() output; any
// drift would surprise operators running 'traffic-gen' without
// --preset after upgrading from v0.0.1.
func TestDefaultPresetMatchesV001Shape(t *testing.T) {
	want := []randommix.Bias{
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
	}
	if !reflect.DeepEqual(presets.Default.Biases, want) {
		t.Errorf("Default.Biases drifted from v0.0.1 shape:\nwant %+v\ngot  %+v", want, presets.Default.Biases)
	}
}

// TestAllPresetsAreUsable confirms every shipped preset's biases
// pass randommix.New validation. Catches a misconfigured weight or
// unknown field name in a preset before the cmd binary surfaces it.
func TestAllPresetsAreUsable(t *testing.T) {
	for _, p := range presets.All() {
		if _, err := randommix.New(p.Biases, 1); err != nil {
			t.Errorf("preset %q: randommix.New: %v", p.Name, err)
		}
	}
}

func TestLookupReturnsKnownPreset(t *testing.T) {
	got, err := presets.Lookup("default")
	if err != nil {
		t.Fatalf("Lookup(default): %v", err)
	}
	if got.Name != "default" {
		t.Errorf("got name %q, want default", got.Name)
	}
}

func TestLookupErrorNamesAvailableChoices(t *testing.T) {
	_, err := presets.Lookup("definitely-not-a-preset")
	if err == nil {
		t.Fatal("Lookup of unknown preset returned nil error")
	}
	msg := err.Error()
	for _, want := range []string{"default", "uniform", "stress-no-match"} {
		if !strings.Contains(msg, want) {
			t.Errorf("err %q does not mention preset %q", msg, want)
		}
	}
}

func TestUniformPresetFlattensAllWeightsToOne(t *testing.T) {
	for _, b := range presets.Uniform.Biases {
		for _, wv := range b.Values {
			if wv.Weight != 1 {
				t.Errorf("uniform: field %q value %q weight=%d, want 1",
					b.Field, wv.Value, wv.Weight)
			}
		}
	}
}

// TestUniformReusesDefaultValueLists pins the ADR-0002 contract:
// uniform reuses Default's value sets exactly, just flattens the
// weights. A drift (e.g., uniform gains a new country not in
// Default) would mean the two presets sample from different value
// menus, which is not the documented behavior.
func TestUniformReusesDefaultValueLists(t *testing.T) {
	if len(presets.Uniform.Biases) != len(presets.Default.Biases) {
		t.Fatalf("uniform has %d biases, default has %d",
			len(presets.Uniform.Biases), len(presets.Default.Biases))
	}
	for i, b := range presets.Uniform.Biases {
		d := presets.Default.Biases[i]
		if b.Field != d.Field {
			t.Errorf("bias[%d] field: uniform=%q default=%q", i, b.Field, d.Field)
		}
		if len(b.Values) != len(d.Values) {
			t.Errorf("bias[%d] field=%q: uniform has %d values, default has %d",
				i, b.Field, len(b.Values), len(d.Values))
			continue
		}
		for j := range b.Values {
			if b.Values[j].Value != d.Values[j].Value {
				t.Errorf("bias[%d] field=%q value[%d]: uniform=%q default=%q",
					i, b.Field, j, b.Values[j].Value, d.Values[j].Value)
			}
		}
	}
}

// TestFlattenDoesNotMutateSource is a defensive guard against a
// future refactor that aliases the source slice (e.g., `values :=
// b.Values` followed by mutating values[j].Weight). The current
// implementation walks via value-copied range variables and so
// could not leak by construction; the test exists to surface the
// hazard at the source if anyone changes the loop shape.
func TestFlattenDoesNotMutateSource(t *testing.T) {
	for _, b := range presets.Default.Biases {
		for _, wv := range b.Values {
			if wv.Weight == 1 && b.Field == "customer_tier" {
				t.Errorf("default preset weight=1 for field %q value %q; flatten leaked into Default",
					b.Field, wv.Value)
			}
		}
	}
}

// TestStressNoMatchProducesPredominantlyMissingCombinations is a
// statistical check on the ADR-0002 claim. Draw 10k requests from
// the preset and assert that at least 70% have customer_tier in
// {gold, silver} AND country in {DE, FR, NL, ES, IT} (i.e., miss
// every rule in markup-svc's testdata). The 70% threshold is below
// the claimed >80% to absorb seed jitter without making the test
// flaky.
func TestStressNoMatchProducesPredominantlyMissingCombinations(t *testing.T) {
	const draws = 10000
	gen, err := randommix.New(presets.StressNoMatch.Biases, 42)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	missingCombos := 0
	for i := 0; i < draws; i++ {
		r := gen.Next()
		tierMisses := r.CustomerTier == "gold" || r.CustomerTier == "silver"
		countryMisses := r.Country == "DE" || r.Country == "FR" ||
			r.Country == "NL" || r.Country == "ES" || r.Country == "IT"
		if tierMisses && countryMisses {
			missingCombos++
		}
	}
	got := float64(missingCombos) / float64(draws)
	if got < 0.70 {
		t.Errorf("stress-no-match: %.2f%% of draws were predicted-miss combos; want >= 70%%", got*100)
	}
}
