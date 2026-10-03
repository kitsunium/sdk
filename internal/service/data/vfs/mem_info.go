// Package vfs — the fs.FileInfo the in-memory filesystem reports.
package vfs

import (
	"io/fs"
	"time"
)

// memInfo is the fs.FileInfo the in-memory filesystem reports.
//
// ModTime is deliberately the ZERO time. A filesystem with no device has no
// clock either, and inventing one would mean either reaching for the wall
// clock — which the SDK's test doubles exist to avoid — or carrying an
// injected clock through a type whose whole appeal is that NewMem takes no
// arguments. A stated absence beats a plausible fiction: code that depends on
// modification times depends on a real filesystem, and this says so instead of
// letting it appear to work.
type memInfo struct {
	// name is the base name, as fs.FileInfo requires — never the full path.
	name string
	// size is the byte length for a file and zero for a directory.
	size int64
	// mode mirrors the node's.
	mode fs.FileMode
}

// Name reports the base name of the file.
func (i memInfo) Name() string {
	//: fs.FileInfo requires the BASE name; the full path is the caller's.
	return i.name
}

// Size reports the length in bytes; it is zero for a directory.
func (i memInfo) Size() int64 {
	//: a directory holds no bytes of its own, so its size stays zero.
	return i.size
}

// Mode reports the file mode bits.
func (i memInfo) Mode() fs.FileMode {
	//: the permission triads, plus fs.ModeDir when this is a directory.
	return i.mode
}

// ModTime reports the zero time — see [memInfo].
func (i memInfo) ModTime() time.Time {
	//: a stated absence, never a plausible fiction — this filesystem has no
	//: clock, and pretending otherwise would let time-dependent code appear
	//: to work here and fail on a real filesystem.
	return time.Time{}
}

// IsDir reports whether the entry is a directory.
func (i memInfo) IsDir() bool {
	//: derived from the mode rather than stored twice, so the two cannot
	//: disagree.
	return i.mode.IsDir()
}

// Sys reports nil: there is no underlying data source to expose.
func (i memInfo) Sys() any {
	//: no inode, no stat buffer, nothing a caller could portably use.
	return nil
}
