//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/logger .

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
//     BENCH.md and pinned by TestV116BuildSendAllocatesOnePerEmit.
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
//	| Sinks (native)      | ConsoleStderr, ConsoleStdout, NewWriterSink(w), Multi(branches…)             | Native + io.Writer adapter + fan-out; bring custom Sink for DB/etc. |
//	| Middleware          | multi, async, route, failover, sample, recover                               | Compose around a base Sink; same Sink interface chainable |
//	| Encoders            | TextEncoder                                                                  | key=value lines on system clock (JSON/structured: internal today) |
//	| Emission            | Info / Warn / Error / Debug (variadic), Build(lg,lv) → chain → Send, LogAttrs| 1 alloc on variadic, 0 alloc steady-state on Build |
//	| Attr constructors   | String, Int, Bool, Float64, Int64, Uint64, Duration, Time, Any               | Typed at the call site |
//	| Levels              | LevelDebug, LevelInfo, LevelWarn, LevelError                                 | MinLevel on SinkConfig filters at the source |
//	| Versioning          | Version (ldflags), FrameworkVersion()                                        | Stamp every record with the SDK build version |
//	| Error sentinels     | WriterRequired (1.1.0.1), SinkConfigRequired (1.1.0.2)                       | Construction-time, introspectable via errs.HasCode |
//
// # Quick start (text on stderr)
//
//	import "github.com/kitsunium/sdk/pkg/v1/logger"
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
// github.com/kitsunium/sdk/pkg/v1/logger/writer to activate the two
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
// # Builder hot path
//
// [Build] returns a chainable, sync.Pool-backed [Builder] whose
// steady-state per-call cost is one heap allocation per emit: the pool
// recycles the Builder and its attrs scratchpad, but the handler clones
// that scratchpad on every [Builder.Send], so one slice escapes.
// Callers MUST NOT use a Builder after [Builder.Send] — it returns to
// the recycler.
//
//	logger.Build(lg, logger.LevelInfo).
//	    Str("user", u.Name).
//	    Int("age", u.Age).
//	    Send(ctx, "user logged in")
//
// [LogAttrs] is the slice overload that avoids the variadic-slice
// allocation in [Logger.Log].
//
// # Trace correlation
//
// Every Logger this package builds stamps the span in scope onto
// every record it emits, as the two TOP-LEVEL fields OpenTelemetry
// prescribes for non-OTLP log formats: [TraceIDKey] ("trace_id", 32
// lowercase hex digits) and [SpanIDKey] ("span_id", 16). Populating
// the context is the trace domain's job — the inbound HTTP middleware
// in github.com/kitsunium/sdk/pkg/v1/trace, or an explicit
// trace.ContextWithSpanContext:
//
//	ctx = trace.ContextWithSpanContext(ctx, spanContext)
//	logger.Info(ctx, lg, "served") // → … trace_id=4bf9… span_id=00f0…
//
// Three properties are worth knowing:
//
//   - They are record FIELDS ([Record].TraceContext), not attributes,
//     so [Logger.WithGroup] never renames them to "http.trace_id" and
//     a Sink can read the identity off the record instead of parsing
//     it back out of a formatted line.
//   - Both names are RESERVED at the top level. An attribute called
//     "trace_id" or "span_id" renders as "attr.trace_id" /
//     "attr.span_id", because two fields of one name let a decoder
//     keep the caller's value as the line's correlation. It is
//     renamed and never dropped, and a key under [Logger.WithGroup]
//     already carries its prefix and is untouched. To log somebody
//     else's identifier, name it for what it is:
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
//   - Raw go build: `go build -ldflags "-X github.com/kitsunium/sdk/pkg/v1/logger.Version=v0.1.0" ./...`
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
package logger

import (
	"context"
	"io"
	"os"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	svclogger "github.com/kitsunium/sdk/internal/service/logger"
)

// Logger is the stable alias for the internal core.Logger interface.
type Logger = corelogger.Logger

// Attr is the stable alias for the internal AttrValue key/value pair.
type Attr = corelogger.AttrValue

// Level is the stable alias for the internal severity type.
type Level = level.Level

// LevelDebug selects records describing detailed tracing information.
const LevelDebug Level = level.Debug

// LevelInfo selects records describing routine operational events.
const LevelInfo Level = level.Info

// LevelWarn selects records describing abnormal but recoverable conditions.
const LevelWarn Level = level.Warn

// LevelError selects records describing failures needing action.
const LevelError Level = level.Error

