// Package rate ships the RateProfile port and three concrete
// profiles (SteadyProfile, LinearProfile, ExponentialProfile) that
// describe how target QPS varies over the life of a poster run.
// See traffic-gen/ADR-0003 for the design rationale.
//
// Profiles are pure functions of elapsed time: QPS(t) for the same
// t always returns the same value. This makes profiles trivially
// unit-testable and lets the poster pull the current target without
// holding a lock or maintaining shared state with the profile.
//
// Parse decodes the DSL operators pass via the --profile cmd flag:
//
//	steady:500                  // constant 500 QPS until SIGINT
//	linear:100->5000@15m        // linear ramp 100 -> 5000 over 15m
//	exp:10->10000@5m            // exponential ramp 10 -> 10000 over 5m
//
// Beyond the profile's Duration, all profiles clamp to EndQPS and
// report that indefinitely. The cmd-level --duration flag and the
// profile's Duration compose: the run ends on whichever fires first.
//
// Profiles are safe for concurrent use by multiple goroutines: each
// is a value type with no mutable state, so a single profile shared
// across the poster and any future observability surface needs no
// synchronization.
package rate
