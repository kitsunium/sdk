// Package file registers the "file" writer factory. Importing the package
// (typically a blank import via pkg/v1/logger/writer) self-registers the factory
// so writer.Open("file", logger.FileConfig{…}) resolves. The factory delegates
// to the existing append-only, symlink-hardened sink in
// service/logger/sink/file and applies the optional per-writer MinLevel via
// levelgate.
package file

import (
	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/writer"
	filesink "github.com/kitsunium/sdk/internal/service/logger/sink/file"
	"github.com/kitsunium/sdk/internal/service/writer/levelgate"
)

// Writer is the registered file factory singleton. The blank assignment runs
// writer.Register at package load (no init()), mirroring the codec convention.
var Writer = writer.Register(&fileFactory{})

// fileFactory builds a file Sink from a writer.FileConfig.
type fileFactory struct{}

// Name reports the canonical key "file".
func (*fileFactory) Name() writer.Name {
	//: the literal key consumers pass in a WriterSpec.
	return "file"
}

// Open opens the configured path through the hardened file sink and wraps it in
// the per-writer level gate. A Config of the wrong concrete type yields the
// shared WriterConfigInvalid sentinel; an empty Path surfaces the file sink's
// own PathEmpty sentinel (origin wins).
func (*fileFactory) Open(cfg writer.Config) (sink corelogger.Sink, err error) {
	//: reject a mismatched config type with the shared sentinel.
	c, ok := cfg.(writer.FileConfig)
	//: type assertion guards the rest of the construction.
	if !ok {
		//: surface the documented config-type-mismatch sentinel.
		return nil, writer.WriterConfigInvalid
	}
	//: delegate to the hardened append-only sink (validates path + symlink).
	base, oerr := filesink.New(c.Path)
	//: forward the sink's typed error unchanged (origin wins).
	if oerr != nil {
		//: PathEmpty / OpenFailed already carry the right code/reason.
		return nil, oerr
	}
	//: apply the optional per-writer floor over the opened file sink.
	return levelgate.New(base, c.MinLevel), nil
}
