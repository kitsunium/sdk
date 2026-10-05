package config

import (
	"io/fs"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	svcconfig "github.com/kitsunium/sdk/internal/service/app/config"
)

// FileSource returns a Source reading path and parsing it as format (the codec
// must be registered — blank-import its package, e.g. pkg/v1/data/codec).
func FileSource(format, path string) Source {
	//: delegate, converting the format name to the codec key type.
	return svcconfig.FileSource(codec.Format(format), path)
}

// FSSource returns a Source reading path inside fsys and parsing it as format
// — [FileSource] over an io/fs.FS, such as an embed.FS. path is an io/fs name:
// slash-separated and unrooted. It fails with SourceFailed exactly where
// FileSource does, a file fsys does not hold included, and a nil fsys is
// refused when the source loads. A traced load reports it as [LayerFile] with
// path as the detail.
func FSSource(fsys fs.FS, format, path string) Source {
	//: delegate, converting the format name to the codec key type.
	return svcconfig.FSSource(fsys, codec.Format(format), path)
}
