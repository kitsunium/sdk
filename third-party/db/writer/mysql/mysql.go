// Package mysql registers the "mysql" writer factory (ADR 0015): a database log
// sink that batches records into multi-row INSERTs over a MySQL Unix-domain
// socket (local-protocol). Importing the package self-registers the factory (no
// init()), so writer.Open("mysql", writer.MySQLConfig{…}) resolves. It is a
// dep-light third-party integration: the go-sql-driver/mysql import is confined
// to client.go and lives in the ROOT module only, so pkg/v1 consumers never pull
// a DB driver into their graph.
//
// Credentials: MySQLConfig.Credentials is REQUIRED and supplied programmatically
// (CredentialProvider); like the AWS writers, this factory has no config-file
// Decoder because a live credential cannot be expressed safely in YAML.
//
// Table schema: the destination table must have columns (ts DATETIME, level
// VARCHAR, message TEXT). The table name is validated as a plain identifier and
// interpolated into the INSERT (it cannot be a bound parameter); every value is
// bound via placeholders.
package mysql

import (
	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/service/writer/dbsink"
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
