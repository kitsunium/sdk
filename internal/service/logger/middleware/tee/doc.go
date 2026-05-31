// Package tee provides a logger middleware that fans each record out to
// every primary sink and, only when all primaries reject it, spills the
// record to a dead-letter sink.
//
// # Scope
//
// This middleware owns exactly one seam: the dead-letter (spill) path. It
// delivers a record to every primary core/logger.Sink, aggregates their
// errors, and — when every primary failed — routes the record to an
// optional spill sink so it is not silently lost.
//
// # Non-goals
//
// Plain fan-out to N sinks belongs to the multi middleware; ordered
// fallback across sinks belongs to the failover middleware. This package
// does not duplicate either — it adds only the spill/dead-letter seam.
//
// Durable retry and backoff are also out of scope: the spill sink is a
// dead-letter seam, not a retry queue. A consumer that needs durable retry
// composes that behind the spill sink.
//
// # Concurrency
//
// A TeeSink is safe for concurrent producers when its primary and spill
// sinks are. It holds no mutable per-record state.
package tee
