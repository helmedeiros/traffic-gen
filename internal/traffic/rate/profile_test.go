package rate_test

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/helmedeiros/traffic-gen/internal/traffic/rate"
)

// Compile-time assertions that each shipped profile satisfies the
// RateProfile port. A regression that drifted any method signature
// would fail to build before the tests run.
var (
	_ rate.RateProfile = rate.SteadyProfile{}
	_ rate.RateProfile = rate.LinearProfile{}
	_ rate.RateProfile = rate.ExponentialProfile{}
)

func TestSteadyProfileQPSIsConstant(t *testing.T) {
	p := rate.SteadyProfile{TargetQPS: 500}
	for _, elapsed := range []time.Duration{0, time.Millisecond, time.Second, time.Hour} {
		if got := p.QPS(elapsed); got != 500 {
			t.Errorf("QPS(%s) = %d, want 500", elapsed, got)
		}
	}
}

func TestSteadyProfileDurationIsZero(t *testing.T) {
	p := rate.SteadyProfile{TargetQPS: 100}
	if got := p.Duration(); got != 0 {
		t.Errorf("Duration = %s, want 0 (infinite)", got)
	}
}

func TestLinearProfileEndpoints(t *testing.T) {
	p := rate.LinearProfile{StartQPS: 100, EndQPS: 500, Total: time.Minute}
	if got := p.QPS(0); got != 100 {
		t.Errorf("QPS(0) = %d, want 100", got)
	}
	if got := p.QPS(time.Minute); got != 500 {
		t.Errorf("QPS(Total) = %d, want 500", got)
	}
	if got := p.QPS(30 * time.Second); got != 300 {
		t.Errorf("QPS(midpoint) = %d, want 300", got)
	}
}

func TestLinearProfileBeyondDurationHoldsEndQPS(t *testing.T) {
	p := rate.LinearProfile{StartQPS: 100, EndQPS: 500, Total: time.Minute}
	if got := p.QPS(2 * time.Minute); got != 500 {
		t.Errorf("QPS(2*Total) = %d, want 500 (clamp)", got)
	}
}

func TestLinearProfileRampDown(t *testing.T) {
	p := rate.LinearProfile{StartQPS: 500, EndQPS: 100, Total: time.Minute}
	if got := p.QPS(30 * time.Second); got != 300 {
		t.Errorf("QPS(midpoint) = %d, want 300 (ramp down midpoint)", got)
	}
	if got := p.QPS(time.Minute); got != 100 {
		t.Errorf("QPS(Total) = %d, want 100", got)
	}
}

func TestExponentialProfileEndpoints(t *testing.T) {
	p := rate.ExponentialProfile{StartQPS: 10, EndQPS: 1000, Total: time.Minute}
	if got := p.QPS(0); got != 10 {
		t.Errorf("QPS(0) = %d, want 10", got)
	}
	if got := p.QPS(time.Minute); got != 1000 {
		t.Errorf("QPS(Total) = %d, want 1000", got)
	}
	// Geometric mean of 10 and 1000 is 100 (sqrt(10*1000) == 100).
	if got := p.QPS(30 * time.Second); got != 100 {
		t.Errorf("QPS(midpoint) = %d, want 100 (geometric mean)", got)
	}
}

func TestExponentialProfileRampDown(t *testing.T) {
	p := rate.ExponentialProfile{StartQPS: 1000, EndQPS: 10, Total: time.Minute}
	if got := p.QPS(30 * time.Second); got != 100 {
		t.Errorf("QPS(midpoint) = %d, want 100 (downward geometric mean)", got)
	}
}

func TestExponentialProfileBeyondDurationHoldsEndQPS(t *testing.T) {
	p := rate.ExponentialProfile{StartQPS: 10, EndQPS: 1000, Total: time.Minute}
	if got := p.QPS(2 * time.Minute); got != 1000 {
		t.Errorf("QPS(2*Total) = %d, want 1000 (clamp)", got)
	}
}

func TestParseSteady(t *testing.T) {
	p, err := rate.Parse("steady:500")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	steady, ok := p.(rate.SteadyProfile)
	if !ok {
		t.Fatalf("got %T, want SteadyProfile", p)
	}
	if steady.TargetQPS != 500 {
		t.Errorf("TargetQPS = %d, want 500", steady.TargetQPS)
	}
	if steady.Total != 0 {
		t.Errorf("Total = %s, want 0", steady.Total)
	}
}

func TestParseLinear(t *testing.T) {
	p, err := rate.Parse("linear:100->5000@15m")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	lin, ok := p.(rate.LinearProfile)
	if !ok {
		t.Fatalf("got %T, want LinearProfile", p)
	}
	if lin.StartQPS != 100 || lin.EndQPS != 5000 || lin.Total != 15*time.Minute {
		t.Errorf("got %+v, want {Start:100 End:5000 Total:15m}", lin)
	}
}

