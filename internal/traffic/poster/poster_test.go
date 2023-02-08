package poster_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/helmedeiros/traffic-gen/internal/traffic"
	"github.com/helmedeiros/traffic-gen/internal/traffic/poster"
)

// stubGenerator returns a fixed Request on every Next.
type stubGenerator struct{ req traffic.Request }

func (g *stubGenerator) Next() traffic.Request { return g.req }

func TestNewRejectsEmptyTargetURL(t *testing.T) {
	if _, err := poster.New(poster.Config{QPS: 10}); err == nil {
		t.Fatal("New accepted empty TargetURL; want error")
	}
}

func TestNewRejectsNonPositiveQPS(t *testing.T) {
	for _, qps := range []int{0, -1} {
		_, err := poster.New(poster.Config{TargetURL: "http://example", QPS: qps})
		if err == nil {
			t.Errorf("New accepted QPS=%d; want error", qps)
		}
	}
}

func TestRunPostsAgainstTargetAndCountsSuccesses(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		if r.Method != http.MethodPost {
			t.Errorf("server saw method %q, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("server saw Content-Type %q, want application/json", got)
		}
		var body traffic.Request
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("server failed to decode body: %v", err)
		}
		if body.Country != "BR" {
			t.Errorf("server saw country %q, want BR (generator stub)", body.Country)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	out := &strings.Builder{}
	p, err := poster.New(poster.Config{
		TargetURL: srv.URL,
		QPS:       100,
		Duration:  60 * time.Millisecond,
		Out:       out,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	gen := &stubGenerator{req: traffic.Request{Country: "BR"}}
	if err := p.Run(context.Background(), gen); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := atomic.LoadInt64(&hits); got < 3 {
		t.Errorf("server saw %d POSTs in 60ms at 100 QPS; want >= 3", got)
	}
	if !strings.Contains(out.String(), "successes=") {
		t.Errorf("Out summary missing successes count: %q", out.String())
	}
}

func TestRunClassifiesOutcomesByStatus(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt64(&hits, 1)
		// Alternate the response so each bucket lands at least once
		// within the run window.
		switch n % 4 {
		case 1:
			w.WriteHeader(http.StatusOK)
		case 2:
			w.WriteHeader(http.StatusNotFound)
		case 3:
			w.WriteHeader(http.StatusInternalServerError)
		case 0:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)

	out := &strings.Builder{}
	p, _ := poster.New(poster.Config{
		TargetURL: srv.URL,
		QPS:       200,
		Duration:  80 * time.Millisecond,
		Out:       out,
	})
	if err := p.Run(context.Background(), &stubGenerator{}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	summary := out.String()
	for _, want := range []string{"successes=", "not_matches=", "client_errors=", "server_errors="} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary %q missing bucket label %q", summary, want)
		}
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(40 * time.Millisecond)
		cancel()
	}()

	out := &strings.Builder{}
	p, _ := poster.New(poster.Config{
		TargetURL: srv.URL,
		QPS:       50,
		Out:       out,
	})
	start := time.Now()
	if err := p.Run(ctx, &stubGenerator{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 300*time.Millisecond {
		t.Errorf("Run took %s, want <300ms (ctx canceled at 40ms)", elapsed)
	}
}

func TestRunRecordsTransportErrorsOnDeadTarget(t *testing.T) {
	out := &strings.Builder{}
	p, _ := poster.New(poster.Config{
		TargetURL: "http://127.0.0.1:1/decide", // port 1 reserved; refuses connection
		QPS:       100,
		Duration:  40 * time.Millisecond,
		Client:    &http.Client{Timeout: 20 * time.Millisecond},
		Out:       out,
	})
	if err := p.Run(context.Background(), &stubGenerator{}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(out.String(), "transport_errors=") {
		t.Errorf("summary missing transport_errors: %q", out.String())
	}
	// Drain any leftover stderr-style output for valgrind cleanliness.
	_, _ = io.Copy(io.Discard, strings.NewReader(out.String()))
}

func TestRunRejectsNilGenerator(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	p, _ := poster.New(poster.Config{TargetURL: srv.URL, QPS: 10})
	if err := p.Run(context.Background(), nil); err == nil {
		t.Fatal("Run accepted nil generator; want error")
	}
}
