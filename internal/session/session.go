// Package session drives simulated customer journeys against funnel-sim.
// Each virtual user runs the state machine POST /search → pick offer with
// P_click → POST /booking/init → sleep → walk with P_walk_at_init OR
// POST /booking/reserve → sleep → walk with P_walk_at_reserve OR POST
// /booking/purchase → END. Sessions run concurrently; no shared state
// between them. See traffic-gen ADR-0009.
package session

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Config bundles every session-driver knob. Defaults match the design
// doc guidance in ~/Code/workspaceBRE/learning-loop-plan.local.md
// §traffic-gen session driver.
type Config struct {
	FunnelURL string // base URL of funnel-sim (e.g., "http://funnel-sim:8081")

	Users        int           // N concurrent virtual users
	Duration     time.Duration // stop after this duration (zero = run until ctx cancels)

	PClick         float64 // P(user clicks any offer after search)
	PWalkAtInit    float64 // P(walks after init before providing info)
	PWalkAtReserve float64 // P(walks after reserve before paying)

	ThinkInitMin    time.Duration
	ThinkInitMax    time.Duration
	ThinkReserveMin time.Duration
	ThinkReserveMax time.Duration

	OffersPerSearch int    // N offers funnel-sim returns per /search
	CustomerTier    string // stamped on every search query
	Country         string
	Channel         string
	Device          string
	Route           string
	Departure       string

	Client *http.Client
	Out    io.Writer // structured-log sink; jsonlog.Logger fits

	Seed int64 // deterministic RNG seed for reproducibility
}

// Stats aggregates outcomes across all virtual users.
type Stats struct {
	Searches   uint64
	NoClick    uint64
	Init       uint64
	WalkInit   uint64
	Reserve    uint64
	WalkReserve uint64
	Purchased  uint64
	Errors     uint64
}

// Runner drives N concurrent virtual users through the funnel state
// machine until the context cancels or Duration elapses. Every user is
// independent and mints its own journey_id.
type Runner struct {
	cfg   Config
	stats Stats
}

func New(cfg Config) (*Runner, error) {
	if cfg.FunnelURL == "" {
		return nil, fmt.Errorf("session: FunnelURL required")
	}
	if cfg.Users <= 0 {
		return nil, fmt.Errorf("session: Users must be > 0, got %d", cfg.Users)
	}
	cfg = applyDefaults(cfg)
	return &Runner{cfg: cfg}, nil
}

func (r *Runner) Stats() Stats {
	return Stats{
		Searches:    atomic.LoadUint64(&r.stats.Searches),
		NoClick:     atomic.LoadUint64(&r.stats.NoClick),
		Init:        atomic.LoadUint64(&r.stats.Init),
		WalkInit:    atomic.LoadUint64(&r.stats.WalkInit),
		Reserve:     atomic.LoadUint64(&r.stats.Reserve),
		WalkReserve: atomic.LoadUint64(&r.stats.WalkReserve),
		Purchased:   atomic.LoadUint64(&r.stats.Purchased),
		Errors:      atomic.LoadUint64(&r.stats.Errors),
	}
}

// Run drives the configured user pool until ctx cancels or Duration
// elapses. Returns nil on graceful shutdown; error only on setup
// failure (Config validation happened at New time).
func (r *Runner) Run(ctx context.Context) error {
	if r.cfg.Duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.cfg.Duration)
		defer cancel()
	}
	var wg sync.WaitGroup
	for i := 0; i < r.cfg.Users; i++ {
		wg.Add(1)
		go func(userIdx int) {
			defer wg.Done()
			r.runUser(ctx, userIdx)
		}(i)
	}
	wg.Wait()
	return nil
}

func (r *Runner) runUser(ctx context.Context, userIdx int) {
	rng := rand.New(rand.NewSource(r.cfg.Seed + int64(userIdx)))
	for {
		if ctx.Err() != nil {
			return
		}
		r.runOneJourney(ctx, rng)
	}
}

// runOneJourney executes one full session state machine. Any HTTP error
// increments the error counter and terminates that journey; the next
// iteration of runUser starts a fresh one.
func (r *Runner) runOneJourney(ctx context.Context, rng *rand.Rand) {
	journeyID := newHexID(rng)
	if journeyID == "" {
		atomic.AddUint64(&r.stats.Errors, 1)
		return
	}

	// 1. Search
	searchResp, err := r.postSearch(ctx, journeyID)
	if err != nil {
		atomic.AddUint64(&r.stats.Errors, 1)
		return
	}
	atomic.AddUint64(&r.stats.Searches, 1)
	if len(searchResp.Offers) == 0 || rng.Float64() > r.cfg.PClick {
		atomic.AddUint64(&r.stats.NoClick, 1)
		return
	}

	// 2. Pick an offer, weighted by 1/position (top-of-page bias)
	picked := pickOfferWeighted(searchResp.Offers, rng)

	// 3. Init
	initResp, err := r.postBookingInit(ctx, journeyID, searchResp.SearchID, picked)
	if err != nil {
		atomic.AddUint64(&r.stats.Errors, 1)
		return
	}
	atomic.AddUint64(&r.stats.Init, 1)
	bookingID := initResp.BookingID

	// 4. Think time before info entry
	sleep(ctx, thinkTime(rng, r.cfg.ThinkInitMin, r.cfg.ThinkInitMax))

	// 5. Walk at init?
	if rng.Float64() < r.cfg.PWalkAtInit {
		_ = r.postBookingTimeout(ctx, bookingID)
		atomic.AddUint64(&r.stats.WalkInit, 1)
		return
	}

	// 6. Reserve
	if _, err := r.postBookingReserve(ctx, bookingID); err != nil {
		atomic.AddUint64(&r.stats.Errors, 1)
		return
	}
	atomic.AddUint64(&r.stats.Reserve, 1)

	// 7. Think time before payment
	sleep(ctx, thinkTime(rng, r.cfg.ThinkReserveMin, r.cfg.ThinkReserveMax))

	// 8. Walk at reserve?
	if rng.Float64() < r.cfg.PWalkAtReserve {
		_ = r.postBookingTimeout(ctx, bookingID)
		atomic.AddUint64(&r.stats.WalkReserve, 1)
		return
	}

	// 9. Purchase
	if _, err := r.postBookingPurchase(ctx, bookingID); err != nil {
		atomic.AddUint64(&r.stats.Errors, 1)
		return
	}
	atomic.AddUint64(&r.stats.Purchased, 1)
}

