// Package multi implements a fanout Sink that broadcasts each Write to a
// fixed list of downstream sinks. A single failure does NOT short-circuit
// the fanout — every sink receives the payload, and the per-sink errors
// are aggregated via errors.Join so callers see the complete failure set.
//
// Use case: emit logs to console + file + remote drain simultaneously, with
// independent failure modes per branch.
package multi
