// Package main is the traffic-gen entry point. Boots a single
// goroutine that draws Request shapes from the configured Generator
// and POSTs them at the configured QPS to the target URL. See
// traffic-gen/ADR-0001 for the wider design and the docs/cookbook/
// for operator-side recipes.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/helmedeiros/traffic-gen/internal/jsonlog"
	tgmetrics "github.com/helmedeiros/traffic-gen/internal/observability/metrics"
	tgotel "github.com/helmedeiros/traffic-gen/internal/observability/otel"
	"github.com/helmedeiros/traffic-gen/internal/traffic"
	"github.com/helmedeiros/traffic-gen/internal/traffic/poster"
	"github.com/helmedeiros/traffic-gen/internal/traffic/randommix"
	"github.com/helmedeiros/traffic-gen/internal/traffic/randommix/presets"
	"github.com/helmedeiros/traffic-gen/internal/traffic/rate"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "traffic-gen: %v\n", err)
		os.Exit(1)
	}
}

// run wires the binary. Separated from main so tests can drive it
// with a cancellable ctx, captured stdout/stderr, and synthetic
// args without spawning a real process.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("traffic-gen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	target := fs.String("target", "http://localhost:8080/decide", "target URL to POST generated Request bodies at")
	qps := fs.Int("qps", 0, "target requests per second; alias for --profile=steady:N (mutually exclusive with --profile). Zero means no value set; the cmd defaults to --profile=steady:100 when neither flag is passed.")
	profileSpec := fs.String("profile", "", "rate profile spec, one of: steady:N, linear:A->B@T, exp:A->B@T (mutually exclusive with --qps); see traffic-gen/ADR-0003")
	duration := fs.Duration("duration", 0, "stop after this duration (zero = run until SIGINT/SIGTERM or the profile's own Duration elapses, whichever fires first)")
	seed := fs.Int64("seed", time.Now().UnixNano(), "random seed for the Generator (set to a fixed value for deterministic mixes across runs)")
	timeout := fs.Duration("timeout", 5*time.Second, "per-request HTTP timeout")
	preset := fs.String("preset", "default", "named persona-mix preset (one of: default, uniform, stress-no-match); see traffic-gen/ADR-0002")
	otelEnabled := fs.Bool("otel-enabled", false, "bootstrap the OTel SDK + emit one root traffic.request span per outbound POST + inject W3C traceparent so downstream services (gateway, markup-svc) join the same trace; reads OTEL_EXPORTER_OTLP_ENDPOINT etc. per the OTel SDK conventions. See ADR-0004.")
	metricsListen := fs.String("metrics-listen", "", "when set, serve Prometheus /metrics on this address (e.g., :9101). Counters per outcome + duration histogram + target/achieved QPS gauges. See ADR-0006.")
	runID := fs.String("run-id", "", "X-Correlation-ID prefix stamped on every outbound POST as '<prefix>:<seq>'. Empty disables. Operators use it to filter Kibana for every request in a single load run. See ADR-0006.")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *qps != 0 && *profileSpec != "" {
		return fmt.Errorf("--qps and --profile are mutually exclusive")
	}
	if *qps < 0 {
		return fmt.Errorf("--qps must be positive, got %d", *qps)
	}
	profile, err := pickProfile(*qps, *profileSpec)
	if err != nil {
		return err
	}

	chosen, err := presets.Lookup(*preset)
	if err != nil {
		return err
	}
	gen, err := randommix.New(chosen.Biases, *seed)
	if err != nil {
		return fmt.Errorf("build generator: %w", err)
	}

	var transport http.RoundTripper = http.DefaultTransport
	if *otelEnabled {
		tracer, shutdown, err := tgotel.Bootstrap(ctx, "github.com/helmedeiros/traffic-gen/cmd/traffic-gen")
		if err != nil {
			return fmt.Errorf("otel bootstrap: %w", err)
		}
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdown(shutdownCtx)
		}()
		transport = &tgotel.InstrumentedTransport{Tracer: tracer, Inner: transport}
	}
	httpClient := &http.Client{Timeout: *timeout, Transport: transport}

	var metricsSink *tgmetrics.Sink
	if *metricsListen != "" {
		sink, handler := tgmetrics.New()
		metricsSink = sink
		mux := http.NewServeMux()
		mux.Handle("/metrics", handler)
		srv := &http.Server{Addr: *metricsListen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			_ = srv.ListenAndServe()
		}()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
		}()
		go gaugeLoop(ctx, profile, sink)
	}

	p, err := poster.New(poster.Config{
		TargetURL:           *target,
		Profile:             profile,
		Duration:            *duration,
		Client:              httpClient,
		Out:                 stderr,
		CorrelationIDPrefix: *runID,
		Metrics:             metricsSink,
	})
	if err != nil {
		return fmt.Errorf("build poster: %w", err)
	}

	// stdout carries the structured boot line so operators piping
	// stdout to a JSON log aggregator (Loki, Elasticsearch, etc.)
	// see the requested configuration as one parsed event; stderr
	// carries the poster's per-run human-readable summary so it
	// stays out of the structured-log stream.
	log := jsonlog.New(stdout)
	log.Info("traffic-gen.boot", map[string]interface{}{
		"target":   *target,
		"profile":  describeProfile(profile),
		"duration": (*duration).String(),
		"seed":     *seed,
		"timeout":  (*timeout).String(),
		"preset":   chosen.Name,
	})
	err = p.Run(ctx, gen)
	if err != nil {
		log.Error("traffic-gen.run", map[string]interface{}{"error": err.Error()})
	} else {
		log.Info("traffic-gen.done", nil)
	}
	return err
}

