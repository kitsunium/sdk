package logger

import (
	"context"
	"os"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	svclogger "github.com/kitsunium/sdk/internal/service/observe/logger"
)

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
//	    "github.com/kitsunium/sdk/pkg/v1/observe/logger"
//	    _ "github.com/kitsunium/sdk/pkg/v1/observe/logger/writer" // console + file
//	)
//
//	lg, err := logger.DefaultMulti("/var/log/app.log")
//
// Without that import the file/console Names do not resolve and DefaultMulti
// returns the registry's WriterUnknownName, surfaced through NewMulti.
//
// Like every Logger NewMulti returns, it owns the writers it opened and
// implements io.Closer: Close releases the console writer and the file.
func DefaultMulti(path string) (lg Logger, err error) {
	//: defer entirely to NewMulti so the rollback-on-failure + framework_version
	//: stamping pipeline applies identically; this is a named-default convenience.
	return NewMulti(
		LevelInfo,
		WriterSpec{Name: "console", Config: ConsoleConfig{}},
		WriterSpec{Name: "file", Config: FileConfig{Path: path}},
	)
}

// debug is Debug's body: decl_gen.go writes Debug, from the
// design, as one call of it.
func debug(ctx context.Context, lg Logger, msg string, attrs ...Attr) {
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
