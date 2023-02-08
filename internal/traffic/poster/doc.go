// Package poster ships the first traffic.Poster adapter: a
// single-goroutine HTTP loop that paces requests against a target
// QPS budget and POSTs JSON-encoded Request bodies to a configured
// URL. See traffic-gen/ADR-0001 for the design rationale.
//
// The Poster is intentionally single-goroutine in this release. The
// observed throughput on commodity hardware against a localhost
// markup-svc is enough to surface the distributional-shape
// concerns the project exists to study; a worker-pool Poster is a
// follow-up ADR if measured throughput proves insufficient.
//
// Run returns a Summary describing the wall-clock duration, the
// number of requests attempted, and the per-class outcome counts.
// HTTP transport failures (connection refused, timeout) are
// classified separately from non-2xx responses so a misbehaving
// downstream and a misbehaving network can be distinguished from
// the log alone.
package poster
