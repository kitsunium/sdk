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
	//: resolve the codec for the declared format.
	c, ok := codec.Lookup(s.format)
	//: an unregistered format cannot be parsed.
	if !ok {
		//: report as a source failure (the consumer must blank-import the codec).
		return nil, kerrs.Wrap(coreconfig.ConfigSourceFailed, kerrs.WrapParams{}, kerrs.String("format", string(s.format)))
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
