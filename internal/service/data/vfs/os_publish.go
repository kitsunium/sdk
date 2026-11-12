package vfs

import (
	"cmp"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path"
	"slices"
	"sync"
	"sync/atomic"

	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
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

// tempNameBytes is the entropy in a random temporary's name — the name a
// publication falls back to when its first one is taken. Sixteen bytes makes a
// collision with a concurrent publisher in the same directory not worth
// reasoning about, and O_EXCL turns the impossible case into an error rather
// than a silent overwrite.
const tempNameBytes int = 16

// tempSeedBytes is the entropy drawn once per process for the ordinary name:
// eight bytes, so a process cannot guess another's names, beside a counter
// that keeps the process's own names apart.
const tempSeedBytes int = 8

// tempAttempts bounds the names one publication tries: the ordinary one, then
// random ones while each is taken. A name is taken only when something else
// created it — a process that read this one's names in the directory and
// planted the next — and a random name cannot be planted ahead.
const tempAttempts int = 3

// tempNameRoom is the stack buffer a name is built in: the base name and a
// directory of most of the rest; a longer directory grows it on the heap.
const tempNameRoom int = 128

// tempCounterBytes is the width of the counter in a name, before its hex.
const tempCounterBytes int = 8

var (
	// tempCounter numbers this process's temporaries: unique within the
	// process, whatever filesystem and directory, so two publications never
	// draw one name.
	tempCounter atomic.Uint64
	// tempSeed is the process's hex-encoded seed, drawn on the first
	// publication (#813): the ordinary name costs one allocation and no
	// entropy call, where sixteen random bytes per publication cost the
	// entropy, the encoding and path.Join's four. crypto/rand.Read does not
	// fail since Go 1.24; were it to, every publication would report it, as
	// the random names did.
	tempSeed = sync.OnceValues(drawTempSeed)
)

// drawTempSeed draws the process's seed and hex-encodes it.
func drawTempSeed() (string, error) {
	var raw [tempSeedBytes]byte
	//: drawn once; the names' secrecy rests on it.
	if _, randErr := rand.Read(raw[:]); randErr != nil {
		//: the caller turns this into PublishFailed.
		return "", randErr
	}
	//: sixteen hex digits.
	return hex.EncodeToString(raw[:]), nil
}

// tempHandle is a temporary just created: the handle publish drives, and the
// name it has in the directory.
type tempHandle struct {
	file tempFile
	name string
}

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
//     Publications into one directory that reach this step while a flush of it
//     runs share the next flush (#803): each still waits for a flush BEGUN
//     after its own rename, and gets that flush's verdict.
//
// Steps 1–4 failing means nothing was published: the temporary is removed and
// the previous bytes are untouched. Step 5 failing means the opposite and gets
// its own code, [corevfs.DirectorySyncFailed].
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
	//: steps 1 to 4: nothing is published unless all four succeed.
	if stageErr := stage(ops, name, data, perm); stageErr != nil {
		//: PublishFailed — the destination is untouched.
		return stageErr
	}
	//: published; only the durability of the NAME is still in question.
	return syncPublished(ops, path.Dir(name), name)
}

// WriteAtomicAll publishes every file of files, in order, and flushes each
// directory they land in once, after the last rename into it
// ([corevfs.AtomicBatchWriter], issue #827).
//
// Each file runs steps 1 to 4 of [osFS.WriteAtomic] — a temporary beside it,
// the payload, the FILE's flush, the rename —, so each is atomic exactly as a
// publication is. Step 5 is the one a batch shares: a directory flush makes
// every rename into that directory before it durable, so one per directory
// after the last of them gives every file of the batch the guarantee a flush
// per file gave each, at half the device round trips.
//
// Every target is checked before any is written, so a refusal publishes
// nothing. A failure at a file's steps 1 to 4 leaves that file and the rest
// unpublished and their previous bytes untouched; the files already renamed
// are flushed with their directories first, so the count returned beside
// PublishFailed is a count of durable files.
func (o *osFS) WriteAtomicAll(files []corevfs.FileValue, perm fs.FileMode) (published int, err error) {
	//: the guard WriteAtomic runs, for every file, before a byte is written.
	for _, file := range files {
		//: InvalidPath, InvalidPermission, PathEscaped or NotRegularFile.
		if guardErr := o.guardFileTarget(file.Name, perm); guardErr != nil {
			return 0, guardErr
		}
	}
	//: the mechanics are a field so the batch is testable at every step.
	return publishAll(o.ops, files, perm)
}

