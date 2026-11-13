package plug

import (
	"context"
	"sync/atomic"
	"time"
)

// The stream subsystem's contract (issue #826, ADR 0179 step 4's shape):
// what kit's core asks of the log a streamed topic appends to and its
// subscriptions read as consumer groups. framework/app/stream sets the hook
// and framework/internal/app/streamengine implements it over the SDK's
// stream; the core reads it and imports none of the SDK's stream. The core
// encodes every message itself: a stream carries bytes.

// Streams is the stream subsystem: the log a streamed topic appends to and
// the migration of its tables on a database; nil until framework/app/stream
// is imported. A product that declares a streamed topic without it is
// refused at the start (ADR 0179 §6).
var Streams atomic.Pointer[StreamDriverConfig]

// StreamDriverConfig is what framework/app/stream plugs into kit: how a
// streamed topic's log opens, and the migration that makes its tables on a
// database.
type StreamDriverConfig struct {
	// Open opens one stream, as cfg places it: on a database, in a
	// directory, or in memory.
	Open func(cfg StreamConfig) (Stream, error)
	// TableMigration is the migration that makes a stream's tables on a
	// database of the dialect named, at version: the SDK's SQL stream's
	// three tables, named from table.
	TableMigration func(dialect, table string, version uint64) (MigrationValue, error)
}

// StreamConfig is what the framework asks of one stream: where it lies, how
// many partitions it has, and the app's clock.
type StreamConfig struct {
	// Name names the stream's node in a refusal: the topic's node ID.
	Name string
	// Dir is the directory a file stream lies in; with no Database and no
	// Dir, the stream is in memory.
	Dir string
	// Database keeps the stream, nil when Dir or memory does: a scope the
	// store subsystem's Database opened, whose transaction an append in it
	// joins.
	Database Database
	// Table is the base name of the stream's tables on Database.
	Table string
	// Partitions is how many partitions the stream has; it cannot change
	// for the life of a stream.
	Partitions int
	// Clock is the app's clock, which the stream stamps and waits on.
	Clock Clock
	// Wrap, when a test sets it, stands in front of the stream the
	// subsystem opens: a func(stream.Stream) stream.Stream of the SDK's
	// stream, which the framework's core does not name. Nil in every app a
	// product builds.
	Wrap any
}

// StreamSubscribeConfig is how one consumer group of a stream runs: its
// name, where a group the stream does not know yet starts, its handler,
// how many records it handles at once, and how it stops.
type StreamSubscribeConfig struct {
	// From, when FromBeginning is false and From is not zero, is the
	// instant a new group starts at: the first record appended at or after
	// it. Neither set, a new group starts after the last record appended.
	From time.Time
	// Handler handles one record: nil commits the group's offset past it,
	// an error hands it back, retried after a growing wait, and its
	// partition waits for it. Every handler the framework runs tolerates a
	// redelivery.
	Handler func(ctx context.Context, r StreamRecordValue) error
	// Group names the consumer group whose offsets are read and committed.
	Group string
	// PollInterval bounds an idle wait for what another process appended.
	PollInterval time.Duration
	// DrainTimeout is how long the handler running when ctx ends may still
	// run before its context is cancelled; no record starts once ctx ended.
	DrainTimeout time.Duration
	// SettleTimeout bounds each commit sent once ctx ended.
	SettleTimeout time.Duration
	// Parallelism is how many records, of distinct partitions, are handled
	// at once.
	Parallelism int
	// FromBeginning makes a new group start at the first record the stream
	// still keeps.
	FromBeginning bool
}

// StreamRecordValue is one record of a stream, as a group reads it: where
// it lies, when it was appended, its key and its payload as the framework
// encoded it.
type StreamRecordValue struct {
	// AppendedAt is when the record was appended.
	AppendedAt time.Time
	// Key is the record's key, which chose its partition.
	Key string
	// Payload is the message as the framework encoded it.
	Payload []byte
	// Partition is the partition the record lies in.
	Partition int
	// Offset is the record's offset in its partition.
	Offset int64
}
