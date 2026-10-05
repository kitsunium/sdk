// Package logger — re-exports the chainable Builder API and the
// slice-overload LogAttrs entry point. Both are routed through the
// internal service implementation, which owns the recycler that holds
// the steady-state hot-path at one allocation per emit.
//
// Package logger — adds the opt-in caller annotation surface. WithCaller
// derives a Logger whose records carry a structured "source" attribute
// (file:line:function) resolved from the program counter the front-end already
// captures. It is additive: an unwrapped Logger emits no source field, so the
// frozen record shape is unchanged until a caller opts in.
//
// Package logger — range 1.1.0.* (ADR 0005 pkg/v1/observe/logger block).
//
// Package logger — adds ergonomic Encoder constructors to the public facade.
// The Encoder type alias itself lives in sink.go; this file contributes the
// named constructors (NewTextEncoder / NewJSONEncoder) so consumers can build
// an encoder directly and pass it to NewWithSink without importing internal/*.
//
// Package logger — declares pkg/v1/observe/logger's sentinels. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// Package logger — exposes FromConfig, the capstone of the config-driven writer
// subsystem (ADR 0014 §D5): it builds a fully wired Logger from a config blob
// with zero Go glue. The blob is decoded by a codec the CONSUMER already
// registered (FromConfig imports only the core/data/codec dispatch surface, never
// pkg/v1/data/codec or any service codec, so a pkg/v1/observe/logger consumer inherits no
// vendor modules). Each decoded WriterEntry is resolved against the writer
// registry; a Factory that implements ConfigDecoder translates its own option
// map, otherwise a default mapping passes the raw map straight to the factory.
//
// Package logger — re-exports the Value payload discriminant so consumers can
// name what Value.Kind() returns.
//
// Without these aliases MemorySink is only half usable: it hands back
// RecordSnapshot values whose Attrs carry a Kind, but the Kind type itself
// lived behind the internal/ firewall, so a consumer test could read the
// discriminant and still not write it down. The core doc already calls these
// values "stable across the public API"; this file makes that true.
//
// Package logger — exposes the runtime-tunable level surface: ParseLevel (the
// strict inverse of a lowercased Level.String), the Leveler one-method port, and
// LevelVar, an atomically mutable threshold holder a custom sink or gate consults
// on each record to retune a live logger's floor without rebuilding the pipeline.
//
// Package logger is the stable v1 public API for SDK logging.
//
// Consumers import this package; internal/* paths are compile-blocked
// outside the SDK repo. Type signatures exported here are frozen after
// the first v1.0.0 release; breaking changes land in pkg/v2. Security
// fixes in internal/* propagate via a minor bump without touching this
// façade.
//
// # Goals
//
//   - One-allocation hot path. The chainable Build(lg, lv).Str(...).
//     Send(...) builder is backed by a sync.Pool that recycles the
//     builder and its attrs scratchpad, but the handler clones that
//     scratchpad on every Send — so steady-state cost is exactly 1
//     heap allocation per emit, not 0. The variadic Info/Warn/... path
//     costs the same 1 (its variadic slice). Prefer Build for
//     ergonomics; it is not an allocation-free guarantee. Measured in
//     BENCH.md and pinned by its design contract (allocsMin: 1 on
//     Build, ADR 0165).
//   - Composable transport. A Logger is wired from a Sink (where bytes
//     go) + an Encoder (how bytes are formatted). Fan-out, async, route,
//     failover, sample, recover middleware compose around a Sink —
//     bring your own topology.
//   - Typed Attrs at the call site. 9 typed constructors (String, Int,
//     Bool, Float64, Int64, Uint64, Duration, Time, Any) — no
//     any-untyped key/value pairs that error at runtime.
//   - Build-time version stamping. Version is the single ldflags
//     injection point. FrameworkVersion() is added as
//     "framework_version" to every emitted record — set under
//     go build -ldflags "-X .../logger.Version=…" or Bazel --stamp.
//   - Trace correlation, on by default and free. A record emitted
//     inside a span carries that span's trace_id and span_id as
//     top-level fields; a record emitted outside one carries neither
//     key. No call site changes — Logger.Log has always taken a
//     context.Context. Costs no extra allocation. See ADR 0062 and
//     the "Trace correlation" section below.
//   - Two failure modes only. Construction returns WriterRequired
//     (1.1.0.1) on a nil writer or SinkConfigRequired (1.1.0.2) on a
//     nil sink — both introspectable via errs.HasCode.
//
// # What's shipped
//
// Three construction paths, three native sinks, six middleware kinds,
// nine Attr ctors, four Level constants, three emission paths.
//
//	| Group               | Symbols                                                                     | Role |
//	|---------------------|------------------------------------------------------------------------------|------|
//	| Construction        | Default, DefaultMulti(path), NewText(Config), NewWithSink(SinkConfig)        | Wire a Logger from explicit knobs OR a one-liner. Config has Writer (single) + Writers ([]io.Writer fan-out) — pick one. DefaultMulti fans console+file from one call (blank-import the writer pkg). |
//	| Sinks (native)      | ConsoleStderr, ConsoleStdout, NewWriterSink(w), Multi(branches…), LevelGate  | Native + io.Writer adapter + fan-out + a per-branch floor; bring custom Sink for DB/etc. |
//	| Middleware          | multi, async, route, failover, sample, recover                               | Compose around a base Sink; same Sink interface chainable |
//	| Encoders            | TextEncoder                                                                  | key=value lines on system clock (JSON/structured: internal today) |
//	| Emission            | Info / Warn / Error / Debug (variadic), Build(lg,lv) → chain → Send, LogAttrs| 1 alloc on variadic, 1 alloc steady-state on Build |
//	| Attr constructors   | String, Int, Bool, Float64, Int64, Uint64, Duration, Time, Any               | Typed at the call site |
//	| Levels              | LevelDebug, LevelInfo, LevelWarn, LevelError                                 | MinLevel on SinkConfig filters at the source |
//	| Versioning          | Version (ldflags), FrameworkVersion()                                        | Stamp every record with the SDK build version |
//	| Error sentinels     | WriterRequired (1.1.0.1), SinkConfigRequired (1.1.0.2)                       | Construction-time, introspectable via errs.HasCode |
//
// # Quick start (text on stderr)
//
//	import "github.com/kitsunium/sdk/pkg/v1/observe/logger"
//
//	func main() {
//	    lg, err := logger.NewText(logger.Config{Writer: os.Stderr, Level: logger.LevelInfo})
//	    if err != nil { panic(err) }
//	    lg.Info(context.Background(), "service ready",
//	        logger.String("addr", ":8080"))
//	}
//
// [Default] returns the stderr one-liner equivalent in tests + scripts.
// [DefaultMulti] is the batteries-included service default: it fans
// INFO-and-above records to BOTH the console AND a plain (non-rotating)
// file from a single call — the caller blank-imports
// github.com/kitsunium/sdk/pkg/v1/observe/logger/writer to activate the two
// named writers. Pass [Config]{Writer: nil} and [NewText] returns the
// typed sentinel [WriterRequired] — the pre-errors API silently
// defaulted to os.Stderr; that was a breaking change recorded in
// ADR 0002.
//
// # Custom sink topology
//
// For multi-sink fan-out, async / failover / sample / route middleware,
// drive [NewWithSink] with a [SinkConfig]:
//
//	sink := logger.Multi(
//	    logger.ConsoleStderr(logger.TextEncoder),
//	    fileSink, // *yourSink implementing logger.Sink
//	)
//	lg, err := logger.NewWithSink(logger.SinkConfig{Sink: sink, Level: logger.LevelDebug})
//
// A nil Sink returns [SinkConfigRequired]. Both [NewText] and
// [NewWithSink] decorate every emitted record with the
// "framework_version" attribute (see [FrameworkVersion]).
//
// # One logger, two floors
//
// [SinkConfig].MinLevel is the floor of the whole pipeline. When one
// branch of a fan-out should see less than another — the terminal at
// the application's level, an in-process viewer at every level — set
// the pipeline's floor to the lowest and gate the narrower branch with
// [LevelGate]:
//
//	sink := logger.Multi(
//	    logger.LevelGate(logger.ConsoleStderr(), logger.LevelInfo), // Info and above
//	    viewer, // everything, Debug included
//	)
//	lg, err := logger.NewWithSink(logger.SinkConfig{Sink: sink, MinLevel: logger.LevelDebug})
//
// The gate applies the floor it is given, Info included; a dropped
// record is a successful no-op, so it never surfaces as a fan-out
// failure.
//
// # Builder hot path
//
// [Build] returns a chainable, sync.Pool-backed [Builder] whose
// steady-state per-call cost is one heap allocation per emit: the pool
// recycles the Builder and its attrs scratchpad, but the handler clones
// that scratchpad on every [Builder].Send, so one slice escapes.
// Callers MUST NOT use a Builder after [Builder].Send — it returns to
// the recycler.
//
//	logger.Build(lg, logger.LevelInfo).
//	    Str("user", u.Name).
//	    Int("age", u.Age).
//	    Send(ctx, "user logged in")
//
// [LogAttrs] is the slice overload that avoids the variadic-slice
// allocation in [Logger].Log.
//
// # Trace correlation
//
// Every Logger this package builds stamps the span in scope onto
// every record it emits, as the two TOP-LEVEL fields OpenTelemetry
// prescribes for non-OTLP log formats: "trace_id" (32 lowercase hex
// digits) and "span_id" (16). Populating
// the context is the trace domain's job — the inbound HTTP middleware
// in github.com/kitsunium/sdk/pkg/v1/observe/trace, or an explicit
// trace.ContextWithSpanContext:
//
//	ctx = trace.ContextWithSpanContext(ctx, spanContext)
//	logger.Info(ctx, lg, "served") // → … trace_id=4bf9… span_id=00f0…
//
// Three properties are worth knowing:
//
//   - They are record FIELDS ([Record].TraceContext), not attributes,
//     so [Logger].WithGroup never renames them to "http.trace_id" and
//     a Sink can read the identity off the record instead of parsing
//     it back out of a formatted line.
//   - Both names are RESERVED at the top level. An attribute called
//     "trace_id" or "span_id" renders as "attr.trace_id" /
//     "attr.span_id", because two fields of one name let a decoder
//     keep the caller's value as the line's correlation. It is
//     renamed and never dropped, and a key under [Logger].WithGroup
//     already carries its prefix and is untouched. The whole "attr."
//     namespace is reserved with them, so a key already inside it is
//     prefixed again — otherwise the rename would not be one-to-one
//     and two of your own keys could collide. To log somebody else's
//     identifier, name it for what it is:
//     logger.String("upstream_trace_id", id).
//   - When no span is in scope NOTHING is emitted — not an empty
//     value and not the all-zero identifier, both of which W3C Trace
//     Context declares invalid. Most lines a service logs are outside
//     a request, and an unjoinable trace_id on all of them would make
//     the field useless as a filter.
//   - It costs no extra allocation: the hot path stays at exactly one
//     heap allocation per emit, with a span and without. The
//     identifiers are hex-encoded straight into the encoder's buffer
//     and never become a Go string. Measured in BENCH.md.
//
// [TraceContextFromContext] is the adapter, exported so a caller
// wiring a Logger by hand can reuse it.
//
// # Version stamping
//
// [Version] is the single ldflags injection point for the SDK.
// Recipes:
//
//   - Raw go build: `go build -ldflags "-X github.com/kitsunium/sdk/pkg/v1/observe/logger.Version=v0.1.0" ./...`
//   - Bazel: --stamp + x_defs + tools/workspace_status.sh (STABLE_VERSION).
//
// [FrameworkVersion] returns the link-time value or the literal "dev"
// sentinel when unset. Every emitted record carries this value under
// the "framework_version" attribute key.
//
// # Errors
//
// Construction failures carry typed dotted-quad codes under range
// 1.1.0.* per ADR 0005:
//
//   - 1.1.0.1 [CodeWriterRequired] / [WriterRequired]: NewText with Writer == nil.
//   - 1.1.0.2 [CodeSinkConfigRequired] / [SinkConfigRequired]: NewWithSink with Sink == nil.
//
// Inspect via the accessors in github.com/kitsunium/sdk/pkg/v1/errs.
//
// Package logger — exposes the in-memory test sink (NewMemorySink) and its
// RecordSnapshot element type so consumers can assert on what was logged.
//
// Package logger — exposes the Sink port, the multi-sink helper and the
// per-branch level gate alongside the encoder-aware constructor NewWithSink.
// Together they let consumers replace the default text-on-stderr wiring
// (NewText / Default) with arbitrary fan-out / async / file / syslog
// topologies — without reaching into internal/* packages.
//
// Package logger — declares the TopologyConfig DTO consumed by FromConfig.
// A TopologyConfig is the decoded shape of a logger config file: a global level
// plus an ordered list of named writer entries. It is a plain data carrier with
// no behaviour — the construction logic lives in FromConfig.
//
// Package logger — bridges the trace domain to the logging domain, so a log
// line emitted inside a span carries that span's identity and an operator
// holding a trace_id can find the logs that belong to it.
//
// This file is the ONLY place in the SDK where logging and tracing meet, and
// that placement is the design (ADR 0062). internal/service/observe/logger and
// internal/service/observe/trace are siblings, and neither imports the other because
// the two domains meet only here, at the top layer; internal/core/observe/logger is
// kept stdlib-only so a consumer who only wants a line on stderr does not
// compile the trace model. pkg/v1 is the layer that knows both domains
// already, so the binding lives here and nowhere else.
//
// Package logger — exposes the SDK version to the rest of the
// logger package. The ldflags pipeline injects the real value at build
// time; local development runs fall back to the "dev" sentinel.
//
// Package logger — WithError decomposes an SDK typed error into structured log
// Attrs (error.code / error.reason / error.public + wrap-trail codes), making
// the SDK's dotted-quad errors first-class structured data rather than a flat
// string. It lives beside the Attr constructors it produces.
//
// Package logger — exposes the named, config-driven writer surface (ADR 0012):
// the WriterName / *Config aliases, the WriterSpec pair, and NewMulti, which
// resolves each named writer to a Sink and fans records out to all of them via
// Multi. Built-in console + file writers activate with a blank import of
// pkg/v1/observe/logger/writer; s3 / cloudwatch activate with a blank import of the
// matching third-party/aws/writer package (which alone pulls the AWS SDK).
//
// Package logger — declares the WriterEntryConfig DTO consumed by FromConfig.
// A WriterEntryConfig names a registered writer and carries its raw, codec-
// decoded option map; FromConfig hands that map to the writer's Decoder (or a
// default mapping) to obtain a typed writer.Config.
package logger
