// Package console registers the "console" writer factory. Importing the package
// (typically a blank import via pkg/v1/logger/writer) self-registers the factory
// so writer.Open("console", logger.ConsoleConfig{…}) resolves. The factory
// delegates to the existing terminal sink in service/logger/sink/console and
// applies the optional per-writer MinLevel via levelgate.
//
// The factory also satisfies core/writer.Decoder so the default-active console
// writer is YAML/JSON/TOML-reachable through FromConfig. Recognised option keys:
//
//	target     "stdout" (default) | "stderr"
//	min_level  "debug" | "info" | "warn" | "error" (default info)
//
// A malformed shape (wrong scalar type, unknown target, unknown level) returns
// the shared core/writer.WriterConfigInvalid sentinel. Per the Decoder
// secret-gate contract the error names only the writer and the failure kind —
// never a decoded option value.
package console

import (
	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	consolesink "github.com/kitsunium/sdk/internal/service/logger/sink/console"
	"github.com/kitsunium/sdk/internal/service/writer/levelgate"
)

// Writer is the registered console factory singleton. The blank assignment runs
// writer.Register at package load (no init()), mirroring the codec convention.
var Writer = writer.Register(&consoleFactory{})

// consoleFactory builds a console Sink from a writer.ConsoleConfig.
type consoleFactory struct{}

// Name reports the canonical key "console".
func (*consoleFactory) Name() writer.Name {
	//: the literal key consumers pass in a WriterSpec.
	return "console"
}

// Open builds the console sink for cfg, wrapping it in the per-writer level
// gate. A Config of the wrong concrete type yields the shared
// WriterConfigInvalid sentinel.
func (*consoleFactory) Open(cfg writer.Config) (sink corelogger.Sink, err error) {
	//: reject a mismatched config type with the shared sentinel.
	c, ok := cfg.(writer.ConsoleConfig)
	//: reject a wrong-type config OR a stream selector outside {stdout, stderr}
	//: (silently defaulting an out-of-range stream to stdout would hide a
	//: malformed config). Short-circuit keeps c unread when the assertion fails.
	if !ok || (c.Stream != writer.ConsoleStdout && c.Stream != writer.ConsoleStderr) {
		//: surface the documented config-invalid sentinel for both cases.
		return nil, writer.WriterConfigInvalid
	}
	//: apply the optional per-writer floor over the chosen stream sink.
	return levelgate.New(pickStream(c.Stream), c.MinLevel), nil
}

// Decode translates a raw topology option map into a ConsoleConfig. It is the
// YAML/JSON/TOML entry point used by FromConfig: it recognises "target" and
// "min_level" and ignores absent keys (the zero ConsoleConfig is valid). A key
// present with the wrong scalar type, an unknown target, or an unknown level
// name returns the shared WriterConfigInvalid sentinel — the offending value is
// never echoed (secret-gate contract), only the writer name is attached.
func (*consoleFactory) Decode(raw map[string]any) (cfg writer.Config, err error) {
	//: start from the valid zero config; absent keys keep their defaults.
	c := writer.ConsoleConfig{}
	//: map the optional stream selector; a malformed value aborts redacted.
	if serr := decodeStream(raw, &c.Stream); serr != nil {
		//: redacted: names only the writer, never the decoded target value.
		return nil, serr
	}
	//: map the optional severity floor; a malformed value aborts redacted.
	if lerr := decodeMinLevel(raw, &c.MinLevel); lerr != nil {
		//: redacted: names only the writer, never the decoded level value.
		return nil, lerr
	}
	//: hand back the typed config Open type-asserts.
	return c, nil
}

// decodeStream maps the optional "target" option onto dst. An absent key keeps
// the zero value (stdout); "stdout"/"" select stdout, "stderr" selects stderr.
// A non-string value or an unrecognised name yields the redacted
// WriterConfigInvalid sentinel — the value is never echoed (secret gate).
func decodeStream(raw map[string]any, dst *writer.ConsoleStream) error {
	//: absent target inherits the zero value (stdout).
	v, present := raw["target"]
	//: nothing to map when the key is absent.
	if !present {
		//: leave dst at its stdout default.
		return nil
	}
	//: the target must be a string scalar; reject any other shape.
	s, ok := v.(string)
	//: a non-string target is a malformed shape (redacted).
	if !ok {
		//: surface the shared sentinel without echoing the value.
		return configInvalid()
	}
	//: match the two canonical stream names.
	switch s {
	//: explicit stderr selection.
	case "stderr":
		//: route to os.Stderr.
		*dst = writer.ConsoleStderr
	//: explicit stdout selection (also the empty/default name).
	case "stdout", "":
		//: route to os.Stdout.
		*dst = writer.ConsoleStdout
	//: any other token is an unknown target (redacted).
	default:
		//: surface the shared sentinel without echoing the value.
		return configInvalid()
	}
	//: target mapped cleanly.
	return nil
}

// decodeMinLevel maps the optional "min_level" option onto dst. An absent key
// inherits the handler-global floor (zero Level). A non-string value or an
// unparsable name yields the redacted WriterConfigInvalid sentinel; ParseLevel
// is redaction-safe and never echoes its input.
func decodeMinLevel(raw map[string]any, dst *level.Level) error {
	//: absent min_level inherits the handler-global level (zero value).
	v, present := raw["min_level"]
	//: nothing to map when the key is absent.
	if !present {
		//: leave dst at its inherit default.
		return nil
	}
	//: the level must be a canonical name (string); reject other shapes.
	s, ok := v.(string)
	//: a non-string level is a malformed shape (redacted).
	if !ok {
		//: surface the shared sentinel without echoing the value.
		return configInvalid()
	}
	//: ParseLevel is redaction-safe — it returns a sentinel, never the input.
	lvl, perr := level.ParseLevel(s)
	//: an unknown level name is a malformed shape (redacted).
	if perr != nil {
		//: surface the shared sentinel without echoing the value.
		return configInvalid()
	}
	//: apply the parsed floor.
	*dst = lvl
	//: level mapped cleanly.
	return nil
}

// configInvalid returns the shared WriterConfigInvalid sentinel tagged with the
// writer name only. It never carries a decoded option value, honouring the
// Decoder secret-gate contract; origin-wins keeps errors.Is matching the
// sentinel.
func configInvalid() error {
	//: attach the writer name (never an option value) for offender ID.
	return errs.Wrap(writer.WriterConfigInvalid, errs.WrapParams{}, errs.String("writer", "console"))
}

// pickStream maps the config stream selector onto the concrete console sink.
func pickStream(s writer.ConsoleStream) corelogger.Sink {
	//: stderr is the explicit non-default choice.
	if s == writer.ConsoleStderr {
		//: bind to os.Stderr via the canonical convenience constructor.
		return consolesink.NewStderr()
	}
	//: default (ConsoleStdout / zero value) targets os.Stdout.
	return consolesink.NewStdout()
}
