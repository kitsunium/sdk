// Package logger — optional capability sub-interfaces detected by type
// assertion. A Sink that implements one of these gains a richer protocol
// with the compose layer; middlewares that need a capability fall back to
// the base Sink.Write contract when it is absent.
package logger

import "context"

// BatchSink is the optional batching capability. A sink that satisfies
// BatchSink receives N records in one call from Async / Multi /
// async-aware middlewares — the genericHandler fans out to WriteBatch
// instead of N sequential Write calls.
//
// Detection happens once at the middleware's construction time via
// type assertion; the hot path stays branch-free.
type BatchSink interface {
	Sink
	// WriteBatch delivers N records as a single call. The recs and payloads
	// slices MUST be the same length, and recs[i] corresponds to payloads[i].
	// Returns the total bytes successfully forwarded plus the first error
	// encountered; partial-batch policy is the implementer's contract (the
	// retry middleware reads n to decide whether to resume mid-batch).
	WriteBatch(ctx context.Context, recs []RecordEvent, payloads [][]byte) (n int, err error)
}

// SyncSink is the optional durability capability. A sink that satisfies
// SyncSink can guarantee on-disk durability (or its equivalent — fsync,
// remote-side acknowledged write) on demand, distinct from Sink.Flush which
// only drains in-memory buffers.
//
// The distinction matters for crash-safe consumers: Flush returns when the
// records have left the SDK; Sync returns when the records survive a
// power loss.
type SyncSink interface {
	Sink
	// Sync forces the underlying transport to confirm durability. For file
	// sinks this maps to fsync/fdatasync; for remote sinks it waits for the
	// server-side ack. A sink that has no stronger guarantee than Flush
	// should NOT implement SyncSink.
	Sync(ctx context.Context) (err error)
}
