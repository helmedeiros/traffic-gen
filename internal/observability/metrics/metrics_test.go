package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/helmedeiros/traffic-gen/internal/observability/metrics"
)

func TestSink_RecordsAndExposes(t *testing.T) {
	sink, handler := metrics.New()

	sink.RecordOutcome("success", 0.0005)
	sink.RecordOutcome("success", 0.0012)
	sink.RecordOutcome("transport_error", 0.0030)
	sink.SetTargetQPS(500)
	sink.SetAchievedQPS(487.3)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	wants := []string{
		`trafficgen_requests_total{outcome="success"} 2`,
		`trafficgen_requests_total{outcome="transport_error"} 1`,
		`trafficgen_request_duration_seconds_count{outcome="success"} 2`,
		`trafficgen_target_qps 500`,
		`trafficgen_achieved_qps 487.3`,
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("missing %q\nbody:\n%s", w, body)
		}
	}
}

func TestSink_NilSafe(t *testing.T) {
	var sink *metrics.Sink
	sink.RecordOutcome("success", 1)
	sink.SetTargetQPS(0)
	sink.SetAchievedQPS(0)
}
