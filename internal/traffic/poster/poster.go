package poster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/helmedeiros/traffic-gen/internal/traffic"
)

// Config carries the operator-set knobs. QPS and TargetURL are
// required; Duration is optional (zero means "run until ctx is
// canceled"); Client defaults to http.DefaultClient when nil; Out is
// the optional progress sink and defaults to io.Discard when nil.
type Config struct {
	TargetURL string
	QPS       int
	Duration  time.Duration
	Client    *http.Client
	Out       io.Writer
}

// Summary is the per-run accounting Run returns when Run returns.
// Outcomes are mutually exclusive: every Attempt counts in exactly
// one of the four bucket fields. TransportErrors covers two cases:
// no response received (connection refused, timeout, EOF, marshal
// failure, request-build failure) AND a response whose status code
// falls outside [200, 600) (a malformed proxy or a corrupt upstream).
// Successes are 2xx; expected non-2xx (404 from markup-svc's no-match
// path, 500 from a guardrails veto) land in NotMatches and
// ServerErrors so dashboards can slice. Other 4xx (400 from a body
// the target rejected, 405 from a misconfigured path) land in
// ClientErrors.
type Summary struct {
	Duration        time.Duration
	Attempts        int
	Successes       int // 2xx
	NotMatches      int // 404 (markup-svc no-match)
	ClientErrors    int // other 4xx
	ServerErrors    int // 5xx
	TransportErrors int // no response received OR out-of-range status
	AchievedQPS     float64
}

// Poster is the traffic.Poster adapter. Constructed via New; Run
// drives the configured Generator at the configured QPS against the
// configured target URL.
type Poster struct {
	cfg Config
}

// New validates cfg and returns a Poster ready to Run. Errors:
//
//   - "target URL is required" if cfg.TargetURL is empty.
//   - "QPS must be positive, got N" if cfg.QPS <= 0.
//
// Other zero-value fields are filled in with defaults (Client,
// Duration, Out).
func New(cfg Config) (*Poster, error) {
	if cfg.TargetURL == "" {
		return nil, errors.New("target URL is required")
	}
	if cfg.QPS <= 0 {
		return nil, fmt.Errorf("QPS must be positive, got %d", cfg.QPS)
	}
	if cfg.Client == nil {
		cfg.Client = http.DefaultClient
	}
	if cfg.Out == nil {
		cfg.Out = io.Discard
	}
	return &Poster{cfg: cfg}, nil
}

// Run implements traffic.Poster. Generates requests at the target
// QPS and POSTs them as JSON to cfg.TargetURL. Returns when:
//
//   - ctx is canceled (any in-flight POST is canceled via the request
//     context -- the generator stops pushing the target immediately
//     rather than draining the current request).
//   - cfg.Duration elapses (when Duration > 0).
//
// The pacing is a ticker fired at 1s/QPS intervals. The implementation
// is intentionally not jitter-corrected: a slow target backs up the
// generation as the ticker queue does not buffer, so AchievedQPS in
// the Summary is the honest measured rate rather than the requested.
func (p *Poster) Run(ctx context.Context, gen traffic.Generator) error {
	if gen == nil {
		return errors.New("generator is required")
	}

	interval := time.Second / time.Duration(p.cfg.QPS)
	tick := time.NewTicker(interval)
	defer tick.Stop()

	var deadlineCh <-chan time.Time
	if p.cfg.Duration > 0 {
		timer := time.NewTimer(p.cfg.Duration)
		defer timer.Stop()
		deadlineCh = timer.C
	}

	var summary Summary
	start := time.Now()

	post := func() {
		summary.Attempts++
		req := gen.Next()
		body, err := json.Marshal(req)
		if err != nil {
			fmt.Fprintf(p.cfg.Out, "poster: marshal error: %v\n", err)
			summary.TransportErrors++
			return
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.TargetURL, bytes.NewReader(body))
		if err != nil {
			fmt.Fprintf(p.cfg.Out, "poster: build request error: %v\n", err)
			summary.TransportErrors++
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		resp, err := p.cfg.Client.Do(httpReq)
		if err != nil {
			fmt.Fprintf(p.cfg.Out, "poster: transport error: %v\n", err)
			summary.TransportErrors++
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		classify(resp.StatusCode, &summary)
	}

	for {
		select {
		case <-ctx.Done():
			writeSummary(p.cfg.Out, &summary, start)
			return nil
		case <-deadlineCh:
			writeSummary(p.cfg.Out, &summary, start)
			return nil
		case <-tick.C:
			post()
		}
	}
}

func writeSummary(out io.Writer, s *Summary, start time.Time) {
	s.Duration = time.Since(start)
	s.AchievedQPS = qps(s.Attempts, s.Duration)
	fmt.Fprintf(out, "poster: done attempts=%d duration=%s qps=%.1f successes=%d not_matches=%d client_errors=%d server_errors=%d transport_errors=%d\n",
		s.Attempts, s.Duration, s.AchievedQPS,
		s.Successes, s.NotMatches, s.ClientErrors,
		s.ServerErrors, s.TransportErrors)
}

// classify increments the matching Summary bucket for the given
// HTTP status code. Out of any unexpected value (negative, very
// large) it falls into TransportErrors so the bucket sums always
// equal Attempts.
func classify(status int, s *Summary) {
	switch {
	case status >= 200 && status < 300:
		s.Successes++
	case status == http.StatusNotFound:
		s.NotMatches++
	case status >= 400 && status < 500:
		s.ClientErrors++
	case status >= 500 && status < 600:
		s.ServerErrors++
	default:
		s.TransportErrors++
	}
}

// qps returns the steady-state requests-per-second for the run.
// Returns 0 when duration is non-positive (no measurable interval).
func qps(attempts int, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(attempts) / d.Seconds()
}
