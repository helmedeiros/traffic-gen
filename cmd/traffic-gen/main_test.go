package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/helmedeiros/traffic-gen/internal/traffic"
)

// TestRunPostsAgainstTargetWithDuration is the integration smoke test
// for the cmd binary. Spin up an httptest server, point the binary
// at it with --duration so the run terminates cleanly, and confirm
// the server saw at least a handful of POSTs.
func TestRunPostsAgainstTargetWithDuration(t *testing.T) {
	var hits int64
	var sawContentType, sawValidRequest int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		if r.Header.Get("Content-Type") == "application/json" {
			atomic.StoreInt32(&sawContentType, 1)
		}
		var body traffic.Request
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			atomic.StoreInt32(&sawValidRequest, 1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	var stdout, stderr bytes.Buffer
	args := []string{
		"--target", srv.URL,
		"--qps", "100",
		"--duration", "80ms",
		"--seed", "1",
	}
	if err := run(context.Background(), args, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := atomic.LoadInt64(&hits); got < 3 {
		t.Errorf("server saw %d POSTs; want >= 3 (logs: stderr=%q)", got, stderr.String())
	}
	if atomic.LoadInt32(&sawContentType) == 0 {
		t.Error("server never saw Content-Type: application/json on a POST")
	}
	if atomic.LoadInt32(&sawValidRequest) == 0 {
		t.Error("server never decoded a POST body as a traffic.Request")
	}
	// Boot + done land on stdout as JSON; assert both are present
	// and parseable rather than grepping for a substring.
	stdoutLines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(stdoutLines) < 2 {
		t.Fatalf("stdout has %d lines, want >= 2 (boot + done): %q", len(stdoutLines), stdout.String())
	}
	var boot struct {
		Msg   string                 `json:"msg"`
		Attrs map[string]interface{} `json:"attrs"`
	}
	if err := json.NewDecoder(strings.NewReader(stdoutLines[0])).Decode(&boot); err != nil {
		t.Fatalf("boot line is not JSON: %v (line=%q)", err, stdoutLines[0])
	}
	if boot.Msg != "traffic-gen.boot" {
		t.Errorf("boot msg = %q, want traffic-gen.boot", boot.Msg)
	}
	if _, ok := boot.Attrs["target"]; !ok {
		t.Errorf("boot attrs missing target: %+v", boot.Attrs)
	}
	if !strings.Contains(stderr.String(), "poster: done") {
		t.Errorf("stderr missing poster summary: %q", stderr.String())
	}
}

func TestRunReturnsErrorOnBadFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"--qps", "not-a-number"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("run accepted bad --qps; want error")
	}
}

func TestRunReturnsErrorOnNegativeQPS(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"--qps", "-1", "--target", "http://example/decide"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("run accepted --qps=-1; want error")
	}
}

func TestRunRejectsUnknownPreset(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{
		"--target", "http://example/decide",
		"--preset", "made-up",
		"--duration", "10ms",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("run accepted unknown preset; want error")
	}
	if !strings.Contains(err.Error(), "made-up") {
		t.Errorf("err %q does not name the offending preset", err)
	}
}

func TestRunPresetFlagWorks(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	for _, name := range []string{"default", "uniform", "stress-no-match"} {
		var stdout, stderr bytes.Buffer
		args := []string{
			"--target", srv.URL,
			"--qps", "100",
			"--duration", "30ms",
			"--seed", "1",
			"--preset", name,
		}
		if err := run(context.Background(), args, &stdout, &stderr); err != nil {
			t.Errorf("preset %q: run: %v", name, err)
		}
		// Boot line should mention the chosen preset.
		var boot struct {
			Attrs map[string]interface{} `json:"attrs"`
		}
		_ = json.NewDecoder(strings.NewReader(strings.Split(stdout.String(), "\n")[0])).Decode(&boot)
		if got, _ := boot.Attrs["preset"].(string); got != name {
			t.Errorf("preset %q: boot attrs.preset = %q, want %q", name, got, name)
		}
	}
}

// TestRunStopsOnContextCancel covers the SIGINT/SIGTERM path the
// real main() uses: a ctx cancel during a long run returns nil
// without crashing.
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

	var stdout, stderr bytes.Buffer
	args := []string{"--target", srv.URL, "--qps", "50"}
	start := time.Now()
	if err := run(ctx, args, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v", err)
	}
	if time.Since(start) > 300*time.Millisecond {
		t.Errorf("run took %s after ctx canceled at 40ms; want <300ms", time.Since(start))
	}
}