// pickProfile resolves the operator's --qps / --profile choice into
// a rate.RateProfile. Precedence:
//
//   - --profile=spec set: parse the DSL, return the resulting profile.
//   - --qps=N set (>0): build SteadyProfile{TargetQPS: N}.
//   - neither set: default to SteadyProfile{TargetQPS: 100} so the
//     binary's no-flag invocation still does something useful.
//
// Mutual exclusion of --qps and --profile is enforced by the caller;
// pickProfile assumes at most one is set.
func pickProfile(qps int, spec string) (rate.RateProfile, error) {
	if spec != "" {
		return rate.Parse(spec)
	}
	if qps > 0 {
		return rate.SteadyProfile{TargetQPS: qps}, nil
	}
	return rate.SteadyProfile{TargetQPS: 100}, nil
}

// describeProfile returns a map suitable for the boot JSON event's
// attrs.profile field. The shape names the kind plus the relevant
// numeric fields so structured-log queries can slice on
// attrs.profile.kind and attrs.profile.start_qps without needing to
// re-parse the spec string.
func describeProfile(p rate.RateProfile) map[string]interface{} {
	switch v := p.(type) {
	case rate.SteadyProfile:
		return map[string]interface{}{
			"kind": "steady",
			"qps":  v.TargetQPS,
		}
	case rate.LinearProfile:
		return map[string]interface{}{
			"kind":      "linear",
			"start_qps": v.StartQPS,
			"end_qps":   v.EndQPS,
			"duration":  v.Total.String(),
		}
	case rate.ExponentialProfile:
		return map[string]interface{}{
			"kind":      "exp",
			"start_qps": v.StartQPS,
			"end_qps":   v.EndQPS,
			"duration":  v.Total.String(),
		}
	default:
		return map[string]interface{}{"kind": fmt.Sprintf("%T", p)}
	}
}

// gaugeLoop keeps the target/achieved QPS gauges fresh on a 1s tick.
// Achieved QPS estimates as the difference in poster attempts vs the
// previous tick (the Sink's request counter is the source).
func gaugeLoop(ctx context.Context, profile rate.RateProfile, sink *tgmetrics.Sink) {
	start := time.Now()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var prevTotal float64
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			sink.SetTargetQPS(float64(profile.QPS(now.Sub(start))))
			cur := sink.Total()
			sink.SetAchievedQPS(cur - prevTotal)
			prevTotal = cur
		}
	}
}

// Compile-time assertion the wired generator satisfies the port.
var _ traffic.Generator = (*randommix.Generator)(nil)
var _ traffic.Poster = (*poster.Poster)(nil)
