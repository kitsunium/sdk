// Package stream is the public facade for the SDK's durable, partitioned,
// offset-addressed log: a producer appends records, and any number of
// consumer groups read every one of them, each at its own pace, until the
// stream's retention removes it (ADR 0231).
//
//	s, err := stream.NewFile(stream.FileConfig{
//		Dir:    "/var/lib/myapp/orders",
//		Policy: stream.Policy{Partitions: 8, RetainFor: 7 * 24 * time.Hour},
//	})
//
//	// …in the producer: one batch, one partition, one sync.
//	appended, err := s.Append(ctx, []stream.Record{{Key: orderID, Payload: body}})
//
//	// …in each consumer group, possibly in another process:
//	err = stream.Subscribe(ctx, s, stream.SubscriberConfig{
//		Group:               "billing",
//		Handler:             bill, // one partition's records, in offset order
//		HandlerIsIdempotent: true, // required; see below
//	})
//
// # This is not a queue, and the difference is the whole design
//
// pkg/v1/data/queue and this package both carry bytes from one process to
// another, durably. They keep them for opposite reasons:
//
//	                  | queue                      | stream (this package)
//	------------------|----------------------------|---------------------------
//	A record is kept  | until a consumer acks it   | until its retention ends
//	Readers           | compete for each message   | every group reads every one
//	Position          | a lease the broker holds   | an offset the reader names
//	Order             | none past the first retry  | per partition, per key
//	Replay            | impossible: Ack removed it | read from an older offset
//	A failing record  | dead-lettered, others pass | blocks its partition, or is
//	                  |                            | parked by the subscriber
//	Backpressure      | none on Publish            | MaxBytes, then STREAM_FULL
//
// If several workers should share a pile of jobs and each job be done once,
// you want a queue. If several independent readers — billing, search, an
// audit — must each see every record, in order, and a new reader must be able
// to start from the beginning, you want a stream.
//
// # Keys, partitions and order
//
// A record's partition is its key's, by [PartitionOf]: FNV-1a modulo
// [Policy].Partitions, the same on every engine, so the records of one key
// are read in the order they were appended wherever they are kept. Order
// holds within a partition and nowhere else. An [Stream].Append is one batch
// on one partition under one sync: records whose keys hash apart are refused
// with [PartitionsMixed], and a batch of unkeyed records is spread
// round-robin. [Policy].Partitions cannot change for the life of a stream: a
// key's partition is its hash modulo that number.
//
// # Reading is by offset, and nothing is removed by reading
//
// [Stream].Read returns the entries of one partition from an offset, at most
// max of them, and returns at once — empty at the end. [Stream].Bounds says
// the first offset still retained and the next one to be assigned; a read
// below the first is [OffsetTrimmed], past the next [OffsetAhead]. The
// siblings, reached by type assertion on every engine here, do the rest: a
// [Waker] signal ends an idle reader's wait on the next Append in this
// process, a [Seeker] turns an instant into an offset, a [Trimmer] drops a
// partition's prefix, a [Compactor] keeps the last record of each key and a
// [Committer] keeps each group's offset.
//
// # A consumer group commits its offset, at least once by default
//
// [Subscribe] runs one worker per partition for a group, hands the handler
// the partition's records in offset order, and commits the offset after a
// batch the handler accepted. A crash between the handler's return and that
// commit delivers the batch again, so [SubscriberConfig].HandlerIsIdempotent
// must be true: its zero is refused, as the queue's is. A handler that keeps
// failing blocks its partition and only its partition — that is what
// per-key order costs —, unless [SubscriberConfig].Park sends the record to a
// queue as a dead letter and moves on.
//
// # Exactly once in effect, in your own database
//
// [NewSQL] keeps the stream in three tables of the database your application
// already writes, created by [SQLMigration]. An Append made inside your
// transaction is a savepoint of it: its records exist if and only if your
// transaction commits, which makes the stream an outbox. Under
// [CommitByHandler] the handler commits the group's offset itself, through
// the stream's [Committer], in the transaction that holds its effect: the
// effect and the offset commit or roll back together — exactly once in
// effect, the one guarantee no transport alone can give.
//
// # Retention, backpressure and compaction
//
// [Policy].RetainFor and [Policy].RetainBytes bound what a partition keeps;
// zero keeps everything. [Policy].MaxBytes bounds the whole stream: above it
// an Append waits up to [Policy].BlockTimeout for retention to free room,
// then is refused with [StreamFull]. A [Compactor] keeps a key's last record
// and drops the older ones — a record with a key and no payload is a
// tombstone — leaving their offsets as holes a read skips, never renumbered.
//
// # Three engines
//
// [NewFile] keeps the stream in a directory of segments, one log per
// partition, and survives the process; one engine opens a directory at a
// time. [NewSQL] keeps it in your database, joined to your transaction.
// [NewMemory] is the test double: the port's full semantics and the same
// refusals, through the same guard, with nothing that survives the process.
package stream
