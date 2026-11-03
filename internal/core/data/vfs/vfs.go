package vfs

import "io/fs"

// FS is the read half of a filesystem, and it is io/fs.FS unchanged.
//
// It is an ALIAS rather than a fresh interface, so a vfs.FS and an fs.FS are
// the same type to the compiler and no conversion, adapter or assertion sits
// between an SDK filesystem and the standard library's walkers. The name
// exists to say, in one place, that the SDK's answer for reading is the
// stdlib's answer for reading.
type FS = fs.FS

// FileValue is one file of a batch [AtomicBatchWriter.WriteAtomicAll]
// publishes: the name it lands at and the whole of its content.
type FileValue struct {
	// Name is where the file is published, a slash-separated path
	// [ValidateWritePath] accepts, its directory already existing.
	Name string
	// Data is the file's content, written as given. The slice is read
	// during the call and never kept.
	Data []byte
}
