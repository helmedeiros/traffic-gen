package traffic

import "context"

// Request is the local mirror of the markup-svc /decide body. JSON
// tags match the markup-svc Request shape exactly so a POST validates
// without translation. Field order matches markup-svc's
// internal/markup.Request for ease of diff. Amount is a float64; all
// other fields are strings because the upstream parser grammar today
// is string/set only -- see markup-svc/ADR-0002.
//
// traffic-gen owns this struct rather than importing it from
// markup-svc; the tradeoff is documented in ADR-0001. The cookbook
// fidelity check covers the runtime contract.
type Request struct {
	ProductID    string  `json:"product_id,omitempty"`
	Category     string  `json:"category,omitempty"`
	CustomerTier string  `json:"customer_tier,omitempty"`
	Channel      string  `json:"channel,omitempty"`
	Country      string  `json:"country,omitempty"`
	Inventory    string  `json:"inventory,omitempty"`
	TimeWindow   string  `json:"time_window,omitempty"`
	Amount       float64 `json:"amount,omitempty"`
}

// Generator produces one synthetic Request per call. Implementations
// document their concurrency posture: some shipped adapters are safe
// for many goroutines, others require a per-goroutine wrapper.
type Generator interface {
	Next() Request
}

// Poster pushes generated Requests at a configured target URL. Run
// returns when ctx is canceled or the adapter's exit condition (a
// duration budget, a target count) is satisfied. Implementations
// document the QPS policy and whether they retry on transient HTTP
// failures.
type Poster interface {
	Run(ctx context.Context, gen Generator) error
}
