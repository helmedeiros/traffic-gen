// Package traffic defines the domain types for the traffic-gen
// project. See ADR-0001 for the design rationale.
//
// The domain has three artefacts:
//
//   - Request: the local mirror of the markup-svc /decide JSON body.
//     traffic-gen owns the wire shape it produces; the cookbook
//     fidelity test confirms a round-trip against a running markup-svc.
//
//   - Generator: the one-method port through which the binary produces
//     synthetic Request shapes. Adapter packages own the distribution
//     strategy (random mix, replayed, sweep) and document their
//     concurrency posture.
//
//   - Poster: the one-method port through which the binary pushes
//     generated requests at a target URL at a configurable rate.
//     Adapter packages own the HTTP-client choice and the QPS policy.
//
// The split keeps "what to generate" and "how fast to push" tunable
// independently; a single-goroutine Poster paired with a high-fanout
// Generator and vice versa are both valid configurations.
package traffic
