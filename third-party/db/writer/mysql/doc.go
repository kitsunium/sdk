// Package mysql — the client seam: the ONLY file that imports the database
// driver. newClient resolves credentials once, opens a lazy database/sql handle
// over a Unix-socket DSN, and returns it plus the execBatch closure that
// delivers a coalesced batch as one multi-row INSERT. Keeping the driver import
// here confines go-sql-driver/mysql to a single file.
//
// Package mysql — range 0.3.32.* (ADR 0015 service slot 0x20).
//
// Package mysql — declares the sentinels returned by the MySQL writer's
// constructor and INSERT path. Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form (short names per the AWS-writer convention; the package
// qualifier gives context). No DSN, credential, or record value is ever echoed
// into these errors (secret gate).
//
// Package mysql registers the "mysql" writer factory (ADR 0015): a database log
// sink that batches records into multi-row INSERTs over a MySQL Unix-domain
// socket (local-protocol). Importing the package self-registers the factory (no
// init()), so writer.Open("mysql", writer.MySQLConfig{…}) resolves. It is a
// dep-light third-party integration: the go-sql-driver/mysql import is confined
// to client.go, in a module of its own (ADR 0157), so pkg/v1 consumers never
// pull a DB driver into their graph and a consumer of this writer pulls no other
// vendor.
//
// Credentials: MySQLConfig.Credentials is REQUIRED and supplied programmatically
// (CredentialProvider); like the AWS writers, this factory has no config-file
// Decoder because a live credential cannot be expressed safely in YAML.
//
// Table schema: the destination table must have columns (ts DATETIME, level
// VARCHAR, message TEXT). The table name is validated as a plain identifier and
// interpolated into the INSERT (it cannot be a bound parameter); every value is
// bound via placeholders.
//
// Package mysql — mysqlSink, the thin wrapper that adds database-handle cleanup
// to the dbsink-composed chain. dbsink.Compose returns the
// levelgate(async(dbSink)) chain whose Close flushes + joins the drainer, but it
// does not own the *sql.DB; this wrapper closes the handle after the chain drains
// so the connection pool is released exactly once.
package mysql
