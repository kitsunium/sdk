package logger

import (
	"io"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	svclogger "github.com/kitsunium/sdk/internal/service/observe/logger"
	"github.com/kitsunium/sdk/internal/service/observe/logger/encoder"
	"github.com/kitsunium/sdk/internal/service/observe/logger/sink/console"
)

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

// NewWriterSink wraps an arbitrary [io.Writer] in a [Sink] so callers can
// compose plain file / network / bytes.Buffer writers with [Multi] for
// fan-out. The returned Sink serialises Write calls through an internal
// mutex (identical contract to [ConsoleStderr] / [ConsoleStdout]).
//
// Returns [WriterRequired] when w is nil.
//
//	f, _ := os.OpenFile("/var/log/app.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
//	fileSink, _ := logger.NewWriterSink(f)
//	lg, _ := logger.NewWithSink(logger.SinkConfig{
//	    Sink: logger.Multi(logger.ConsoleStderr(), fileSink),
//	})
func NewWriterSink(w io.Writer) (sink Sink, err error) {
	//: refuse nil writers up-front with the same sentinel NewText uses,
	//: so callers see a consistent failure mode across the public API.
	if w == nil {
		//: surface the documented sentinel so HasCode(err, 1.1.0.1) works.
		return nil, WriterRequired
	}
	//: delegate to the canonical console sink constructor; its mutex
	//: + error-wrapping behaviour is what we want for any plain writer.
	built, bErr := console.New(w)
	//: forward any console-level error unchanged (origin wins).
	if bErr != nil {
		//: console.New only fails on nil writer — already filtered above,
		//: but forward defensively in case the contract evolves.
		return nil, bErr
	}
	//: hand back the validated sink to the caller.
	return built, nil
}

// TextEncoder returns a fresh text Encoder bound to the real system clock.
// Callers passing a custom Encoder to NewWithSink usually want this as a
// starting point — it is the same encoder NewText / Default rely on.
func TextEncoder() Encoder {
	//: reuse the canonical text encoder constructor with the system clock.
	return encoder.NewText(clock.System)
}
