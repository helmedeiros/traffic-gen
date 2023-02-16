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
	"github.com/helmedeiros/traffic-gen/internal/traffic/rate"
)

// pauseCheckInterval is the sleep window between QPS checks when the
// active RateProfile reports QPS == 0 (a transient pause). 100ms
// trades responsiveness against tight-loop CPU when the operator
// adjusts the rate via a future control endpoint.
const pauseCheckInterval = 100 * time.Millisecond

// Config carries the operator-set knobs. TargetURL and Profile are
// required; Duration is optional (zero means "run until ctx is
// canceled OR the Profile's own Duration elapses, whichever comes
// first"); Client defaults to http.DefaultClient when nil; Out is the
// optional progress sink and defaults to io.Discard when nil.
//
// The cmd-level --duration flag and the Profile's own Duration
// compose: the run ends on whichever fires first. Operators wanting
// "ramp then hold until SIGINT" leave Duration zero and rely on the
// profile's clamp-to-EndQPS behavior beyond Profile.Duration.
type Config struct {
	TargetURL string
	Profile   rate.RateProfile
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
//   - "rate profile is required" if cfg.Profile is nil.
//
// Other zero-value fields are filled in with defaults (Client,
// Duration, Out).
func New(cfg Config) (*Poster, error) {
	if cfg.TargetURL == "" {
		return nil, errors.New("target URL is required")
	}
	if cfg.Profile == nil {
		return nil, errors.New("rate profile is required")
	}
	if cfg.Client == nil {
		cfg.Client = http.DefaultClient
	}
	if cfg.Out == nil {
		cfg.Out = io.Discard
	}
	return &Poster{cfg: cfg}, nil
}

// Run implements traffic.Poster. Generates requests at the QPS the
// configured RateProfile reports for the current elapsed time and
// POSTs them as JSON to cfg.TargetURL. Returns when:
//
//   - ctx is canceled (any in-flight POST is canceled via the request
//     context -- the generator stops pushing the target immediately
//     rather than draining the current request).
//   - cfg.Duration elapses (when Duration > 0).
//
// The pacing is a sleep-until loop that recomputes the inter-send
// interval on every send. Per send: read elapsed, ask the profile
// for QPS(elapsed), compute interval := time.Second / QPS, sleep
// until the next send time. When the profile reports QPS == 0 the
// loop sleeps a fixed pauseCheckInterval and re-checks; the run is
// paused but not stopped.
//
// The implementation is intentionally not jitter-corrected: a slow
// target backs up the next-send computation so AchievedQPS in the
// Summary is the honest measured rate rather than the requested.
func (p *Poster) Run(ctx context.Context, gen traffic.Generator) error {
	if gen == nil {
		return errors.New("generator is required")
	}

	var deadlineCh <-chan time.Time
	if p.cfg.Duration > 0 {
		timer := time.NewTimer(p.cfg.Duration)
		defer timer.Stop()
		deadlineCh = timer.C
	}

	var summary Summary
	start := time.Now()
	nextSend := start

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
		default:
		}

		currentQPS := p.cfg.Profile.QPS(time.Since(start))
		if currentQPS <= 0 {
			// Profile is paused. Sleep a short window and re-check.
			// A select on the timer + ctx so cancel terminates fast.
			pause := time.NewTimer(pauseCheckInterval)
			select {
			case <-ctx.Done():
				pause.Stop()
				writeSummary(p.cfg.Out, &summary, start)
				return nil
			case <-deadlineCh:
				pause.Stop()
				writeSummary(p.cfg.Out, &summary, start)
				return nil
			case <-pause.C:
			}
			// Reset nextSend so the post-pause cadence picks up cleanly
			// rather than firing a burst to catch up.
			nextSend = time.Now()
			continue
		}

		interval := time.Second / time.Duration(currentQPS)
		nextSend = nextSend.Add(interval)
		sleep := time.Until(nextSend)
		if sleep > 0 {
			timer := time.NewTimer(sleep)
			select {
			case <-ctx.Done():
				timer.Stop()
				writeSummary(p.cfg.Out, &summary, start)
				return nil
			case <-deadlineCh:
				timer.Stop()
				writeSummary(p.cfg.Out, &summary, start)
				return nil
			case <-timer.C:
			}
		}
		post()
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
