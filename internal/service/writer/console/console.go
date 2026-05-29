// Package console registers the "console" writer factory. Importing the package
// (typically a blank import via pkg/v1/logger/writer) self-registers the factory
// so writer.Open("console", logger.ConsoleConfig{…}) resolves. The factory
// delegates to the existing terminal sink in service/logger/sink/console and
// applies the optional per-writer MinLevel via levelgate.
package console

import (
	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/writer"
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
	//: type assertion guards the rest of the construction.
	if !ok {
		//: surface the documented config-type-mismatch sentinel.
		return nil, writer.WriterConfigInvalid
	}
	//: apply the optional per-writer floor over the chosen stream sink.
	return levelgate.New(pickStream(c.Stream), c.MinLevel), nil
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
