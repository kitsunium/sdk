// Package writer — ClickHouseConfig value type for the "clickhouse" writer.
package writer

import (
	"time"

	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// ClickHouseConfig configures the "clickhouse" writer (registered by
// third-party/db/writer/clickhouse). It carries no driver types — only plain
// data — so it lives in the dep-light core layer; the concrete low-level driver
// (ClickHouse/ch-go) is confined to the third-party adapter. A batch maps to one
// native-protocol block insert, so the sink batches by row count.
type ClickHouseConfig struct {
	// Address is the ClickHouse native-protocol endpoint (host:port, default
	// port 9000); required.
	Address string
	// Database is the target database (required).
	Database string
	// Table is the destination table for log rows (required).
	Table string
	// Credentials yields the DB login on demand and is REQUIRED: a nil provider
	// is rejected with the adapter's ClientInitFailed. AccessKeyID() is the
	// ClickHouse username and SecretAccessKey() is the password (the redacting
	// CredentialValue is reused so the password never leaks into %v/%#v).
	Credentials CredentialProvider
	// MaxRows forces a flush once the pending block reaches this many rows; a
	// non-positive value applies the dbsink shell's default.
	MaxRows int
	// FlushEvery bounds how long a partial block waits before the insert; the
	// zero value flushes only on an explicit Flush / Close.
	FlushEvery time.Duration
	// BufferSize is the async ring capacity; a non-positive value applies
	// async's default. The producer never blocks on the database.
	BufferSize int
	// MinLevel is the optional per-writer severity floor; the zero value
	// inherits the handler-global level.
	MinLevel level.Level
	// OnDrop, when non-nil, is invoked with the count of records discarded
	// because the non-blocking ring saturated.
	OnDrop func(dropped int)
	// OnError, when non-nil, is invoked for every failed block insert seen by
	// the background drainer.
	OnError func(err error)
}
