// Package vfs — the disk filesystem's non-atomic write verbs.
package vfs

import (
	"io/fs"

	corevfs "github.com/kitsunium/sdk/internal/core/vfs"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// WriteFile writes data to name, creating it with perm or truncating it.
//
// It is NOT atomic and does not pretend to be: a reader that opens the file
// while this is running can see a truncated one. [osFS.WriteAtomic] is the
// call for content anyone else may be reading. This one exists because not
// every write is a publication, and paying for a temporary file plus two
// flushes to drop a scratch file is a cost with nothing on the other side.
func (o *osFS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	//: grammar, mode, and what is already sitting at that name.
	if guardErr := o.guardFileTarget(name, perm); guardErr != nil {
		//: InvalidPath, InvalidPermission, PathEscaped or NotRegularFile.
		return guardErr
	}
	//: a missing parent directory is NOT created — see the port's doc.
	if writeErr := o.root.WriteFile(name, data, perm); writeErr != nil {
		//: WriteFailed, with the *fs.PathError intact underneath.
		return failWrite(writeErr, kerrs.String("path", name))
	}
	//: on disk.
	return nil
}

// MkdirAll creates name and every missing parent.
func (o *osFS) MkdirAll(name string, perm fs.FileMode) error {
	//: an existing directory is success here, unlike WriteFile.
	if guardErr := o.guardDirTarget(name, perm); guardErr != nil {
		//: InvalidPath, InvalidPermission, PathEscaped or NotRegularFile.
		return guardErr
	}
	//: the target itself was cleared above; an ANCESTOR can still be a file.
	if mkErr := o.root.MkdirAll(name, perm); mkErr != nil {
		//: NotRegularFile when a component is in the way, WriteFailed
		//: otherwise — the two are different problems for the caller.
		return o.classifyBlocked(name, mkErr)
	}
	//: the whole chain exists.
	return nil
}

// Remove removes one file or one empty directory.
//
// An absent name is an ERROR here and success in [osFS.RemoveAll]. That
// asymmetry is os.Remove's and os.RemoveAll's, kept deliberately: a precise
// removal that names something that is not there is usually a mistake worth
// hearing about, while a recursive one is a cleanup whose whole point is to be
// safe to run twice.
func (o *osFS) Remove(name string) error {
	//: no mode is involved, so only the path grammar applies. A symbolic link
	//: is NOT refused: unlinkat removes the link, never its target, so this is
	//: the one write verb a link cannot redirect.
	if pathErr := corevfs.ValidateWritePath(name); pathErr != nil {
		//: InvalidPath.
		return pathErr
	}
	//: the common failure has its own verdict.
	if rmErr := o.root.Remove(name); rmErr != nil {
		//: DirectoryNotEmpty or WriteFailed.
		return o.classifyRemove(name, rmErr)
	}
	//: gone.
	return nil
}

// classifyRemove separates "that directory still has entries" from every other
// removal failure, because only the first one has an obvious next call.
func (o *osFS) classifyRemove(name string, cause error) error {
	info, statErr := o.root.Lstat(name)
	//: an errno comparison would be platform-specific and would have to be
	//: build-tagged; observing the directory is portable and says the same.
	if statErr == nil && info.IsDir() {
		entries, readErr := fs.ReadDir(o.read, name)
		//: a directory that still lists something is the ENOTEMPTY case.
		if readErr == nil && len(entries) > 0 {
			//: DirectoryNotEmpty — RemoveAll is the call that was wanted.
			return verdict(corevfs.DirectoryNotEmpty, kerrs.String("path", name))
		}
	}
	//: anything else keeps the operating system's own account of it.
	return failWrite(cause, kerrs.String("path", name))
}

// RemoveAll removes name and everything beneath it. An absent name is success.
func (o *osFS) RemoveAll(name string) error {
	//: the write grammar refuses "." here, which matters more than anywhere
	//: else in the package: RemoveAll(".") would empty the whole filesystem
	//: and reads, in a diff, exactly like a no-op.
	if pathErr := corevfs.ValidateWritePath(name); pathErr != nil {
		//: InvalidPath.
		return pathErr
	}
	//: idempotent by contract.
	if rmErr := o.root.RemoveAll(name); rmErr != nil {
		//: WriteFailed.
		return failWrite(rmErr, kerrs.String("path", name))
	}
	//: gone, or never there.
	return nil
}
