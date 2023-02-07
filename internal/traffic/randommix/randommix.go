package randommix

import (
	"fmt"
	"math/rand"

	"github.com/helmedeiros/traffic-gen/internal/traffic"
)

// supportedFields names the seven Request fields a Bias may target.
// Amount is intentionally absent -- see package doc.
var supportedFields = map[string]bool{
	"product_id":    true,
	"category":      true,
	"customer_tier": true,
	"channel":       true,
	"country":       true,
	"inventory":     true,
	"time_window":   true,
}

// Bias configures the weighted distribution for one Request field.
// Field must be one of the seven supported field names; Values must
// be non-empty and every WeightedValue.Weight must be positive.
type Bias struct {
	Field  string
	Values []WeightedValue
}

// WeightedValue is one option in a Bias's distribution. Weight is
// proportional; absolute scale is irrelevant (weights of {3, 2, 1}
// produce the same distribution as {30, 20, 10}). Zero weight is
// rejected at construction so a misconfigured rule never produces
// silently-impossible Values.
type WeightedValue struct {
	Value  string
	Weight int
}

// Generator implements traffic.Generator by drawing each configured
// field independently from its Bias. Unconfigured fields are left at
// the zero value (omitempty in the JSON encoding drops them).
//
// Not safe for concurrent use: see package doc for the wrapper
// pattern.
type Generator struct {
	rng     *rand.Rand
	pickers []picker
}

type picker struct {
	field  string
	values []string
	cum    []int // cumulative weights; cum[len-1] is the total
}

// New validates biases and returns a ready Generator seeded with the
// given seed. Same seed produces the same sequence; tests rely on
// this for deterministic distribution assertions. Errors:
//
//   - "no biases configured" if biases is empty.
//   - "duplicate bias for field %q" if two Bias entries name the same
//     field; the configuration is ambiguous.
//   - "unknown field %q" if Field is not one of the seven supported.
//   - "field %q has no values" if Values is empty.
//   - "field %q value %q has non-positive weight" if any Weight <= 0.
func New(biases []Bias, seed int64) (*Generator, error) {
	if len(biases) == 0 {
		return nil, fmt.Errorf("no biases configured")
	}
	seen := make(map[string]bool, len(biases))
	pickers := make([]picker, 0, len(biases))
	for _, b := range biases {
		if !supportedFields[b.Field] {
			return nil, fmt.Errorf("unknown field %q (want one of: product_id, category, customer_tier, channel, country, inventory, time_window)", b.Field)
		}
		if seen[b.Field] {
			return nil, fmt.Errorf("duplicate bias for field %q", b.Field)
		}
		seen[b.Field] = true
		if len(b.Values) == 0 {
			return nil, fmt.Errorf("field %q has no values", b.Field)
		}
		p := picker{field: b.Field, values: make([]string, len(b.Values)), cum: make([]int, len(b.Values))}
		running := 0
		for i, wv := range b.Values {
			if wv.Weight <= 0 {
				return nil, fmt.Errorf("field %q value %q has non-positive weight %d", b.Field, wv.Value, wv.Weight)
			}
			running += wv.Weight
			p.values[i] = wv.Value
			p.cum[i] = running
		}
		pickers = append(pickers, p)
	}
	return &Generator{
		rng:     rand.New(rand.NewSource(seed)),
		pickers: pickers,
	}, nil
}

// Next draws each configured field's value and returns a Request.
// Unconfigured fields stay at the zero value; the omitempty tags
// drop them from the JSON body so the markup-svc /decide handler
// sees only what the operator asked to generate.
func (g *Generator) Next() traffic.Request {
	var req traffic.Request
	for _, p := range g.pickers {
		total := p.cum[len(p.cum)-1]
		r := g.rng.Intn(total)
		idx := pick(p.cum, r)
		setField(&req, p.field, p.values[idx])
	}
	return req
}

// pick returns the smallest index i such that cum[i] > r. cum is
// strictly increasing (every Weight is positive), so the binary
// search is O(log N) per Next.
func pick(cum []int, r int) int {
	lo, hi := 0, len(cum)-1
	for lo < hi {
		mid := (lo + hi) / 2
		if cum[mid] > r {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lo
}

// setField assigns value to the named field on req. The switch is
// intentional: at the handful of supported field names a Go string
// switch compiles to a length-bucketed jump and beats a map lookup;
// the same pattern guards the markup-svc RequiredFields rule. An
// unknown field name is rejected at New so this switch's default
// branch is unreachable in practice.
func setField(req *traffic.Request, field, value string) {
	switch field {
	case "product_id":
		req.ProductID = value
	case "category":
		req.Category = value
	case "customer_tier":
		req.CustomerTier = value
	case "channel":
		req.Channel = value
	case "country":
		req.Country = value
	case "inventory":
		req.Inventory = value
	case "time_window":
		req.TimeWindow = value
	}
}
