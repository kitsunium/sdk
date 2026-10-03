// Package vfs — an open regular file in the in-memory filesystem.
package vfs

import (
	"io"
	"io/fs"
)

// memFile is an open regular file. It holds a COPY of the node's bytes taken
// at Open, so a reader is never racing a writer over the same slice — which is
// what an in-memory double has to get right to be usable under -race.
type memFile struct {
	// info is the metadata snapshot the handle reports.
	info memInfo
	// data is the private copy this handle reads from.
	data []byte
	// offset is how far Read has got.
	offset int
}

// Stat reports the metadata captured when the file was opened.
func (f *memFile) Stat() (info fs.FileInfo, err error) {
	//: a snapshot, like every other field of this handle.
	return f.info, nil
}

// Read copies the next bytes into p.
func (f *memFile) Read(p []byte) (n int, err error) {
	//: io.Reader's contract: at the end, zero bytes and io.EOF.
	if f.offset >= len(f.data) {
		//: exhausted.
		return 0, io.EOF
	}
	copied := copy(p, f.data[f.offset:])
	f.offset += copied
	//: a short read is legal and needs no error.
	return copied, nil
}

// Close releases the handle. It holds nothing, so it cannot fail.
func (f *memFile) Close() error {
	//: no descriptor, no device, nothing to flush.
	return nil
}
