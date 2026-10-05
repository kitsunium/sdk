// Package writer — range 0.2.3.* (ADR 0012 core/observe/logger/writer block).
//
// Package writer — ClickHouseConfig value type for the "clickhouse" writer.
//
// Package writer — CloudWatchConfig value type for the "cloudwatch" writer.
//
// Package writer — ConsoleConfig value type for the "console" writer.
//
// Package writer — declares the optional config Decoder extension that a
// Factory MAY implement to translate a raw, parsed config map into its typed
// Config. It mirrors codec's Appender optional-extension convention: consumers
// detect support with a runtime type assertion and fall back to a default
// mapping that passes the raw map straight through when the extension is absent.
//
// A Factory that parses credentials or other sensitive material out of the raw
// map MUST NOT echo any option value into an error it returns; callers that
// surface a Decoder failure name only the writer and the failure kind.
//
// Package writer — FileConfig value type for the "file" writer.
//
// Package writer — MySQLConfig value type for the "mysql" writer.
//
// Package writer — RedisStreamConfig value type for the "redis" writer.
//
// Package writer — RotFileConfig value type for the "rotfile" writer.
//
// Package writer — S3Config value type for the "s3" writer.
//
// Package writer — credential value + provider port shared by the network
// writers (s3, cloudwatch). Carries no AWS types; the redacting CredentialValue
// keeps secrets out of any accidental log emission (rule 4 Public/Private).
//
// Package writer — declares the sentinels returned by the registry and shared
// across factories. Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form. DuplicateRegistration is never returned: Register
// panics at boot with conflictText of it. CodeWriterNil and CodeWriterNameEmpty
// label boot panics too, spelled in the message (see registry.go).
//
// Package writer — holds the process-wide Factory registry. Service- and
// public-level writer packages register themselves via package-level var
// initialisers when imported (no init()), mirroring core/data/codec.
//
// Package writer declares the transport-factory port: a named, config-driven
// constructor that yields a logger Sink, plus the process-wide registry that
// maps a writer Name to its Factory. It is the peer of internal/core/data/codec —
// the registry resolves a Name to a Factory exactly as codec resolves a Format
// to a Codec (ADR 0012).
//
// A writer is NOT a transport: it builds one. Factory.Open is called once at
// logger-construction time and returns a core/observe/logger.Sink that owns its
// transport for its lifetime; the hot path (Sink.Write) is untouched by this
// package. Concrete factories live in internal/service/observe/logger/writer/<x>/ (console,
// file) and third-party/aws/writer/<x>/ (s3, cloudwatch) and self-register via a
// package-level var initialiser when imported — no init().
//
// Package writer — Spec value type, in its own file per the
// one-exported-struct-per-file convention.
package writer
