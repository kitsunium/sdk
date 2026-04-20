// Package logger: sink.go exposes the Sink port and the multi-sink helper
// alongside the encoder-aware constructor NewWithSink. Together they let
// consumers replace the default text-on-stderr wiring (NewText / Default)
// with arbitrary fan-out / async / file / syslog topologies — without
// reaching into internal/* packages.
package logger

import (
	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	svclogger "github.com/kitsunium/sdk/internal/service/logger"
	"github.com/kitsunium/sdk/internal/service/logger/encoder"
	"github.com/kitsunium/sdk/internal/service/logger/sink/console"
	"github.com/kitsunium/sdk/internal/service/logger/sink/multi"
)

// Sink is the stable alias for the internal core.Sink port. Consumers
// compose Sink instances (console / file / syslog / async / multi …) and
// pass them to NewWithSink to wire a custom transport pipeline.
type Sink = corelogger.Sink

// Record is the stable alias for the internal RecordEvent value passed to
// Sink.Write. Consumers implementing custom Sinks reach for this type
// rather than reimporting the internal core.logger package.
type Record = corelogger.RecordEvent

// Encoder is the stable alias for the internal service encoder interface.
// The default text encoder is exposed via TextEncoder; structured codecs
// (json / ndjson) can be injected through NewWithCodec when callers need
// machine-readable output.
type Encoder = encoder.Encoder

// SinkConfig carries the construction parameters accepted by NewWithSink.
// A zero-valued SinkConfig{Sink: s} is enough to ship records through s at
// LevelInfo using the default text encoder bound to the real wall clock.
type SinkConfig struct {
	// Sink is the destination transport; nil yields SinkConfigRequired.
	Sink Sink
	// Encoder formats records into bytes; nil falls back to the text encoder.
	Encoder Encoder
	// MinLevel is the minimum severity emitted; zero value is LevelInfo.
	MinLevel Level
}

// NewWithSink builds a Logger forwarding records through cfg.Sink and
// formatting them with cfg.Encoder. It is the port-and-adapter entry point
// for callers that want full control over both the format (Encoder) and
// the transport (Sink); use NewText for the default text-on-stderr wiring.
//
// Params:
//   - cfg: construction parameters; Sink is mandatory.
//
// Returns:
//   - Logger: the version-decorated logger, or nil on error.
//   - error: SinkConfigRequired when cfg.Sink is nil; forwarded service errors otherwise.
func NewWithSink(cfg SinkConfig) (lg Logger, err error) {
	//: refuse a nil sink so callers get a typed sentinel rather than a nil panic.
	if cfg.Sink == nil {
		//: surface the documented sentinel so operators can HasCode(err, 4102).
		return nil, SinkConfigRequired
	}
	//: default the encoder to the text encoder bound to the real wall clock.
	enc := cfg.Encoder
	//: callers may pass a nil encoder to opt into the default text encoder.
	if enc == nil {
		//: reuse the same encoder NewText uses so semantics stay consistent.
		enc = encoder.NewText(clock.System)
	}
	//: wire encoder + sink into the generic handler with the minimum level.
	handler, hErr := svclogger.NewHandler(enc, cfg.Sink, cfg.MinLevel)
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

// Multi is a thin wrapper around the internal multi (fan-out) sink. It
// broadcasts every record to each branch in order and aggregates per-sink
// failures via errors.Join under the FANOUT_WRITE_FAILED sentinel.
//
// Params:
//   - branches: downstream Sinks; nil entries are silently skipped.
//
// Returns:
//   - sink: the fan-out Sink behind the public Sink interface.
func Multi(branches ...Sink) (sink Sink) {
	//: delegate to the internal fan-out implementation.
	return multi.New(branches...)
}

// ConsoleStderr returns the stderr console Sink used by Default. Exposed
// so callers building a Multi() topology can wire stderr alongside richer
// transports without re-implementing the convenience constructor.
//
// Returns:
//   - sink: a console Sink writing to os.Stderr.
func ConsoleStderr() (sink Sink) {
	//: reuse the canonical convenience constructor from the console sink.
	return console.NewStderr()
}

// ConsoleStdout returns the stdout console Sink. Same rationale as
// ConsoleStderr — exposed for Multi() compositions.
//
// Returns:
//   - sink: a console Sink writing to os.Stdout.
func ConsoleStdout() (sink Sink) {
	//: reuse the canonical convenience constructor from the console sink.
	return console.NewStdout()
}

// TextEncoder returns a fresh text Encoder bound to the real system clock.
// Callers passing a custom Encoder to NewWithSink usually want this as a
// starting point — it is the same encoder NewText / Default rely on.
//
// Returns:
//   - enc: a ready-to-use text Encoder.
func TextEncoder() (enc Encoder) {
	//: reuse the canonical text encoder constructor with the system clock.
	return encoder.NewText(clock.System)
}