// Config carries the construction parameters accepted by NewText.
// Two destination forms are supported — pick the one that fits:
//
//   - Writer single — Writer: w. Records go to that one writer.
//   - Writers fan-out — Writers: []io.Writer{a, b, c}. Records broadcast
//     to every writer in order; per-branch failures are joined under
//     FANOUT_WRITE_FAILED. Use this when you want stderr AND a log file
//     in one go (a common ops pattern) without dropping to NewWithSink.
//
// When BOTH are set, Writers wins and Writer is ignored. Use whichever
// reads more naturally at the call site. Construction returns
// WriterRequired (1.1.0.1) when neither field carries any writer.
type Config struct {
	// Writer is the single-destination convenience field; nil triggers
	// WriterRequired unless Writers carries at least one entry.
	Writer io.Writer
	// Writers is the multi-destination fan-out field; non-empty wires
	// the records through logger.Multi over per-writer console sinks.
	// Nil/empty falls back to Writer.
	Writers []io.Writer
	// MinLevel is the minimum severity emitted; zero value is LevelInfo.
	MinLevel Level
}

// NewText builds a text-format Logger writing to cfg.Writer (single) OR
// cfg.Writers (fan-out) filtered at cfg.MinLevel. NewText intentionally
// does NOT default a nil destination: callers that want stderr use
// Default(), which supplies it explicitly. Every record emitted through
// the returned Logger carries a "framework_version" attr.
//
// Multi-writer example — stream identical text records to stderr AND a
// log file:
//
//	f, _ := os.OpenFile("/var/log/myapp.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
//	lg, err := logger.NewText(logger.Config{
//	    Writers:  []io.Writer{os.Stderr, f},
//	    MinLevel: logger.LevelInfo,
//	})
func NewText(cfg Config) (lg Logger, err error) {
	//: prefer the Writers fan-out path when the slice is non-empty —
	//: it covers strictly more cases than the single-writer path.
	if len(cfg.Writers) > 0 {
		//: pre-allocate the branch slice with exact cardinality.
		branches := make([]Sink, 0, len(cfg.Writers))
		//: wrap each plain io.Writer into a Sink so Multi can fold them.
		for _, w := range cfg.Writers {
			//: NewWriterSink rejects nil with WriterRequired so a typo
			//: in the Writers slice surfaces the right typed error.
			sink, sErr := NewWriterSink(w)
			//: forward the construction error untouched (origin wins).
			if sErr != nil {
				//: bubble out so the caller sees the first failing writer.
				return nil, sErr
			}
			branches = append(branches, sink)
		}
		//: route through NewWithSink so the same encoder + version
		//: stamping pipeline applies as the single-writer path.
		return NewWithSink(SinkConfig{
			Sink:     Multi(branches...),
			Encoder:  TextEncoder(),
			MinLevel: cfg.MinLevel,
		})
	}
	//: refuse to default silently — explicit construction prevents silent stderr.
	if cfg.Writer == nil {
		//: surface the documented sentinel so operators can HasCode(err, 1.1.0.1).
		return nil, WriterRequired
	}
	//: build the handler; svclogger owns its own validation.
	handler, hErr := svclogger.NewTextHandler(cfg.Writer, cfg.MinLevel)
	//: forward any svclogger-level error unchanged (origin wins).
	if hErr != nil {
		//: service-layer rejection already carries the right code/reason.
		return nil, hErr
	}
	//: wrap the handler into a Logger via svclogger, bound to the trace domain
	//: so every record emitted inside a span carries its ids (ADR 0062).
	base, lErr := svclogger.NewWithTraceContext(handler, TraceContextFromContext)
	//: forward any svclogger-level error unchanged (origin wins).
	if lErr != nil {
		//: service-layer rejection already carries the right code/reason.
		return nil, lErr
	}
	//: decorate every emitted record with the SDK version for observability.
	return base.With(corelogger.AttrValue{Key: "framework_version", Value: corelogger.StringValue(FrameworkVersion())}), nil
}

// Default returns a Logger writing INFO-and-above records to os.Stderr.
// The stderr Writer is supplied explicitly here; NewText itself no longer
// silently defaults a nil Writer.
func Default() (lg Logger, err error) {
	//: supply os.Stderr explicitly so NewText's WriterRequired check passes.
	return NewText(Config{Writer: os.Stderr})
}

