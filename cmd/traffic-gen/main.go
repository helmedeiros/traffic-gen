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
	"github.com/helmedeiros/traffic-gen/internal/traffic"
	"github.com/helmedeiros/traffic-gen/internal/traffic/poster"
	"github.com/helmedeiros/traffic-gen/internal/traffic/randommix"
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
	qps := fs.Int("qps", 100, "target requests per second (paced via a 1s/QPS ticker; AchievedQPS in the summary is the honest measured rate)")
	duration := fs.Duration("duration", 0, "stop after this duration (zero = run until SIGINT/SIGTERM)")
	seed := fs.Int64("seed", time.Now().UnixNano(), "random seed for the Generator (set to a fixed value for deterministic mixes across runs)")
	timeout := fs.Duration("timeout", 5*time.Second, "per-request HTTP timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	gen, err := randommix.New(defaultBiases(), *seed)
	if err != nil {
		return fmt.Errorf("build generator: %w", err)
	}

	httpClient := &http.Client{Timeout: *timeout}
	p, err := poster.New(poster.Config{
		TargetURL: *target,
		QPS:       *qps,
		Duration:  *duration,
		Client:    httpClient,
		Out:       stderr,
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
		"qps":      *qps,
		"duration": (*duration).String(),
		"seed":     *seed,
		"timeout":  (*timeout).String(),
	})
	err = p.Run(ctx, gen)
	if err != nil {
		log.Error("traffic-gen.run", map[string]interface{}{"error": err.Error()})
	} else {
		log.Info("traffic-gen.done", nil)
	}
	return err
}

// defaultBiases is the v0.0.1 shipped persona mix. The proportions
// roughly mirror markup-svc's testdata/rules.csv -- some Requests
// match the enterprise rule, some the br_peak AND-condition, some
// the default_consumer rule, and a meaningful slice match nothing
// (404 from markup-svc). The 404 slice is intentional: it exercises
// the no-match path, which is part of the production wire shape.
//
// Operators wanting a different mix edit this function and rebuild,
// or supply a custom Generator via a wrapper main. A config-file
// route is a follow-up if a real operator asks for it.
func defaultBiases() []randommix.Bias {
	return []randommix.Bias{
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
}

// Compile-time assertion the wired generator satisfies the port.
var _ traffic.Generator = (*randommix.Generator)(nil)
var _ traffic.Poster = (*poster.Poster)(nil)
