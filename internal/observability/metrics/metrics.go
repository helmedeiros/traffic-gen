// Package metrics ships the traffic-gen-side Prometheus exposition:
// per-outcome counters, request-duration histogram, target-vs-achieved
// QPS gauges. Used by cmd to expose /metrics behind --metrics-listen.
// See ADR-0006.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
)

type Sink struct {
	requests    *prometheus.CounterVec
	duration    *prometheus.HistogramVec
	targetQPS   prometheus.Gauge
	achievedQPS prometheus.Gauge
}

func New() (*Sink, http.Handler) {
	reg := prometheus.NewRegistry()
	s := &Sink{
		requests: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "trafficgen_requests_total",
				Help: "Outbound POST attempts labeled by outcome (success / no_match / client_error / server_error / transport_error).",
			},
			[]string{"outcome"},
		),
		duration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "trafficgen_request_duration_seconds",
				Help:    "Outbound POST round-trip duration in seconds.",
				Buckets: durationBuckets,
			},
			[]string{"outcome"},
		),
		targetQPS: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "trafficgen_target_qps",
			Help: "Current target QPS from the active rate profile.",
		}),
		achievedQPS: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "trafficgen_achieved_qps",
			Help: "Measured QPS over a 1-second rolling window.",
		}),
	}
	reg.MustRegister(s.requests, s.duration, s.targetQPS, s.achievedQPS)
	return s, promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
}

var durationBuckets = []float64{
	0.0005, 0.001, 0.0025, 0.005, 0.01,
	0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5,
}

func (s *Sink) RecordOutcome(outcome string, durationSeconds float64) {
	if s == nil {
		return
	}
	labels := prometheus.Labels{"outcome": outcome}
	s.requests.With(labels).Inc()
	s.duration.With(labels).Observe(durationSeconds)
}

func (s *Sink) SetTargetQPS(qps float64) {
	if s == nil {
		return
	}
	s.targetQPS.Set(qps)
}

func (s *Sink) SetAchievedQPS(qps float64) {
	if s == nil {
		return
	}
	s.achievedQPS.Set(qps)
}

// Total returns the running sum of trafficgen_requests_total across
// all outcome labels. Used by the achieved-QPS gauge loop.
func (s *Sink) Total() float64 {
	if s == nil {
		return 0
	}
	ch := make(chan prometheus.Metric, 8)
	go func() { s.requests.Collect(ch); close(ch) }()
	sum := 0.0
	for m := range ch {
		var pb dto.Metric
		_ = m.Write(&pb)
		if pb.Counter != nil {
			sum += pb.Counter.GetValue()
		}
	}
	return sum
}