// DefaultMulti returns a Logger that fans INFO-and-above records out to BOTH
// the console (stderr, the ConsoleConfig zero value — ADR 0030) AND a plain
// file at path.
// It is the batteries-included "perfect default" the SDK recommends for a
// service: two destinations from one call, with NO rotation surface — the
// plain "file" writer never rotates, so rotation stays opt-in (a caller wanting
// size/age rotation reaches for the rotating writer explicitly).
//
// Unlike Default (stderr-only, no extra import), DefaultMulti resolves the
// "console" and "file" writers through the registry, so the CALLER MUST
// blank-import the writer package to activate them:
//
//	import (
//	    "github.com/kitsunium/sdk/pkg/v1/logger"
//	    _ "github.com/kitsunium/sdk/pkg/v1/logger/writer" // console + file
//	)
//
//	lg, err := logger.DefaultMulti("/var/log/app.log")
//
// Without that import the file/console Names do not resolve and DefaultMulti
// returns the registry's WriterUnknownName, surfaced through NewMulti.
func DefaultMulti(path string) (lg Logger, err error) {
	//: defer entirely to NewMulti so the rollback-on-failure + framework_version
	//: stamping pipeline applies identically; this is a named-default convenience.
	return NewMulti(
		LevelInfo,
		WriterSpec{Name: "console", Config: ConsoleConfig{}},
		WriterSpec{Name: "file", Config: FileConfig{Path: path}},
	)
}

// Debug emits a RecordEvent at LevelDebug through lg.
func Debug(ctx context.Context, lg Logger, msg string, attrs ...Attr) {
	//: delegate to the Logger contract at LevelDebug.
	lg.Log(ctx, LevelDebug, msg, attrs...)
}

// Info emits a RecordEvent at LevelInfo through lg.
func Info(ctx context.Context, lg Logger, msg string, attrs ...Attr) {
	//: delegate to the Logger contract at LevelInfo.
	lg.Log(ctx, LevelInfo, msg, attrs...)
}

// Warn emits a RecordEvent at LevelWarn through lg.
func Warn(ctx context.Context, lg Logger, msg string, attrs ...Attr) {
	//: delegate to the Logger contract at LevelWarn.
	lg.Log(ctx, LevelWarn, msg, attrs...)
}

// Error emits a RecordEvent at LevelError through lg.
func Error(ctx context.Context, lg Logger, msg string, attrs ...Attr) {
	//: delegate to the Logger contract at LevelError.
	lg.Log(ctx, LevelError, msg, attrs...)
}

// String builds an Attr carrying a string value.
func String(key, val string) Attr {
	//: wrap into the shared AttrValue shape via the typed constructor.
	return Attr{Key: key, Value: corelogger.StringValue(val)}
}

// Int builds an Attr carrying an int value.
func Int(key string, val int) Attr {
	//: wrap into the shared AttrValue shape via the typed constructor.
	return Attr{Key: key, Value: corelogger.IntValue(val)}
}

// Bool builds an Attr carrying a boolean value.
func Bool(key string, val bool) Attr {
	//: wrap into the shared AttrValue shape via the typed constructor.
	return Attr{Key: key, Value: corelogger.BoolValue(val)}
}

// Float64 builds an Attr carrying a float64 value.
func Float64(key string, val float64) Attr {
	//: wrap into the shared AttrValue shape via the typed constructor.
	return Attr{Key: key, Value: corelogger.Float64Value(val)}
}

// Int64 builds an Attr carrying an int64 value.
func Int64(key string, val int64) Attr {
	//: wrap into the shared AttrValue shape via the typed constructor.
	return Attr{Key: key, Value: corelogger.Int64Value(val)}
}

// Uint64 builds an Attr carrying a uint64 value.
func Uint64(key string, val uint64) Attr {
	//: wrap into the shared AttrValue shape via the typed constructor.
	return Attr{Key: key, Value: corelogger.Uint64Value(val)}
}

// Duration builds an Attr carrying a time.Duration value.
func Duration(key string, val time.Duration) Attr {
	//: wrap into the shared AttrValue shape via the typed constructor.
	return Attr{Key: key, Value: corelogger.DurationValue(val)}
}

// Time builds an Attr carrying a time.Time value.
func Time(key string, val time.Time) Attr {
	//: wrap into the shared AttrValue shape via the typed constructor.
	return Attr{Key: key, Value: corelogger.TimeValue(val)}
}

// Any builds an Attr carrying an opaque payload. Use the typed helpers when
// possible — Any disables type-aware rendering.
func Any(key string, val any) Attr {
	//: wrap into the shared AttrValue shape via the typed constructor.
	return Attr{Key: key, Value: corelogger.AnyValue(val)}
}
