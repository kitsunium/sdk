// Package writer — MySQLConfig value type for the "mysql" writer.
package writer

import (
	"time"

	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// MySQLConfig configures the "mysql" writer (registered by
// third-party/db/writer/mysql). It carries no driver types — only plain data —
// so it lives in the dep-light core layer; the concrete driver
// (go-sql-driver/mysql) is confined to the third-party adapter. A batch is
// delivered as a single multi-row INSERT, so the sink batches by row count.
//
// Local-protocol by design: SocketPath points at a Unix domain socket
// (net=unix in the DSN the adapter builds), keeping log traffic off TCP.
type MySQLConfig struct {
	// SocketPath is the MySQL Unix domain socket (e.g.
	// /var/run/mysqld/mysqld.sock); required — the adapter builds a net=unix DSN
	// from it. TCP is intentionally not exposed here (local-protocol policy).
	SocketPath string
	// Database is the target schema (required).
	Database string
	// Table is the destination table for log rows (required).
	Table string
	// Credentials yields the DB login on demand and is REQUIRED: a nil provider
	// is rejected with the adapter's ClientInitFailed. The redacting
	// CredentialValue is reused as-is — AccessKeyID() is the MySQL username and
	// SecretAccessKey() is the password (SessionToken is unused). Reusing the
	// existing redacting type avoids a second secret abstraction; the password
	// never appears in %v/%#v or an error.
	Credentials CredentialProvider
	// MaxRows forces a flush once the pending batch reaches this many rows; a
	// non-positive value applies the dbsink shell's default.
	MaxRows int
	// FlushEvery bounds how long a partial batch waits before the INSERT; the
	// zero value flushes only on an explicit Flush / Close.
	FlushEvery time.Duration
	// BufferSize is the async ring capacity; a non-positive value applies
	// async's default. The producer never blocks on the database.
	BufferSize int
	// MinLevel is the optional per-writer severity floor; the zero value
	// inherits the handler-global level.
	MinLevel level.Level
	// OnDrop, when non-nil, is invoked with the count of records discarded
	// because the non-blocking ring saturated (database slower than producers).
	OnDrop func(dropped int)
	// OnError, when non-nil, is invoked for every failed batch INSERT seen by
	// the background drainer. Without it those errors are lost.
	OnError func(err error)
}
