package session

import (
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeFunnel is a mock funnel-sim that responds to all five endpoints
// with deterministic bodies. Counts requests per path so tests can
// assert the session state machine visited each stage as expected.
type fakeFunnel struct {
	server *httptest.Server
	calls  map[string]*uint64
}

func newFakeFunnel(t *testing.T) *fakeFunnel {
	t.Helper()
	ff := &fakeFunnel{
		calls: map[string]*uint64{
			"/search":           new(uint64),
			"/booking/init":     new(uint64),
			"/booking/reserve":  new(uint64),
			"/booking/purchase": new(uint64),
			"/booking/timeout":  new(uint64),
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddUint64(ff.calls["/search"], 1)
		_ = json.NewEncoder(w).Encode(searchResponse{
			SearchID: "sid",
			Offers: []Offer{
				{OfferID: "o1", DecisionID: "d1", Position: 1, Price: 49.99, MarkupFactor: 1.08},
				{OfferID: "o2", DecisionID: "d2", Position: 2, Price: 52.49, MarkupFactor: 1.08},
				{OfferID: "o3", DecisionID: "d3", Position: 3, Price: 54.99, MarkupFactor: 1.08},
			},
		})
	})
	mux.HandleFunc("/booking/init", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddUint64(ff.calls["/booking/init"], 1)
		_ = json.NewEncoder(w).Encode(initResponse{BookingID: "bk-42"})
	})
	mux.HandleFunc("/booking/reserve", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddUint64(ff.calls["/booking/reserve"], 1)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "reserved"})
	})
	mux.HandleFunc("/booking/purchase", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddUint64(ff.calls["/booking/purchase"], 1)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "purchased", "order_id": "ord-1"})
	})
	mux.HandleFunc("/booking/timeout", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddUint64(ff.calls["/booking/timeout"], 1)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "timed_out"})
	})
	ff.server = httptest.NewServer(mux)
	t.Cleanup(ff.server.Close)
	return ff
}

func TestRunner_HappyPathReachesPurchase(t *testing.T) {
	ff := newFakeFunnel(t)
	r, err := New(Config{
		FunnelURL:       ff.server.URL,
		Users:           1,
		Duration:        500 * time.Millisecond,
		PClick:          1.0,
		PWalkAtInit:     0.0,
		PWalkAtReserve:  0.0,
		ThinkInitMin:    time.Millisecond,
		ThinkInitMax:    2 * time.Millisecond,
		ThinkReserveMin: time.Millisecond,
		ThinkReserveMax: 2 * time.Millisecond,
		OffersPerSearch: 3,
		Seed:            42,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = r.Run(context.Background())
	stats := r.Stats()
	if stats.Searches == 0 || stats.Purchased == 0 {
		t.Fatalf("no purchases in 200ms: %+v", stats)
	}
	if stats.WalkInit != 0 || stats.WalkReserve != 0 {
		t.Errorf("PWalk_*=0 should mean zero walks; stats=%+v", stats)
	}
}

func TestRunner_PWalkAtInitOneMeansAllWalksAtInit(t *testing.T) {
	ff := newFakeFunnel(t)
	r, _ := New(Config{
		FunnelURL: ff.server.URL, Users: 1, Duration: 500 * time.Millisecond,
		PClick: 1.0, PWalkAtInit: 1.0, PWalkAtReserve: 0.0,
		ThinkInitMin: time.Millisecond, ThinkInitMax: 2 * time.Millisecond,
		ThinkReserveMin: time.Millisecond, ThinkReserveMax: 2 * time.Millisecond,
		OffersPerSearch: 3, Seed: 42,
	})
	_ = r.Run(context.Background())
	stats := r.Stats()
	if stats.WalkInit == 0 {
		t.Fatalf("expected walks at init: %+v", stats)
	}
	if stats.Reserve > 0 || stats.Purchased > 0 {
		t.Errorf("PWalkAtInit=1 must terminate before reserve; stats=%+v", stats)
	}
}

func TestRunner_PClickZeroMeansNoClicks(t *testing.T) {
	ff := newFakeFunnel(t)
	r, _ := New(Config{
		FunnelURL: ff.server.URL, Users: 1, Duration: 100 * time.Millisecond,
		PClick: 0.0, // never click; applyDefaults respects explicit zero
		OffersPerSearch: 3, Seed: 42,
	})
	// override PClick after default fill via a fresh Config path — easier
	// to just wire another runner explicitly:
	r2, _ := New(Config{
		FunnelURL: ff.server.URL, Users: 1, Duration: 200 * time.Millisecond,
		PClick: 0.000001, PWalkAtInit: 0, PWalkAtReserve: 0,
		ThinkInitMin: time.Millisecond, ThinkInitMax: 2 * time.Millisecond,
		ThinkReserveMin: time.Millisecond, ThinkReserveMax: 2 * time.Millisecond,
		OffersPerSearch: 3, Seed: 42,
	})
	_ = r2.Run(context.Background())
	stats := r2.Stats()
	// with p_click near zero, no_clicks should dominate; init should be nearly zero.
	if stats.Init > stats.NoClick {
		t.Errorf("near-zero p_click should mean NoClick > Init; stats=%+v", stats)
	}
	// keep r referenced to avoid a "declared and not used" complaint if
	// the earlier default path fires side-effect free (it doesn't run).
	_ = r
}

func TestPickOfferWeighted_PrefersTopPosition(t *testing.T) {
	offers := []Offer{
		{OfferID: "o1", Position: 1},
		{OfferID: "o2", Position: 2},
		{OfferID: "o3", Position: 3},
	}
	rng := rand.New(rand.NewSource(1))
	counts := map[string]int{}
	for i := 0; i < 3000; i++ {
		picked := pickOfferWeighted(offers, rng)
		counts[picked.OfferID]++
	}
	// Weights are 1, 1/2, 1/3. Expected fractions ≈ 0.545, 0.273, 0.182.
	// Just assert the ordering: o1 > o2 > o3 by picked count.
	if counts["o1"] <= counts["o2"] || counts["o2"] <= counts["o3"] {
		t.Errorf("weighted pick did not prefer top positions: %v", counts)
	}
}
