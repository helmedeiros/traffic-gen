package rate

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// RateProfile expresses the target QPS as a function of elapsed
// time since profile start. Implementations are pure: QPS(t) for
// the same t always returns the same value. The poster recomputes
// the next-send interval on every send by asking the profile for
// the current QPS, so a profile that varies smoothly produces a
// smooth send rate.
//
// QPS returns a non-negative target. The poster treats QPS == 0
// as "pause sending and recheck in 100ms"; profile authors can use
// this to express transient gaps without stopping the run.
//
// Duration returns the profile's total length. Zero means infinite
// (the profile reports a steady QPS forever); the run ends only on
// context cancellation or the cmd-level --duration flag.
type RateProfile interface {
	QPS(elapsed time.Duration) int
	Duration() time.Duration
}

// SteadyProfile holds QPS constant for the whole run. The
// v0.0.1 --qps cmd flag is the boot-time alias for this profile
// with Duration zero.
type SteadyProfile struct {
	TargetQPS int
	Total     time.Duration
}

// QPS returns the configured TargetQPS at any elapsed time.
func (p SteadyProfile) QPS(time.Duration) int {
	return p.TargetQPS
}

// Duration returns p.Total. Zero is infinite.
func (p SteadyProfile) Duration() time.Duration {
	return p.Total
}

// LinearProfile varies QPS linearly between StartQPS at elapsed=0
// and EndQPS at elapsed=Total. Operators ramp up by setting EndQPS
// > StartQPS and ramp down by reversing the inequality. After Total,
// QPS clamps to EndQPS and reports that indefinitely.
type LinearProfile struct {
	StartQPS int
	EndQPS   int
	Total    time.Duration
}

// QPS interpolates linearly between StartQPS and EndQPS. Clamps to
// StartQPS for elapsed <= 0 and to EndQPS for elapsed >= Total.
func (p LinearProfile) QPS(elapsed time.Duration) int {
	if elapsed <= 0 {
		return p.StartQPS
	}
	if elapsed >= p.Total {
		return p.EndQPS
	}
	frac := float64(elapsed) / float64(p.Total)
	value := float64(p.StartQPS) + frac*float64(p.EndQPS-p.StartQPS)
	return int(math.Round(value))
}

// Duration returns p.Total.
func (p LinearProfile) Duration() time.Duration {
	return p.Total
}

// ExponentialProfile varies QPS log-linearly between StartQPS at
// elapsed=0 and EndQPS at elapsed=Total. The midpoint QPS is the
// geometric mean sqrt(StartQPS * EndQPS), which captures the
// "thundering herd" shape operators want to stress recovery and
// cache-warming behavior in the target service.
//
// StartQPS and EndQPS must both be positive; an exponential ramp
// through zero has no defined log-linear interpolation. Parse
// enforces this; direct construction with a zero or negative value
// produces QPS values that are mathematically meaningless. Downward
// ramps (StartQPS > EndQPS) are supported and use the same log-linear
// math.
type ExponentialProfile struct {
	StartQPS int
	EndQPS   int
	Total    time.Duration
}

// QPS interpolates log-linearly between StartQPS and EndQPS. Clamps
// to StartQPS for elapsed <= 0 and to EndQPS for elapsed >= Total.
func (p ExponentialProfile) QPS(elapsed time.Duration) int {
	if elapsed <= 0 {
		return p.StartQPS
	}
	if elapsed >= p.Total {
		return p.EndQPS
	}
	frac := float64(elapsed) / float64(p.Total)
	// log-linear: q(t) = start * (end/start)^frac
	ratio := float64(p.EndQPS) / float64(p.StartQPS)
	value := float64(p.StartQPS) * math.Pow(ratio, frac)
	return int(math.Round(value))
}

// Duration returns p.Total.
func (p ExponentialProfile) Duration() time.Duration {
	return p.Total
}

// Parse decodes a profile spec from the DSL grammar described in
// ADR-0003:
//
//	steady:N                # SteadyProfile{TargetQPS: N, Total: 0}
//	linear:A->B@T           # LinearProfile{StartQPS: A, EndQPS: B, Total: T}
//	exp:A->B@T              # ExponentialProfile{StartQPS: A, EndQPS: B, Total: T}
//
// Returns a descriptive error naming what went wrong; cmd-side
// callers re-emit the error verbatim so the operator sees the
// offending spec at boot.
func Parse(spec string) (RateProfile, error) {
	kind, body, ok := splitTwo(spec, ":")
	if !ok {
		return nil, fmt.Errorf("invalid profile spec %q: want KIND:SPEC", spec)
	}
	switch kind {
	case "steady":
		return parseSteady(body)
	case "linear":
		return parseLinear(body)
	case "exp":
		return parseExponential(body)
	default:
		return nil, fmt.Errorf("unknown profile kind %q (want one of: steady, linear, exp)", kind)
	}
}

func parseSteady(body string) (RateProfile, error) {
	qps, err := strconv.Atoi(body)
	if err != nil {
		return nil, fmt.Errorf("steady profile: QPS must be an integer, got %q", body)
	}
	if qps <= 0 {
		return nil, fmt.Errorf("steady profile: QPS must be positive, got %d", qps)
	}
	return SteadyProfile{TargetQPS: qps}, nil
}

func parseLinear(body string) (RateProfile, error) {
	start, end, dur, err := parseRamp(body, "linear")
	if err != nil {
		return nil, err
	}
	return LinearProfile{StartQPS: start, EndQPS: end, Total: dur}, nil
}

func parseExponential(body string) (RateProfile, error) {
	start, end, dur, err := parseRamp(body, "exp")
	if err != nil {
		return nil, err
	}
	// parseRamp already rejects start <= 0 and end <= 0, which is
	// the log-linear positivity contract.
	return ExponentialProfile{StartQPS: start, EndQPS: end, Total: dur}, nil
}

// parseRamp decodes "A->B@T" used by both linear and exp profiles.
// kind is the profile kind used only for error wording.
func parseRamp(body, kind string) (start, end int, dur time.Duration, err error) {
	rampSpec, durSpec, ok := splitTwo(body, "@")
	if !ok {
		return 0, 0, 0, fmt.Errorf("%s profile: want A->B@DURATION, got %q", kind, body)
	}
	startSpec, endSpec, ok := splitTwo(rampSpec, "->")
	if !ok {
		return 0, 0, 0, fmt.Errorf("%s profile: ramp must be A->B, got %q", kind, rampSpec)
	}
	start, err = strconv.Atoi(startSpec)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("%s profile: StartQPS must be an integer, got %q", kind, startSpec)
	}
	end, err = strconv.Atoi(endSpec)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("%s profile: EndQPS must be an integer, got %q", kind, endSpec)
	}
	if start <= 0 {
		return 0, 0, 0, fmt.Errorf("%s profile: StartQPS must be positive, got %d", kind, start)
	}
	if end <= 0 {
		return 0, 0, 0, fmt.Errorf("%s profile: EndQPS must be positive, got %d", kind, end)
	}
	dur, err = time.ParseDuration(durSpec)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("%s profile: duration %q is not a valid Go duration: %w", kind, durSpec, err)
	}
	if dur <= 0 {
		return 0, 0, 0, fmt.Errorf("%s profile: duration must be positive, got %s", kind, dur)
	}
	return start, end, dur, nil
}

// splitTwo splits s on the first occurrence of sep and returns the
// two halves. The third return is false when sep does not appear.
func splitTwo(s, sep string) (string, string, bool) {
	idx := strings.Index(s, sep)
	if idx < 0 {
		return "", "", false
	}
	return s[:idx], s[idx+len(sep):], true
}
