// Package vfs — atomic publication: the five mechanics, the order they run in,
// and the cleanup that makes a failure indistinguishable from never having
// started.
package vfs

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"io/fs"
	"os"
	"path"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// tempPrefix and tempSuffix bracket the name of a temporary that has not been
// published yet. The leading dot keeps it out of an ordinary listing and the
// suffix makes an orphan — which only a crashed process can leave — obvious to
// whoever finds one.
const (
	tempPrefix string = ".vfs-"
	tempSuffix string = ".tmp"
)

// tempNameBytes is the entropy in a temporary's name. Sixteen bytes makes a
// collision with a concurrent publisher in the same directory not worth
// reasoning about, and O_EXCL turns the impossible case into an error rather
// than a silent overwrite.
const tempNameBytes int = 16

// tempFile is the narrow view of a freshly created temporary that [publish]
// drives. *os.File satisfies it; nothing else in the SDK does.
//
// It is an interface rather than the concrete handle for exactly one reason,
// and it is the reason the whole domain exists: it makes the FAILURE paths
// reachable. A write that stops halfway, a Sync that errors, a Close that
// refuses — none of those can be provoked on a real file without a hostile
// filesystem, and all of them are paths on which the previous content must
// still be intact afterwards. An untested cleanup is a cleanup that has never
// run.
type tempFile interface {
	// Write appends part of the payload.
	Write(payload []byte) (n int, err error)
	// Sync flushes it to the device, BEFORE the rename.
	Sync() error
	// Close releases the descriptor.
	Close() error
}

// atomicOps is the set of filesystem mechanics [publish] drives.
//
// Holding them as a struct of functions is what keeps publish a pure function
// of its dependencies: production supplies the ones backed by os.Root, and
// publish_internal_test.go supplies ones that fail at a chosen step and then
// checks, by hash, that the destination did not move.
type atomicOps struct {
	// create makes a new file at an unused path with mode perm.
	create func(name string, perm fs.FileMode) (tempFile, error)
	// rename replaces to with from in one indivisible step.
	rename func(from, to string) error
	// remove unlinks an abandoned temporary.
	remove func(name string) error
	// syncDir flushes a directory's entries to the device.
	syncDir func(dir string) error
}

// WriteAtomic publishes data at name.
//
// The order is the contract, and every step earns its place:
//
//  1. create a temporary IN THE SAME DIRECTORY as the target. Same directory
//     means same filesystem by construction, so the rename in step 4 cannot
//     fail with EXDEV — a cross-device rename is not handled here because it
//     is not reachable here.
//  2. write the payload into it.
//  3. flush the FILE. A rename over data still sitting in the page cache is
//     atomic about nothing once the machine loses power.
//  4. rename. This is the indivisible step: POSIX requires rename(2) to
//     replace atomically, so a concurrent reader gets the old inode or the
//     new one and never a partial file.
//  5. flush the DIRECTORY. Without it the entry naming the new inode may not
//     survive a crash, and the file would come back with content and no name.
//
// Steps 1–4 failing means nothing was published: the temporary is removed and
// the previous bytes are untouched. Step 5 failing means the opposite and gets
// its own code, [DirectorySyncFailed].
func (o *osFS) WriteAtomic(name string, data []byte, perm fs.FileMode) error {
	//: the same guard WriteFile runs — a publication must not land on a
	//: symbolic link's target either.
	if guardErr := o.guardFileTarget(name, perm); guardErr != nil {
		//: InvalidPath, InvalidPermission, PathEscaped or NotRegularFile.
		return guardErr
	}
	//: the mechanics are a field so this call is testable at every step.
	return publish(o.ops, name, data, perm)
}

// publish runs the five steps over ops. It is deliberately free of any
// reference to os.Root, so the test that proves its guarantee can substitute
// mechanics that fail.
func publish(ops atomicOps, name string, data []byte, perm fs.FileMode) error {
	dir := path.Dir(name)
	tempName, nameErr := tempPath(dir)
	//: entropy failing is the one step with nothing to clean up.
	if nameErr != nil {
		//: PublishFailed — nothing was created.
		return failPublish(nameErr, kerrs.String("path", name))
	}
	file, createErr := ops.create(tempName, perm)
	//: a temporary that was never created leaves nothing behind either.
	if createErr != nil {
		//: PublishFailed — the destination is untouched.
		return failPublish(createErr, kerrs.String("path", name))
	}
	//: from here on, every failure path MUST remove the temporary.
	if writeErr := writeAndSync(file, data); writeErr != nil {
		//: PublishFailed, after the orphan is gone.
		return discard(ops, tempName, name, writeErr)
	}
	//: THE atomic step.
	if renameErr := ops.rename(tempName, name); renameErr != nil {
		//: PublishFailed — the previous content is still the current content.
		return discard(ops, tempName, name, renameErr)
	}
	//: published; only the durability of the NAME is still in question.
	return syncPublished(ops, dir, name)
}

