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
