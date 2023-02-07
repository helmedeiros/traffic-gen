package randommix_test

import (
	"math"
	"strings"
	"testing"

	"github.com/helmedeiros/traffic-gen/internal/traffic/randommix"
)

func TestNewRejectsEmptyBiases(t *testing.T) {
	if _, err := randommix.New(nil, 0); err == nil {
		t.Fatal("New accepted nil biases; want error")
	}
}

func TestNewRejectsUnknownField(t *testing.T) {
	_, err := randommix.New([]randommix.Bias{
		{Field: "made_up", Values: []randommix.WeightedValue{{Value: "x", Weight: 1}}},
	}, 0)
	if err == nil {
		t.Fatal("New accepted unknown field; want error")
	}
	if !strings.Contains(err.Error(), "made_up") {
		t.Errorf("err %q does not quote the offending field name", err)
	}
}

func TestNewRejectsDuplicateField(t *testing.T) {
	_, err := randommix.New([]randommix.Bias{
		{Field: "country", Values: []randommix.WeightedValue{{Value: "BR", Weight: 1}}},
		{Field: "country", Values: []randommix.WeightedValue{{Value: "DE", Weight: 1}}},
	}, 0)
	if err == nil {
		t.Fatal("New accepted duplicate bias for country; want error")
	}
}

func TestNewRejectsEmptyValues(t *testing.T) {
	_, err := randommix.New([]randommix.Bias{
		{Field: "country", Values: nil},
	}, 0)
	if err == nil {
		t.Fatal("New accepted bias with no values; want error")
	}
}

func TestNewRejectsNonPositiveWeight(t *testing.T) {
	for _, w := range []int{0, -1} {
		_, err := randommix.New([]randommix.Bias{
			{Field: "country", Values: []randommix.WeightedValue{{Value: "BR", Weight: w}}},
		}, 0)
		if err == nil {
			t.Errorf("New accepted weight %d; want error", w)
		}
	}
}

// TestNextDistributionMatchesWeights is the load-bearing test for
// the adapter. Given a Bias{country: (BR 50, DE 30, FR 20)} and a
// fixed seed, drawing 10_000 times yields empirical proportions
// within 2 percentage points of the weights. The 2pp tolerance is
// generous for 10k draws (the central-limit-theorem σ is ~0.5pp on
// each bucket) and absorbs scheduler / runtime noise without making
// the test flaky.
func TestNextDistributionMatchesWeights(t *testing.T) {
	const draws = 10000
	gen, err := randommix.New([]randommix.Bias{{
		Field: "country",
		Values: []randommix.WeightedValue{
			{Value: "BR", Weight: 50},
			{Value: "DE", Weight: 30},
			{Value: "FR", Weight: 20},
		},
	}}, 42)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	counts := map[string]int{}
	for i := 0; i < draws; i++ {
		counts[gen.Next().Country]++
	}

	expected := map[string]float64{"BR": 0.50, "DE": 0.30, "FR": 0.20}
	for value, want := range expected {
		got := float64(counts[value]) / float64(draws)
		if math.Abs(got-want) > 0.02 {
			t.Errorf("country %q proportion = %.3f, want %.3f (+- 0.020)", value, got, want)
		}
	}
}

// TestSameSeedSameSequence pins the determinism property the
// distribution test relies on: same seed, same biases, same first
// N Requests. A regression that introduced an unseeded source
// (rand.Intn, time.Now, etc.) would surface here.
func TestSameSeedSameSequence(t *testing.T) {
	bias := []randommix.Bias{{
		Field: "country",
		Values: []randommix.WeightedValue{
			{Value: "BR", Weight: 1},
			{Value: "DE", Weight: 1},
			{Value: "FR", Weight: 1},
		},
	}}
	a, _ := randommix.New(bias, 99)
	b, _ := randommix.New(bias, 99)
	for i := 0; i < 50; i++ {
		got := a.Next().Country
		want := b.Next().Country
		if got != want {
			t.Fatalf("draw %d: a=%q b=%q, want identical sequences", i, got, want)
		}
	}
}

// TestUnconfiguredFieldsStayZero confirms the documented behavior
// that Next() leaves unconfigured fields at their zero value. The
// omitempty tags then drop them from the JSON body.
func TestUnconfiguredFieldsStayZero(t *testing.T) {
	gen, _ := randommix.New([]randommix.Bias{{
		Field:  "country",
		Values: []randommix.WeightedValue{{Value: "BR", Weight: 1}},
	}}, 0)
	got := gen.Next()
	if got.Country != "BR" {
		t.Errorf("Country = %q, want %q", got.Country, "BR")
	}
	if got.ProductID != "" || got.Category != "" || got.CustomerTier != "" ||
		got.Channel != "" || got.Inventory != "" || got.TimeWindow != "" ||
		got.Amount != 0 {
		t.Errorf("unconfigured fields leaked non-zero values: %+v", got)
	}
}

// TestAllSevenFieldsConfigurable confirms each supported field can
// be set via a Bias. A regression that dropped a case in the
// setField switch would fail this on the missed field.
func TestAllSevenFieldsConfigurable(t *testing.T) {
	wv := []randommix.WeightedValue{{Value: "x", Weight: 1}}
	gen, err := randommix.New([]randommix.Bias{
		{Field: "product_id", Values: wv},
		{Field: "category", Values: wv},
		{Field: "customer_tier", Values: wv},
		{Field: "channel", Values: wv},
		{Field: "country", Values: wv},
		{Field: "inventory", Values: wv},
		{Field: "time_window", Values: wv},
	}, 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := gen.Next()
	if got.ProductID != "x" || got.Category != "x" || got.CustomerTier != "x" ||
		got.Channel != "x" || got.Country != "x" || got.Inventory != "x" ||
		got.TimeWindow != "x" {
		t.Errorf("not every field got set: %+v", got)
	}
}
