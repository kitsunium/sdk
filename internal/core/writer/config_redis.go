// Package writer — RedisStreamConfig value type for the "redis" writer.
package writer

import (
	"time"

	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// RedisStreamConfig configures the "redis" writer (registered by
// third-party/db/writer/redis). It carries no driver types — only plain data —
// so it lives in the dep-light core layer; the concrete driver (redis/go-redis)
// is confined to the third-party adapter. A batch is delivered as a pipelined
// run of XADD commands, so the sink batches by entry count.
//
// Local-protocol by design: SocketPath points at a Unix domain socket, keeping
// log traffic off TCP.
type RedisStreamConfig struct {
	// SocketPath is the Redis Unix domain socket (e.g. /var/run/redis/redis.sock);
	// required — the adapter dials net=unix from it.
	SocketPath string
	// Stream is the Redis stream key entries are XADDed to (required).
	Stream string
	// MaxLen, when positive, caps the stream length via XADD MAXLEN ~ (the
	// approximate trim that lets Redis evict in whole macro-nodes). Zero leaves
	// the stream untrimmed (the operator manages retention).
	MaxLen int
	// Credentials yields the DB login on demand. A nil provider means no AUTH
	// (a socket-local Redis with no password); when set, AccessKeyID() is the
	// ACL username (empty for the default user) and SecretAccessKey() is the
	// password (reused redacting CredentialValue keeps it out of %v/%#v).
	Credentials CredentialProvider
	// MaxRows forces a flush once the pending batch reaches this many entries; a
	// non-positive value applies the dbsink shell's default.
	MaxRows int
	// FlushEvery bounds how long a partial batch waits before the pipelined
	// XADD run; the zero value flushes only on an explicit Flush / Close.
	FlushEvery time.Duration
	// BufferSize is the async ring capacity; a non-positive value applies
	// async's default. The producer never blocks on Redis.
	BufferSize int
	// MinLevel is the optional per-writer severity floor; the zero value
	// inherits the handler-global level.
	MinLevel level.Level
	// OnDrop, when non-nil, is invoked with the count of records discarded
	// because the non-blocking ring saturated.
	OnDrop func(dropped int)
	// OnError, when non-nil, is invoked for every failed XADD batch seen by the
	// background drainer.
	OnError func(err error)
}
