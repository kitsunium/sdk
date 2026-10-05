// Package clickhouse registers the "clickhouse" writer factory (ADR 0015): a
// database log sink that batches records into multi-row INSERTs over the
// ClickHouse native protocol. Importing the package self-registers the factory
// (no init()), so writer.Open("clickhouse", writer.ClickHouseConfig{…}) resolves.
// It is a dep-light third-party integration: the clickhouse-go/v2 import is
// confined to client.go, in a module of its own (ADR 0157), so pkg/v1 consumers
// never pull the driver into their graph and a consumer of this writer pulls no
// other vendor.
//
// Credentials: ClickHouseConfig.Credentials is OPTIONAL (empty falls back to the
// default user) and supplied programmatically; like the AWS writers, there is no
// config-file Decoder because a live credential cannot be expressed in YAML.
//
// Table schema: the destination table must have columns (ts DateTime, level
// String, message String). The table name is validated as a plain identifier and
// interpolated into the INSERT; every value is bound via placeholders.
//
// Package clickhouse — chSink, the thin wrapper that adds database-handle
// cleanup to the dbsink-composed chain. dbsink.Compose returns the
// levelgate(async(dbSink)) chain whose Close flushes + joins the drainer, but it
// does not own the *sql.DB; this wrapper closes the handle after the chain drains.
//
// Package clickhouse — the client seam: the ONLY file that imports the database
// driver. newClient validates the config, optionally resolves credentials, and
// builds a lazy database/sql handle via clickhouse-go's OpenDB (no connection
// until the first INSERT). It returns the handle plus the execBatch closure that
// delivers a coalesced batch as one multi-row INSERT, confining the driver here.
//
// Package clickhouse — range 0.3.33.* (ADR 0015 service slot 0x21).
//
// Package clickhouse — declares the sentinels returned by the ClickHouse
// writer's constructor and INSERT path. Each var's name equals its errs.Define
// Reason in SCREAMING_SNAKE form (short names per the AWS-writer convention; the
// package qualifier gives context). No DSN, credential, or record value is ever
// echoed into these errors (secret gate).
package clickhouse
