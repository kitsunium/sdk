package redis

import (
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/writer"
	"github.com/kitsunium/sdk/internal/service/observe/logger/writer/dbsink"
)

// writerName is the canonical registry key.
const writerName = "redis"

// Writer is the registered redis factory singleton. The blank assignment runs
// writer.Register at package load (no init()), mirroring the codec convention.
var Writer = writer.Register(&redisFactory{})

// redisFactory builds a Redis Stream Sink from a writer.RedisStreamConfig.
type redisFactory struct{}

// Name reports the canonical key "redis".
func (*redisFactory) Name() writer.Name {
	//: the literal key consumers pass in a writer spec.
	return writerName
}

// Open validates cfg, builds the client, and composes the batching chain. A
// wrong config type yields the shared WriterConfigInvalid; a missing
// socket/stream or unresolvable credential surfaces the redis ClientInitFailed.
func (*redisFactory) Open(cfg writer.Config) (sink corelogger.Sink, err error) {
	//: reject a mismatched config type with the shared sentinel.
	c, ok := cfg.(writer.RedisStreamConfig)
	//: the type assertion guards the rest of the construction.
	if !ok {
		//: surface the documented config-type-mismatch sentinel.
		return nil, writer.WriterConfigInvalid
	}
	//: build the lazy client + the XADD deliver closure.
	exec, closeFn, cerr := newClient(c)
	//: forward a construction failure (origin wins; never echoes the socket).
	if cerr != nil {
		//: ClientInitFailed already carries the right code/reason.
		return nil, cerr
	}
	//: compose the driver-agnostic batching chain over the deliver closure.
	composed := dbsink.Compose(exec, dbsink.Config{
		MaxRows:    c.MaxRows,
		FlushEvery: c.FlushEvery,
		MinLevel:   c.MinLevel,
		BufferSize: c.BufferSize,
		OnDrop:     c.OnDrop,
		OnError:    c.OnError,
	})
	//: wrap so Close also releases the client after the chain drains.
	return newRedisSink(composed, closeFn), nil
}
