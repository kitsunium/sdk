//go:generate gomarkdoc --output README.md .

// Package logger is the stable v1 public API for SDK logging.
//
// Consumers import this package; internal/* paths are compile-blocked
// outside the SDK repo. Type signatures exported here are frozen after
// the first v1.0.0 release; breaking changes land in pkg/v2. Security
// fixes in internal/* propagate via a minor bump without touching this
// façade.
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
// Pass [Config]{Writer: nil} and [NewText] returns the typed sentinel
// [WriterRequired] — the pre-errors API silently defaulted to
// os.Stderr; that was a breaking change recorded in ADR 0002.
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
// steady-state per-call cost is zero heap allocations after the pool
// warms. Callers MUST NOT use a Builder after [Builder.Send] — it
// returns to the recycler.
//
//	logger.Build(lg, logger.LevelInfo).
//	    Str("user", u.Name).
//	    Int("age", u.Age).
//	    Send(ctx, "user logged in")
//
// [LogAttrs] is the slice overload that avoids the variadic-slice
// allocation in [Logger.Log].
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
// Fields that remain at their zero value fall back to documented defaults,
// so Config{} is a valid argument producing a stderr INFO+ logger.
type Config struct {
	// Writer is the destination sink; nil defaults to os.Stderr.
	Writer io.Writer
	// MinLevel is the minimum severity emitted; zero value is LevelInfo.
	MinLevel Level
}

// NewText builds a text-format Logger writing to cfg.Writer filtered at
// cfg.MinLevel. NewText intentionally does NOT default a nil Writer: callers
// that want stderr use Default(), which supplies it explicitly. Every record
// emitted through the returned Logger carries a "framework_version" attr.
func NewText(cfg Config) (lg Logger, err error) {
	//: refuse to default silently — explicit construction prevents silent stderr.
	if cfg.Writer == nil {
		//: surface the documented sentinel so operators can HasCode(err, 4101).
		return nil, WriterRequired
	}
	//: build the handler; svclogger owns its own validation.
	handler, hErr := svclogger.NewTextHandler(cfg.Writer, cfg.MinLevel)
	//: forward any svclogger-level error unchanged (origin wins).
	if hErr != nil {
		//: service-layer rejection already carries the right code/reason.
		return nil, hErr
	}
	//: wrap the handler into a Logger via svclogger.
	base, lErr := svclogger.New(handler)
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
