// Package config — file Source over an io/fs.FS (codec-dispatched parse).
package config

import (
	"io/fs"

	"github.com/kitsunium/sdk/internal/core/codec"
	coreconfig "github.com/kitsunium/sdk/internal/core/config"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// fsSource reads a file inside an fs.FS and parses it through the codec
// registry into a map — FileSource, with the filesystem as a parameter.
type fsSource struct {
	fsys   fs.FS
	format codec.Format
	path   string
}

// FSSource returns a Source reading path inside fsys and parsing it as format
// (the codec must be registered — blank-import its package, e.g.
// pkg/v1/codec). It is [FileSource] over an fs.FS: the same codec dispatch,
// the same CONFIG_SOURCE_FAILED for every way the file cannot be read or
// parsed, and the same description — layer "file", detail the path as given —
// so a traced load reports it as a file. The reason it exists is a program
// that carries its configuration inside its own binary:
//
//	//go:embed config
//	var files embed.FS
//
//	source := config.FSSource(files, "yaml", "config/config.yaml")
//
// path is an io/fs name — slash-separated and unrooted, fs.ValidPath — and a
// name that is not one is refused when the source loads.
//
// A file the filesystem does not hold is REFUSED, exactly as FileSource
// refuses a path it cannot open, and never read as an empty layer. The two are
// one source over two filesystems and must answer alike; and the usual reason
// a file is missing from an embedded tree is a build mistake — an embed
// pattern that matched nothing, a file renamed and not re-embedded — which,
// read as empty, starts the program on its defaults with nothing said. A layer
// that is optional by design is the caller's decision, one fs.Stat away.
//
// A nil fsys is refused when the source loads, rather than panicking there.
func FSSource(fsys fs.FS, format codec.Format, path string) coreconfig.Source {
	//: a stateless reader bound to a filesystem, a path and a format.
	return fsSource{fsys: fsys, format: format, path: path}
}

// Load reads the file from the filesystem and decodes it into a flat/nested
// map.
func (s fsSource) Load() (values map[string]any, err error) {
	//: no filesystem, nothing to read — a refusal, not a nil dereference.
	if s.fsys == nil {
		//: CONFIG_SOURCE_FAILED, naming what was missing.
		return nil, kerrs.Wrap(coreconfig.ConfigSourceFailed, kerrs.WrapParams{}, kerrs.String("fs", "nil"))
	}
	//: read the raw file bytes; fs.ReadFile applies the fs.ValidPath rule.
	data, err := fs.ReadFile(s.fsys, s.path)
	//: an unreadable file — absent included — is a source failure.
	if err != nil {
		//: surface CONFIG_SOURCE_FAILED.
		return nil, wrapAs(coreconfig.ConfigSourceFailed, err)
	}
	//: the bytes are read; the declared format decides the rest.
	return parseDocument(s.format, data)
}

// Describe implements core/config.Describer exactly as FileSource does: the
// layer is a file and the detail is the path as given.
func (s fsSource) Describe(_ string) (layer, detail string) {
	//: one path answers for the whole document.
	return coreconfig.LayerFile, s.path
}
