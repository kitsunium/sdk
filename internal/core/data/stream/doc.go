// Package stream declares the SDK's durable, partitioned, offset-addressed
// log: the [Stream] a producer appends records to and any number of readers
// read at their own pace, each record kept until its retention ends rather
// than until somebody acknowledges it (ADR 0231).
//
// It is the left-hand column of a frontier core/data/queue draws on its right
// (ADR 0054). A queue removes a message at its acknowledgement, leases it to
// one consumer at a time, keeps no order past the first retry and
// dead-letters what fails; a stream keeps every record for its retention,
// lets every reader name the offset it reads from, keeps the order of each
// partition, and leaves a failing record where it is — the reader that cannot
// handle it parks, and every other reader goes on.
//
//	value, err := s.Append(ctx, []stream.RecordValue{{Key: order, Payload: body}})
//	entries, err := s.Read(ctx, value.Partition, from, 100)
//
// A record's partition is its key's, by [PartitionOf] — FNV-1a modulo the
// partition count —, the same on every engine, so a key's records are read in
// the order they were appended wherever they are kept. Ordering holds within a
// partition and nowhere else.
//
// The port is FROZEN at four methods (ADR 0039); every other capability is a
// sibling reached by type assertion: [Waker], [Seeker], [Trimmer],
// [Compactor] and [Committer].
//
// Package stream — range 0.2.38.* (ADR 0231 core/data/stream block), and the
// engines' and the subscriber's 0.3.94.* (ADR 0231 service/data/stream block,
// declared here as ADR 0160 requires).
//
// Package stream — hosts the stream policy every engine is configured with,
// and the guard every engine runs so that all of them refuse identical inputs
// identically (ADR 0031).
package stream
