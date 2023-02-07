// Package randommix ships a traffic.Generator that draws each field
// of a Request from an operator-configurable weighted distribution.
// See traffic-gen/ADR-0001 for the design rationale.
//
// The Generator is NOT safe for concurrent use: it holds a
// *rand.Rand whose source is not internally synchronized. Operators
// running multiple poster goroutines wrap the Generator with a
// per-call sync.Mutex or instantiate one Generator per goroutine
// from a different seed.
//
// Amount is intentionally not configurable in this adapter. Markup-svc
// today does not act on Amount (the parser grammar is string/set
// only); operators that want biased Amount distributions either
// land a follow-up adapter or wrap the shipped Generator with a
// decorator that fills the Amount field post-Next().
package randommix