// publishAll runs steps 1 to 4 for every file over ops, then step 5 once per
// directory renamed into, and returns how many files were renamed into place.
func publishAll(ops atomicOps, files []corevfs.FileValue, perm fs.FileMode) (published int, err error) {
	var dirs []string
	//: in order: a failure stops the batch at the file it struck.
	for index, file := range files {
		//: steps 1 to 4 of this file.
		if stageErr := stage(ops, file.Name, file.Data, perm); stageErr != nil {
			//: the files before it are visible: durable before the count says
			//: so — or DirectorySyncFailed, which wins.
			return index, cmp.Or(syncDirs(ops, dirs), stageErr)
		}
		//: each directory once, in the order the batch reached it.
		if dir := path.Dir(file.Name); !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	//: step 5, once per directory.
	return len(files), syncDirs(ops, dirs)
}

// syncDirs flushes every directory of dirs — all of them, a failure
// included, so as much of a batch as the device accepts is durable — and
// reports the first failure as DirectorySyncFailed.
func syncDirs(ops atomicOps, dirs []string) error {
	var first error
	//: every directory, whatever an earlier one answered.
	for _, dir := range dirs {
		//: the first refusal is the one reported.
		if syncErr := ops.syncDir(dir); syncErr != nil && first == nil {
			first = failDirSync(syncErr, dir)
		}
	}
	//: nil when every directory was flushed.
	return first
}

// stage runs steps 1 to 4 of a publication over ops: the temporary, the
// payload, the file's flush and the rename. On any failure the temporary is
// removed and nothing was published.
func stage(ops atomicOps, name string, data []byte, perm fs.FileMode) error {
	temp, createErr := createTemp(ops, path.Dir(name), perm)
	//: a temporary that was never created — its name not drawn, or every
	//: name tried taken or refused — leaves nothing behind.
	if createErr != nil {
		//: PublishFailed — the destination is untouched.
		return failPublish(createErr, kerrs.String("path", name))
	}
	file, tempName := temp.file, temp.name
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
	//: renamed into place; its directory's flush is the caller's.
	return nil
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
	return failDirSync(syncErr, name)
}

// SyncDir implements [corevfs.DirSyncer]: it opens the directory name through
// the root — "." included — and flushes its entries, the step WriteAtomic
// takes for the directory naming the file it publishes, offered for the
// directory naming a directory just created (#1060, ADR 0110's deferral).
func (o *osFS) SyncDir(name string) error {
	//: the read grammar: the root is the directory flushed most.
	if pathErr := corevfs.ValidatePath(name); pathErr != nil {
		//: InvalidPath.
		return pathErr
	}
	info, statErr := o.root.Stat(name)
	//: a directory that cannot be described cannot be flushed.
	if statErr != nil {
		//: DirectorySyncFailed, the *fs.PathError kept.
		return failDirSync(statErr, name)
	}
	//: a file is not what the call needs.
	if !info.IsDir() {
		//: NotRegularFile.
		return verdict(corevfs.NotRegularFile, kerrs.String("path", name))
	}
	//: the same mechanic step 5 of a publication runs.
	if syncErr := o.syncDir(name); syncErr != nil {
		//: DirectorySyncFailed, the cause kept.
		return failDirSync(syncErr, name)
	}
	//: flushed.
	return nil
}

// failDirSync wraps a directory flush's cause onto
// [corevfs.DirectorySyncFailed] by restating it, so the operating-system
// error survives in the chain — the shape of failRead / failWrite.
func failDirSync(cause error, name string) error {
	//: a durability verdict, never a rollback.
	return kerrs.Wrap(cause, kerrs.WrapParams{
		Code:     corevfs.CodeDirectorySyncFailed,
		Reason:   "DIRECTORY_SYNC_FAILED",
		Public:   "The file was published but the directory entry was not flushed",
		Private:  "service/data/vfs: fsync on a directory failed — after a successful rename, the content is visible and may not survive a power loss, deliberately not rolled back; from SyncDir, the directory's entries may not",
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

// createTemp creates a temporary in dir, exclusively, and returns it with its
// name: the ordinary name first, then a random one while the name tried is
// taken, up to tempAttempts names. Any other refusal ends it at once.
func createTemp(ops atomicOps, dir string, perm fs.FileMode) (tempHandle, error) {
	name, err := tempPath(dir)
	//: try names until one is created or the bound is reached.
	for attempt := 1; err == nil; attempt++ {
		file, createErr := ops.create(name, perm)
		//: created, refused for another reason, or out of names.
		if !errors.Is(createErr, fs.ErrExist) || attempt == tempAttempts {
			//: the temporary, or the last refusal.
			return tempHandle{file: file, name: name}, createErr
		}
		//: taken: a name nobody can have planted ahead.
		name, err = randomTempPath(dir)
	}
	//: no entropy: nothing was created.
	return tempHandle{}, err
}

// tempPath returns the ordinary name of a new temporary in dir:
// dir/.vfs-<the process's seed><the counter>.tmp, sixteen hex digits each,
// built on the stack and allocated once (#813). O_EXCL is still what makes it
// unused: createTemp falls back to a random name if it is taken.
//
// Returning a name in DIR and nowhere else is the same-filesystem guarantee:
// a rename between two entries of one directory cannot cross a device, so
// EXDEV is unreachable rather than handled. TestTheTemporaryLivesBesideItsTarget
// pins it, because the day someone "tidies" this into os.TempDir the failure
// is a cross-device rename in production and nothing at all in the tests.
func tempPath(dir string) (name string, err error) {
	seed, seedErr := tempSeed()
	//: an entropy source that fails is not worked around with a counter.
	if seedErr != nil {
		//: the caller turns this into PublishFailed.
		return "", seedErr
	}
	var room [tempNameRoom]byte
	buf := appendTempDir(room[:0], dir)
	buf = append(buf, tempPrefix...)
	buf = append(buf, seed...)
	buf = appendTempCounter(buf, tempCounter.Add(1))
	buf = append(buf, tempSuffix...)
	//: the one allocation: the name.
	return string(buf), nil
}

// randomTempPath returns a name of sixteen random bytes in dir — the name a
// publication falls back to when its ordinary one is taken.
func randomTempPath(dir string) (name string, err error) {
	var raw [tempNameBytes]byte
	_, randErr := rand.Read(raw[:])
	//: an entropy source that fails is not worked around with a counter.
	if randErr != nil {
		//: the caller turns this into PublishFailed.
		return "", randErr
	}
	buf := appendTempDir(nil, dir)
	buf = append(buf, tempPrefix...)
	buf = hex.AppendEncode(buf, raw[:])
	buf = append(buf, tempSuffix...)
	//: dir/.vfs-<32 hex digits>.tmp.
	return string(buf), nil
}

// appendTempDir appends dir and a separator, or nothing for ".": a
// root-level target's temporary is a bare base name, as path.Join made it.
// dir is path.Dir of a validated relative name, so it is clean, never "/".
func appendTempDir(buf []byte, dir string) []byte {
	//: the root: the base name alone.
	if dir == "." {
		//: unchanged.
		return buf
	}
	//: dir/.
	return append(append(buf, dir...), '/')
}

// appendTempCounter appends n as the hex of its tempCounterBytes big-endian
// bytes: fixed width, zero-padded, encoded from the stack.
func appendTempCounter(buf []byte, n uint64) []byte {
	var raw [tempCounterBytes]byte
	binary.BigEndian.PutUint64(raw[:], n)
	//: sixteen hex digits.
	return hex.AppendEncode(buf, raw[:])
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
		rename: o.root.Rename,
		remove: o.root.Remove,
		//: step 5, shared by the publications into one directory that reach
		//: it while a flush of that directory runs (#803).
		syncDir: o.flushes.sync,
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
