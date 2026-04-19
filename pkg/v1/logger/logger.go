// Package logger is the stable v1 public API for SDK logging.
// Consumers import this package — internal/* paths are compile-blocked
// outside the SDK repo. Type signatures exported here are frozen after the
// first v1.0.0 release; breaking changes land in pkg/v2. Security fixes in
// internal/* propagate via a minor bump without touching this façade.
package logger

import (
	"context"
	"io"
	"os"

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
//
// Params:
//   - cfg: construction parameters; Writer is mandatory.
//
// Returns:
//   - Logger: the version-decorated logger, or nil on error.
//   - error: WriterRequired when cfg.Writer is nil; forwarded service errors otherwise.
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
	return base.With(corelogger.AttrValue{Key: "framework_version", Value: FrameworkVersion()}), nil
}

// Default returns a Logger writing INFO-and-above records to os.Stderr.
// The stderr Writer is supplied explicitly here; NewText itself no longer
// silently defaults a nil Writer.
//
// Returns:
//   - Logger: a stderr INFO+ logger.
//   - error: propagated from NewText (should not happen with the supplied defaults).
func Default() (lg Logger, err error) {
	//: supply os.Stderr explicitly so NewText's WriterRequired check passes.
	return NewText(Config{Writer: os.Stderr})
}

// Debug emits a RecordEvent at LevelDebug through lg.
//
// Params:
//   - ctx: request-scoped context forwarded to the Logger.
//   - lg: destination Logger.
//   - msg: human-readable message.
//   - attrs: optional structured attributes.
func Debug(ctx context.Context, lg Logger, msg string, attrs ...Attr) {
	//: delegate to the Logger contract at LevelDebug.
	lg.Log(ctx, LevelDebug, msg, attrs...)
}

// Info emits a RecordEvent at LevelInfo through lg.
//
// Params:
//   - ctx: request-scoped context forwarded to the Logger.
//   - lg: destination Logger.
//   - msg: human-readable message.
//   - attrs: optional structured attributes.
func Info(ctx context.Context, lg Logger, msg string, attrs ...Attr) {
	//: delegate to the Logger contract at LevelInfo.
	lg.Log(ctx, LevelInfo, msg, attrs...)
}

// Warn emits a RecordEvent at LevelWarn through lg.
//
// Params:
//   - ctx: request-scoped context forwarded to the Logger.
//   - lg: destination Logger.
//   - msg: human-readable message.
//   - attrs: optional structured attributes.
func Warn(ctx context.Context, lg Logger, msg string, attrs ...Attr) {
	//: delegate to the Logger contract at LevelWarn.
	lg.Log(ctx, LevelWarn, msg, attrs...)
}

// Error emits a RecordEvent at LevelError through lg.
//
// Params:
//   - ctx: request-scoped context forwarded to the Logger.
//   - lg: destination Logger.
//   - msg: human-readable message.
//   - attrs: optional structured attributes.
func Error(ctx context.Context, lg Logger, msg string, attrs ...Attr) {
	//: delegate to the Logger contract at LevelError.
	lg.Log(ctx, LevelError, msg, attrs...)
}

// String builds an Attr carrying a string value.
//
// Params:
//   - key: attribute key rendered in the output line.
//   - val: attribute string value; renders quoted so whitespace is visible.
//
// Returns:
//   - Attr: an Attr with the given key and string value.
func String(key, val string) (a Attr) {
	//: wrap into the shared AttrValue shape.
	return Attr{Key: key, Value: val}
}

// Int builds an Attr carrying an int value.
//
// Params:
//   - key: attribute key rendered in the output line.
//   - val: attribute int value; renders base-10 unquoted.
//
// Returns:
//   - Attr: an Attr with the given key and int value.
func Int(key string, val int) (a Attr) {
	//: wrap into the shared AttrValue shape.
	return Attr{Key: key, Value: val}
}
