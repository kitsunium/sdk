// Package config — file Source (codec-dispatched parse).
package config

import (
	"os"

	"github.com/kitsunium/sdk/internal/core/codec"
	coreconfig "github.com/kitsunium/sdk/internal/core/config"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// fileSource reads a file and parses it through the codec registry into a map.
type fileSource struct {
	format codec.Format
	path   string
}

// FileSource returns a Source reading path and parsing it as format (the codec
// must be registered — blank-import its package, e.g. pkg/v1/codec).
func FileSource(format codec.Format, path string) coreconfig.Source {
	//: a stateless reader bound to a path + format.
	return fileSource{format: format, path: path}
}

// Load reads the file and decodes it into a flat/nested map.
func (s fileSource) Load() (values map[string]any, err error) {
	//: read the raw file bytes.
	data, err := os.ReadFile(s.path)
	//: an unreadable file is a source failure.
	if err != nil {
		//: surface CONFIG_SOURCE_FAILED.
		return nil, wrapAs(coreconfig.ConfigSourceFailed, err)
	}
	//: the bytes are read; the declared format decides the rest.
	return parseDocument(s.format, data)
}

// Describe implements core/config.Describer: the layer is a file and the
// detail is its path, for every key it supplied.
func (s fileSource) Describe(_ string) (layer, detail string) {
	//: one path answers for the whole document.
	return coreconfig.LayerFile, s.path
}

// parseDocument decodes one configuration document through the codec
// registry. It is the half FileSource and FSSource share: they differ only in
// where the bytes come from, so they refuse the same documents with the same
// code, and a caller swapping one for the other sees no other difference.
//
// It runs AFTER the read, so an unreadable file and an unregistered format
// stay distinguishable by the fields their errors carry.
func parseDocument(format codec.Format, data []byte) (values map[string]any, err error) {
	//: resolve the codec for the declared format.
	c, ok := codec.Lookup(format)
	//: an unregistered format cannot be parsed.
	if !ok {
		//: report as a source failure (the consumer must blank-import the codec).
		return nil, kerrs.Wrap(coreconfig.ConfigSourceFailed, kerrs.WrapParams{}, kerrs.String("format", string(format)))
	}
	//: parse the bytes into a map.
	out := make(map[string]any, 0)
	//: a parse error is a source failure.
	if err := c.Unmarshal(data, &out); err != nil {
		//: surface CONFIG_SOURCE_FAILED with the parse cause.
		return nil, wrapAs(coreconfig.ConfigSourceFailed, err)
	}
	//: the parsed layer.
	return out, nil
}
