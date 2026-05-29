// Package writer — FileConfig value type for the "file" writer.
package writer

import "github.com/kitsunium/sdk/internal/core/logger/level"

// FileConfig configures the "file" writer — an append-only, symlink-hardened
// on-disk file. Path is required; an empty Path is rejected by the factory with
// the shared WriterConfigInvalid sentinel.
type FileConfig struct {
	// Path is the destination file; it is opened O_APPEND|O_CREATE|O_WRONLY
	// (+ O_NOFOLLOW on Linux) with mode 0600.
	Path string
	// MinLevel is the optional per-writer severity floor; the zero value
	// inherits the handler-global level.
	MinLevel level.Level
}
