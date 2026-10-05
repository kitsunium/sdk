//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package vfs

import "os"

// platformNative reports that this GOOS has every mechanic the disk filesystem
// requires. NewOS reads it and refuses where it is false, rather than building
// a filesystem that would silently provide neither durability nor a meaningful
// permission mode.
const platformNative bool = true

// syncDirHandle flushes a directory's own entries to the device.
//
// It is the step people leave out, and leaving it out produces the exact
// failure atomic publication exists to prevent: after a crash the rename may
// be visible while the directory entry naming it is not, so the file has
// content and no name — or a name and no content. fsync(2) on the directory
// descriptor is what orders the two.
func syncDirHandle(dir *os.File) error {
	//: on a directory descriptor this is fsync(2) on the directory itself,
	//: not on the files it names.
	return dir.Sync()
}
