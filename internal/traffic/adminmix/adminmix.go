// Package adminmix runs a low-rate background POST loop against a
// configured admin endpoint so dashboards that key on the gateway's
// /admin route see continuous traffic instead of NaN. See ADR-0007.
package adminmix

import (
	"context"
	"errors"
	"net/http"
	"time"
)

type Config struct {
	TargetURL string
	Interval  time.Duration
	Client    *http.Client
	Out       outcomeSink
}

// outcomeSink is the metric callback shape. Mirrors the poster's
// metricsSink so a single Sink can serve both.
type outcomeSink interface {
	RecordOutcome(outcome string, durationSeconds float64)
}

// Run posts to cfg.TargetURL once per cfg.Interval until ctx is done.
// Each POST carries an empty body; the markup-svc reload endpoint
// re-reads its source file regardless. Errors:
//
//   - ErrTargetRequired if cfg.TargetURL is empty.
//   - ErrIntervalRequired if cfg.Interval <= 0.
func Run(ctx context.Context, cfg Config) error {
	if cfg.TargetURL == "" {
		return ErrTargetRequired
	}
	if cfg.Interval <= 0 {
		return ErrIntervalRequired
	}
	if cfg.Client == nil {
		cfg.Client = http.DefaultClient
	}
	t := time.NewTicker(cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			post(ctx, cfg)
		}
	}
}

func post(ctx context.Context, cfg Config) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TargetURL, nil)
	if err != nil {
		recordOutcome(cfg.Out, "build_error", start)
		return
	}
	resp, err := cfg.Client.Do(req)
	if err != nil {
		recordOutcome(cfg.Out, "transport_error", start)
		return
	}
	_ = resp.Body.Close()
	recordOutcome(cfg.Out, outcomeFor(resp.StatusCode), start)
}

func outcomeFor(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "success"
	case status >= 400 && status < 500:
		return "client_error"
	case status >= 500 && status < 600:
		return "server_error"
	default:
		return "transport_error"
	}
}

func recordOutcome(sink outcomeSink, outcome string, start time.Time) {
	if sink == nil {
		return
	}
	sink.RecordOutcome(outcome, time.Since(start).Seconds())
}

var (
	ErrTargetRequired   = errors.New("admin target URL is required")
	ErrIntervalRequired = errors.New("admin interval must be > 0")
)