func TestParseExponential(t *testing.T) {
	p, err := rate.Parse("exp:10->10000@5m")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	exp, ok := p.(rate.ExponentialProfile)
	if !ok {
		t.Fatalf("got %T, want ExponentialProfile", p)
	}
	if exp.StartQPS != 10 || exp.EndQPS != 10000 || exp.Total != 5*time.Minute {
		t.Errorf("got %+v, want {Start:10 End:10000 Total:5m}", exp)
	}
}

func TestParseAcceptsFlatRamp(t *testing.T) {
	for _, spec := range []string{"linear:100->100@5m", "exp:100->100@5m"} {
		p, err := rate.Parse(spec)
		if err != nil {
			t.Errorf("Parse(%q): %v", spec, err)
			continue
		}
		// Flat ramp: QPS at any elapsed in [0, Total] should be 100.
		if got := p.QPS(time.Minute); got != 100 {
			t.Errorf("Parse(%q): midpoint QPS = %d, want 100", spec, got)
		}
	}
}

func TestParseAcceptsDownwardRamps(t *testing.T) {
	for _, spec := range []string{"linear:500->100@1m", "exp:1000->10@1m"} {
		if _, err := rate.Parse(spec); err != nil {
			t.Errorf("Parse(%q): %v", spec, err)
		}
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	cases := []struct {
		spec   string
		reason string
	}{
		{"", "want KIND:SPEC"},
		{"steady", "want KIND:SPEC"},
		{"wat:500", "unknown profile kind"},
		{"steady:abc", "must be an integer"},
		{"steady:0", "must be positive"},
		{"steady:-5", "must be positive"},
		{"linear:100->500", "want A->B@DURATION"},
		{"linear:100@5m", "ramp must be A->B"},
		{"linear:abc->500@5m", "StartQPS must be an integer"},
		{"linear:100->abc@5m", "EndQPS must be an integer"},
		{"linear:0->500@5m", "StartQPS must be positive"},
		{"linear:100->0@5m", "EndQPS must be positive"},
		{"linear:100->500@notduration", "not a valid Go duration"},
		{"linear:100->500@0s", "duration must be positive"},
		{"exp:100->500@-1m", "duration must be positive"},
	}
	for _, tc := range cases {
		_, err := rate.Parse(tc.spec)
		if err == nil {
			t.Errorf("Parse(%q) accepted; want error %q", tc.spec, tc.reason)
			continue
		}
		if !strings.Contains(err.Error(), tc.reason) {
			t.Errorf("Parse(%q) err = %q, want it to mention %q", tc.spec, err.Error(), tc.reason)
		}
	}
}

// TestExponentialMidpointMatchesGeometricMean is the analytical
// regression guard: for any (a, b) with both positive, the
// exponential profile's QPS at the midpoint must equal round(sqrt(a*b))
// within floating-point precision. A regression that swapped to
// arithmetic interpolation would fail this on every non-degenerate pair.
func TestExponentialMidpointMatchesGeometricMean(t *testing.T) {
	cases := []struct {
		start, end int
	}{
		{10, 1000}, {100, 10000}, {1, 100}, {500, 50}, {1000, 1000},
	}
	for _, tc := range cases {
		p := rate.ExponentialProfile{StartQPS: tc.start, EndQPS: tc.end, Total: time.Minute}
		got := p.QPS(30 * time.Second)
		want := int(math.Round(math.Sqrt(float64(tc.start) * float64(tc.end))))
		if got != want {
			t.Errorf("exp{%d->%d} midpoint = %d, want %d (geometric mean)", tc.start, tc.end, got, want)
		}
	}
}

// TestNegativeElapsedClampsToStartQPS pins the documented contract
// that LinearProfile and ExponentialProfile clamp to StartQPS for
// any elapsed <= 0. The poster only passes elapsed = time.Since(start)
// (non-negative), so this branch is defensive against future callers
// that compute elapsed differently.
func TestNegativeElapsedClampsToStartQPS(t *testing.T) {
	lin := rate.LinearProfile{StartQPS: 100, EndQPS: 500, Total: time.Minute}
	if got := lin.QPS(-time.Second); got != 100 {
		t.Errorf("LinearProfile.QPS(-1s) = %d, want 100 (StartQPS clamp)", got)
	}
	exp := rate.ExponentialProfile{StartQPS: 10, EndQPS: 1000, Total: time.Minute}
	if got := exp.QPS(-time.Second); got != 10 {
		t.Errorf("ExponentialProfile.QPS(-1s) = %d, want 10 (StartQPS clamp)", got)
	}
}

// TestParseErrorIsNotSomeUnrelatedSentinel is a defensive guard
// that future refactors wrapping parse errors via fmt.Errorf("%w",
// ...) do not accidentally collide with an unrelated sentinel.
func TestParseErrorIsNotSomeUnrelatedSentinel(t *testing.T) {
	_, err := rate.Parse("")
	if err == nil {
		t.Fatal("Parse('') returned nil")
	}
	if errors.Is(err, errSentinelThatShouldNotExist) {
		t.Error("Parse error matched a non-existent sentinel")
	}
}

var errSentinelThatShouldNotExist = errors.New("unused sentinel for the negative-Is check")
