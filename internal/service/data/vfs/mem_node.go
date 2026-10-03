// Package vfs — one entry in the in-memory tree.
package vfs

import "io/fs"

// memNode is one entry in the in-memory tree: a regular file with bytes, or a
// directory with none. There is no third kind, and that is the point — an
// in-memory filesystem that grew symbolic links would be a second, different
// implementation of the resolution rules the operating system already owns,
// and the two would disagree the first time anyone leaned on them.
type memNode struct {
	// mode carries the permission bits, plus fs.ModeDir for a directory.
	mode fs.FileMode
	// data is nil for a directory and the whole content for a file.
	data []byte
}