// writeAndSync writes the payload, flushes it to the device, and closes the
// handle. It never renames: the caller owns the ordering.
func writeAndSync(file tempFile, data []byte) error {
	//: one Write; the payload is a whole file, never a stream.
	if _, writeErr := file.Write(data); writeErr != nil {
		//: close before returning so the descriptor is not leaked along with
		//: the failure — the removal that follows does not need it open.
		return cmp.Or(writeErr, file.Close())
	}
	//: flush BEFORE the rename, or the rename publishes a name for bytes that
	//: may not be on the device yet.
	if syncErr := file.Sync(); syncErr != nil {
		//: same rule: release the descriptor, keep the first failure.
		return cmp.Or(syncErr, file.Close())
	}
	//: close last, so a close error is never mistaken for a write error.
	return file.Close()
}

// syncPublished performs step 5 and reports its distinct verdict.
func syncPublished(ops atomicOps, dir, name string) error {
	syncErr := ops.syncDir(dir)
	//: the ordinary path.
	if syncErr == nil {
		//: published and durable.
		return nil
	}
	//: deliberately NOT rolled back: the rename already happened, so every
	//: reader now sees the new content, and undoing it would mean a second
	//: non-atomic write to repair a durability problem.
	return kerrs.Wrap(syncErr, kerrs.WrapParams{
		Code:     CodeDirectorySyncFailed,
		Reason:   "DIRECTORY_SYNC_FAILED",
		Public:   "The file was published but the directory entry was not flushed",
		Private:  "service/vfs: fsync on the parent directory failed AFTER a successful rename; the content is visible and may not survive a power loss — deliberately not rolled back",
		ExitCode: exitIOErr,
	}, kerrs.String("path", name))
}

// discard removes an abandoned temporary and returns the failure that caused
// it to be abandoned.
//
// The removal is CHECKED rather than discarded, and then deliberately
// subordinate to cause: replacing "the disk is full" with "the orphan would
// not unlink" would report the consequence instead of the problem. The
// destination is not touched on any path through here — that is the guarantee
// [failPublish] hands to the caller.
func discard(ops atomicOps, tempName, name string, cause error) error {
	removeErr := ops.remove(tempName)
	//: a removal that failed is still reported, just not instead of cause.
	return failPublish(cmp.Or(cause, removeErr), kerrs.String("path", name))
}

// tempPath returns an unused name in dir.
//
// Returning a name in DIR and nowhere else is the same-filesystem guarantee:
// a rename between two entries of one directory cannot cross a device, so
// EXDEV is unreachable rather than handled. TestTheTemporaryLivesBesideItsTarget
// pins it, because the day someone "tidies" this into os.TempDir the failure
// is a cross-device rename in production and nothing at all in the tests.
func tempPath(dir string) (name string, err error) {
	var raw [tempNameBytes]byte
	_, randErr := rand.Read(raw[:])
	//: an entropy source that fails is not worked around with a counter.
	if randErr != nil {
		//: the caller turns this into PublishFailed.
		return "", randErr
	}
	//: path.Join collapses the "." case to a bare basename, which is what a
	//: root-level target needs.
	return path.Join(dir, tempPrefix+hex.EncodeToString(raw[:])+tempSuffix), nil
}

// nativeOps binds the five mechanics to this filesystem's os.Root, so every
// one of them is confined to the same tree the rest of the package is.
func (o *osFS) nativeOps() atomicOps {
	//: every mechanic goes through o.root, so confinement is not something
	//: publish has to remember — it cannot reach outside the tree at all.
	return atomicOps{
		create: func(name string, perm fs.FileMode) (tempFile, error) {
			//: O_EXCL turns a name collision into an error instead of an
			//: overwrite of somebody else's in-flight publication.
			file, openErr := o.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
			//: never hand back a non-nil interface holding a nil *os.File.
			if openErr != nil {
				//: the caller reports PublishFailed.
				return nil, openErr
			}
			//: a real handle.
			return file, nil
		},
		rename:  o.root.Rename,
		remove:  o.root.Remove,
		syncDir: o.syncDir,
	}
}

// syncDir opens a directory through the root and flushes its entries.
func (o *osFS) syncDir(dir string) error {
	handle, openErr := o.root.Open(dir)
	//: a directory that cannot be opened cannot be flushed.
	if openErr != nil {
		//: the caller reports DirectorySyncFailed.
		return openErr
	}
	syncErr := syncDirHandle(handle)
	closeErr := handle.Close()
	//: the flush failure wins; a close failure is the answer only when there
	//: was nothing better to report.
	return cmp.Or(syncErr, closeErr)
}
