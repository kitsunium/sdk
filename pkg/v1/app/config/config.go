package config

import (
	"io/fs"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	svcconfig "github.com/kitsunium/sdk/internal/service/app/config"
)

// fileSource is FileSource's body: decl_gen.go writes FileSource, from the
// design, as one call of it.
func fileSource(format, path string) Source {
	//: delegate, converting the format name to the codec key type.
	return svcconfig.FileSource(codec.Format(format), path)
}

// fsSource is FSSource's body: decl_gen.go writes FSSource, from the
// design, as one call of it.
func fsSource(fsys fs.FS, format, path string) Source {
	//: delegate, converting the format name to the codec key type.
	return svcconfig.FSSource(fsys, codec.Format(format), path)
}
