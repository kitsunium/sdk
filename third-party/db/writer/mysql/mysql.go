package mysql

import (
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/writer"
	"github.com/kitsunium/sdk/internal/service/observe/logger/writer/dbsink"
)

// writerName is the canonical registry key.
const writerName = "mysql"

// Writer is the registered mysql factory singleton. The blank assignment runs
// writer.Register at package load (no init()), mirroring the codec convention.
var Writer = writer.Register(&mysqlFactory{})

// mysqlFactory builds a MySQL Sink from a writer.MySQLConfig.
type mysqlFactory struct{}

// Name reports the canonical key "mysql".
func (*mysqlFactory) Name() writer.Name {
	//: the literal key consumers pass in a writer spec.
	return writerName
}

// Open validates cfg, builds the database client, and composes the batching
// chain. A wrong config type yields the shared WriterConfigInvalid; a missing
// credential / invalid table / bad DSN surfaces the mysql ClientInitFailed.
func (*mysqlFactory) Open(cfg writer.Config) (sink corelogger.Sink, err error) {
	//: reject a mismatched config type with the shared sentinel.
	c, ok := cfg.(writer.MySQLConfig)
	//: the type assertion guards the rest of the construction.
	if !ok {
		//: surface the documented config-type-mismatch sentinel.
		return nil, writer.WriterConfigInvalid
	}
	//: build the lazy sql handle + the INSERT deliver closure.
	db, exec, cerr := newClient(c)
	//: forward a construction failure (origin wins; never echoes the DSN).
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
	//: wrap so Close also releases the connection pool after the chain drains.
	return newMySQLSink(composed, db), nil
}
