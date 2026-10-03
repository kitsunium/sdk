// Package vfs — an open directory in the in-memory filesystem.
package vfs

import (
	"io"
	"io/fs"
)

// memDir is an open directory. It carries the listing captured at Open, sorted
// as io/fs requires, so a concurrent write does not change what this handle
// reports halfway through a walk.
type memDir struct {
	// info is the metadata snapshot the handle reports.
	info memInfo
	// entries is the sorted listing captured at Open.
	entries []fs.DirEntry
	// offset is how far a paginated ReadDir has got.
	offset int
}

// Stat reports the directory's metadata.
func (d *memDir) Stat() (info fs.FileInfo, err error) {
	//: a snapshot, as with memFile.
	return d.info, nil
}

// Read refuses: a directory has no byte stream, and returning zero bytes with
// no error would make an io.Copy over one look like an empty file.
func (d *memDir) Read(_ []byte) (n int, err error) {
	//: the same shape the operating system reports for EISDIR.
	return 0, failRead(&fs.PathError{Op: "read", Path: d.info.name, Err: fs.ErrInvalid})
}

// Close releases the handle.
func (d *memDir) Close() error {
	//: nothing held.
	return nil
}

// ReadDir implements fs.ReadDirFile, which is what makes fs.WalkDir work on a
// handle obtained from Open alone.
func (d *memDir) ReadDir(n int) (entries []fs.DirEntry, err error) {
	remaining := d.entries[d.offset:]
	//: n <= 0 means "everything left, and no io.EOF", per io/fs.
	if n <= 0 {
		d.offset = len(d.entries)
		//: the whole listing.
		return remaining, nil
	}
	//: a paginated read that has run out reports io.EOF, unlike the n <= 0 form.
	if len(remaining) == 0 {
		//: exhausted.
		return nil, io.EOF
	}
	//: never return more than was asked for.
	if n > len(remaining) {
		n = len(remaining)
	}
	d.offset += n
	//: one page.
	return remaining[:n], nil
}
