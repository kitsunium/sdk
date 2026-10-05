// Package dbsink is the driver-AGNOSTIC database sink shell (ADR 0015, gated).
// It is the DB analogue of the S3 batching sink (third-party/aws/writer/s3):
// records are coalesced into batches by the generic kernel batcher and handed
// to a single deliver seam — execBatch — when the item-count cap is reached, on
// the optional flush ticker, or on Flush / Close. The seam is the only thing a
// concrete driver supplies; this package imports NO database driver, NO vendor
// SDK, and NO new core sibling, so it stays stdlib-only and dep-light. The real
// driver adapters (third-party/db/writer/{mysql,clickhouse,redis}) wrap New
// with their own execBatch closure and self-register as writer factories.
//
// Composition: a concrete driver wires levelgate(async(dbSink)) — the gate
// drops below-floor records before the ring, async gives a non-blocking ring +
// OnDrop back-pressure, and the dbSink batches the rest. Compose builds that
// chain for them so the ordering lives in one place (mirroring the s3 factory).
//
// Ownership / allocation: dbSink.Write runs on the async drainer goroutine (the
// producer call already returned at async.Write), and RecordEvent is an
// immutable snapshot by contract, so no defensive per-record clone is needed —
// the batcher's appended value safely outlives async's recycled entry. This
// package therefore makes NO zero-alloc claim on Write: the only Write-side cost
// is the amortised growth of the batcher's pending slice, and the zero-alloc
// invariant belongs to the producer's Build().Send() hot path (ADR 0014), not to
// a deferring sink. CPU/RAM stay minimal by coalescing many records into one
// execBatch round-trip — the DB analogue of the s3 sink's batched object upload.
//
// Package dbsink — the Config value type plus Compose, the single entry point a
// concrete driver adapter uses to build the levelgate(async(dbSink)) chain.
// Keeping the composition order here (not in each driver) means every DB writer
// inherits the same back-pressure + level-floor wiring as the s3 factory, with
// only the execBatch seam and the plain-data Config varying per driver.
package dbsink
