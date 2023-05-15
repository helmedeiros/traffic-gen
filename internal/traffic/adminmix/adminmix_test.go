package adminmix_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/helmedeiros/traffic-gen/internal/traffic/adminmix"
)

func TestRun_PostsAtInterval(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Millisecond)
	defer cancel()

	if err := adminmix.Run(ctx, adminmix.Config{
		TargetURL: srv.URL,
		Interval:  30 * time.Millisecond,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := hits.Load()
	if got < 2 || got > 4 {
		t.Errorf("expected 2-4 hits in ~110ms at 30ms interval, got %d", got)
	}
}

func TestRun_RecordsOutcome(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := &fakeSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = adminmix.Run(ctx, adminmix.Config{
		TargetURL: srv.URL,
		Interval:  20 * time.Millisecond,
		Out:       sink,
	})

	if got := sink.count(); got == 0 {
		t.Fatalf("expected at least one outcome recorded, got 0")
	}
	if got := sink.lastOutcome(); got != "success" {
		t.Errorf("expected outcome=success, got %q", got)
	}
}

func TestRun_OutcomeForStatus(t *testing.T) {
	cases := []struct {
		status  int
		outcome string
	}{
		{200, "success"},
		{204, "success"},
		{400, "client_error"},
		{404, "client_error"},
		{500, "server_error"},
		{502, "server_error"},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
		}))
		sink := &fakeSink{}
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
		_ = adminmix.Run(ctx, adminmix.Config{
			TargetURL: srv.URL,
			Interval:  15 * time.Millisecond,
			Out:       sink,
		})
		cancel()
		srv.Close()
		if got := sink.lastOutcome(); got != tc.outcome {
			t.Errorf("status=%d: outcome=%q, want %q", tc.status, got, tc.outcome)
		}
	}
}

func TestRun_ValidatesConfig(t *testing.T) {
	ctx := context.Background()
	if err := adminmix.Run(ctx, adminmix.Config{Interval: time.Second}); !errors.Is(err, adminmix.ErrTargetRequired) {
		t.Errorf("expected ErrTargetRequired, got %v", err)
	}
	if err := adminmix.Run(ctx, adminmix.Config{TargetURL: "http://x"}); !errors.Is(err, adminmix.ErrIntervalRequired) {
		t.Errorf("expected ErrIntervalRequired, got %v", err)
	}
}

type fakeSink struct {
	mu       sync.Mutex
	outcomes []string
}

func (s *fakeSink) RecordOutcome(outcome string, _ float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcomes = append(s.outcomes, outcome)
}

func (s *fakeSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.outcomes)
}

func (s *fakeSink) lastOutcome() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.outcomes) == 0 {
		return ""
	}
	return s.outcomes[len(s.outcomes)-1]
}