// pickOfferWeighted picks an offer weighted by 1/position so the top of
// the results page is chosen more often than the bottom. Rough model of
// customer eye-tracking behaviour; realism-improved when a real ranker
// lands on a downstream search-svc.
func pickOfferWeighted(offers []Offer, rng *rand.Rand) Offer {
	total := 0.0
	for _, o := range offers {
		if o.Position <= 0 {
			total += 1.0
			continue
		}
		total += 1.0 / float64(o.Position)
	}
	r := rng.Float64() * total
	acc := 0.0
	for _, o := range offers {
		w := 1.0
		if o.Position > 0 {
			w = 1.0 / float64(o.Position)
		}
		acc += w
		if r <= acc {
			return o
		}
	}
	return offers[len(offers)-1]
}

func thinkTime(rng *rand.Rand, min, max time.Duration) time.Duration {
	if max <= min {
		return min
	}
	span := max - min
	return min + time.Duration(rng.Int63n(int64(span)))
}

// sleep respects the context; a canceled context returns immediately.
func sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

func newHexID(rng *rand.Rand) string {
	var b [16]byte
	_, _ = rng.Read(b[:])
	const hex = "0123456789abcdef"
	out := make([]byte, 32)
	for i, v := range b {
		out[i*2] = hex[v>>4]
		out[i*2+1] = hex[v&0x0f]
	}
	return string(out)
}

func applyDefaults(c Config) Config {
	// Probability and think-time fields are NOT defaulted here:
	// zero is a legitimate value (never walk, no sleep). The CLI
	// layer supplies its own defaults via flag definitions so an
	// operator's --session-p-click=0 actually means "never click".
	if c.OffersPerSearch <= 0 {
		c.OffersPerSearch = 8
	}
	if c.CustomerTier == "" {
		c.CustomerTier = "enterprise"
	}
	if c.Country == "" {
		c.Country = "DE"
	}
	if c.Channel == "" {
		c.Channel = "web"
	}
	if c.Device == "" {
		c.Device = "desktop"
	}
	if c.Route == "" {
		c.Route = "BR-DE"
	}
	if c.Departure == "" {
		c.Departure = "2024-09-14"
	}
	if c.Client == nil {
		c.Client = &http.Client{Timeout: 5 * time.Second}
	}
	if c.Seed == 0 {
		c.Seed = time.Now().UnixNano()
	}
	return c
}

// --- HTTP helpers ---

type Offer struct {
	OfferID      string  `json:"offer_id"`
	DecisionID   string  `json:"decision_id"`
	Position     int     `json:"position"`
	Price        float64 `json:"price"`
	MarkupFactor float64 `json:"markup_factor"`
	Experiment   string  `json:"experiment,omitempty"`
	Variant      string  `json:"variant,omitempty"`
}

type searchResponse struct {
	SearchID string  `json:"search_id"`
	Offers   []Offer `json:"offers"`
}

type initResponse struct {
	BookingID string `json:"booking_id"`
}

func (r *Runner) postSearch(ctx context.Context, journeyID string) (*searchResponse, error) {
	body := map[string]any{
		"journey_id":    journeyID,
		"route":         r.cfg.Route,
		"departure":     r.cfg.Departure,
		"passengers":    1,
		"channel":       r.cfg.Channel,
		"device":        r.cfg.Device,
		"country":       r.cfg.Country,
		"customer_tier": r.cfg.CustomerTier,
		"offer_count":   r.cfg.OffersPerSearch,
	}
	var out searchResponse
	if err := r.postJSON(ctx, "/search", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *Runner) postBookingInit(ctx context.Context, journeyID, searchID string, o Offer) (*initResponse, error) {
	body := map[string]any{
		"journey_id":    journeyID,
		"search_id":     searchID,
		"decision_id":   o.DecisionID,
		"offer_id":      o.OfferID,
		"price":         o.Price,
		"markup_factor": o.MarkupFactor,
		"currency":      "EUR",
	}
	var out initResponse
	if err := r.postJSON(ctx, "/booking/init", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *Runner) postBookingReserve(ctx context.Context, bookingID string) (*bytes.Buffer, error) {
	body := map[string]any{"booking_id": bookingID}
	return nil, r.postJSON(ctx, "/booking/reserve", body, nil)
}

func (r *Runner) postBookingPurchase(ctx context.Context, bookingID string) (*bytes.Buffer, error) {
	body := map[string]any{"booking_id": bookingID}
	return nil, r.postJSON(ctx, "/booking/purchase", body, nil)
}

func (r *Runner) postBookingTimeout(ctx context.Context, bookingID string) error {
	body := map[string]any{"booking_id": bookingID}
	return r.postJSON(ctx, "/booking/timeout", body, nil)
}

func (r *Runner) postJSON(ctx context.Context, path string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.cfg.FunnelURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.cfg.Client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("session: %s %d", path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
