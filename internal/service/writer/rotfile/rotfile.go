// Package rotfile registers the "rotfile" writer factory (ADR 0014): a
// size-capped, on-disk file sink that rotates when a write would exceed
// MaxBytes and optionally gzips each rotated file. Importing the package
// (typically a blank import via pkg/v1/logger/writer) self-registers the
// factory so writer.Open("rotfile", Config{…}) resolves.
//
// Unlike service/writer/file, this sink owns its descriptor directly because it
// must close + rename + reopen Path across a rotation; it cannot delegate to
// the append-only sink/file. The security-critical hardening of sink/file is
// preserved and, crucially, RE-RUN on every reopen: the symlink refusal +
// O_NOFOLLOW + 0600 checks fire each time Path is recreated after a rename/gzip
// cycle, not only at first New (CWE-59). Rotated .N and .N.gz siblings are
// forced to 0600 so a gzip never leaks default permissions.
//
// Durability note: the rename is not followed by a directory fsync, so a crash
// between rename and reopen can leave Path missing until the next write — an
// accepted crash window for logs, documented rather than paid for on every
// rotation.
package rotfile

import (
	"github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/service/writer/levelgate"
)

// Writer is the registered rotfile factory singleton. The blank assignment runs
// writer.Register at package load (no init()), mirroring the codec convention.
var Writer = writer.Register(&rotFileFactory{})

// rotFileFactory builds a rotating file Sink from a Config.
type rotFileFactory struct{}

// Name reports the canonical key "rotfile".
func (*rotFileFactory) Name() writer.Name {
	//: the literal key consumers pass in a WriterSpec.
	return "rotfile"
}

// Open builds the rotating sink from cfg and wraps it in the per-writer level
// gate. A Config of the wrong concrete type yields the shared
// WriterConfigInvalid sentinel; an empty Path or a symlink target surfaces the
// rotfile sink's own RotFileOpenFailed sentinel (origin wins).
func (*rotFileFactory) Open(cfg writer.Config) (sink logger.Sink, err error) {
	//: reject a mismatched config type with the shared sentinel.
	c, ok := cfg.(Config)
	//: type assertion guards the rest of the construction.
	if !ok {
		//: surface the documented config-type-mismatch sentinel.
		return nil, writer.WriterConfigInvalid
	}
	//: open the active file with the hardened, reopen-safe constructor;
	//: pass cfg by pointer so the grown Config value is not copied.
	base, oerr := newRotatingSink(&c)
	//: forward the typed open error unchanged (origin wins).
	if oerr != nil {
		//: RotFileOpenFailed already carries the right code/reason.
		return nil, oerr
	}
	//: apply the optional per-writer floor over the opened rotating sink.
	return levelgate.New(base, c.MinLevel), nil
}
