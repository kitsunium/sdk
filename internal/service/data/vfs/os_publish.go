package vfs

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
	"github.com/kitsunium/sdk/internal/kernel/concur/group"
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

// publishWindow is how many files of a batch ([osFS.WriteAtomicAll]) have
// their temporaries open and their flushes in flight at once (#827): enough
// for a journal to commit a window's flushes together, few enough that a
// batch of any size holds sixteen descriptors and sixteen blocked threads at
// most, and that a crash leaves sixteen orphaned temporaries at most.
const publishWindow int = 16

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
// The batch runs in windows of [publishWindow] files: each window's
// temporaries are created and written in order, each one's flush started as
// soon as it is written, so the window's flushes are in flight together —
// still one per file, each before its own rename, the journal free to
// commit them together (ADR 0056, amended 2026-11-15) —; once every flush
// has returned, the renames follow in the batch's order, stopping at the
// first file whose steps 1 to 4 failed. The next window begins only once
// this one is renamed whole.
//
// Every target is checked before any is written, so a refusal publishes
// nothing. A failure at a file's steps 1 to 4 leaves that file and the rest
// unpublished and their previous bytes untouched, no temporary of theirs
// surviving; the files already renamed are flushed with their directories
// first, so the count returned beside PublishFailed is a count of durable
// files.
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

// publishAll runs steps 1 to 4 for every file over ops, a window at a time,
// then step 5 once per directory renamed into, and returns how many files
// were renamed into place.
func publishAll(ops atomicOps, files []corevfs.FileValue, perm fs.FileMode) (published int, err error) {
	var dirs []string
	//: window after window, each renamed whole before the next is written.
	for start := 0; start < len(files); start += publishWindow {
		window := files[start:min(start+publishWindow, len(files))]
		renamed, stageErr := stageWindow(ops, window, perm)
		dirs = appendDirs(dirs, window[:renamed])
		//: a failure stops the batch at the file it struck.
		if stageErr != nil {
			//: the files before it are visible: durable before the count says
			//: so — or DirectorySyncFailed, which wins.
			return start + renamed, cmp.Or(syncDirs(ops, dirs), stageErr)
		}
	}
	//: step 5, once per directory.
	return len(files), syncDirs(ops, dirs)
}

// appendDirs appends to dirs the directory of every file of files it does
// not hold yet, in the order the batch reaches them.
func appendDirs(dirs []string, files []corevfs.FileValue) []string {
	//: each directory once.
	for _, file := range files {
		//: a directory not seen yet.
		if dir := path.Dir(file.Name); !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	//: the directories renamed into so far.
	return dirs
}

// stageWindow runs steps 1 to 4 for every file of window: the temporaries
// created and written in order, each one's flush started as soon as it is
// written ([writeWindow]), then, once every flush has returned, the renames
// in order, up to the first file whose write, flush, close or rename failed.
// That file's temporary and every later one are removed, so renamed counts
// the files published, from the first, and err is the failure of
// window[renamed] — PublishFailed, the lowest index winning whatever order
// the flushes answered in.
func stageWindow(ops atomicOps, window []corevfs.FileValue, perm fs.FileMode) (renamed int, err error) {
	temps, verdicts, writeErr := writeWindow(ops, window, perm)
	//: step 4, in the batch's order.
	for index, temp := range temps {
		var failure error
		//: a flush of the window failed: this file's own verdict.
		if verdicts != nil {
			failure = verdicts[index]
		}
		//: flushed to the device: THE atomic step.
		if failure == nil {
			failure = ops.rename(temp.name, window[index].Name)
		}
		//: this file and every later one stay unpublished.
		if failure != nil {
			//: PublishFailed, after their temporaries are gone.
			return index, discardAll(ops, temps[index:], window[index].Name, failure)
		}
	}
	//: every temporary written is renamed; writeErr names the file the
	//: writing stopped at, its temporary already removed.
	return len(temps), writeErr
}

// writeWindow runs steps 1 and 2 for the files of window, in order, and
// starts step 3 for each as soon as it is written, so a file's flush
// overlaps the writing of the next ones and the flushes of the others. It
// returns the temporaries written, flushed and closed, once every flush has
// returned, and each flush's verdict at its temporary's index — nil, the
// slice, when every flush succeeded. The first file whose temporary cannot
// be created or written ends the writing: its temporary removed, its
// failure returned as PublishFailed beside the temporaries before it.
func writeWindow(ops atomicOps, window []corevfs.FileValue, perm fs.FileMode) (temps []tempHandle, verdicts []error, err error) {
	temps = make([]tempHandle, 0, len(window))
	flusher := newWindowFlusher(len(window))
	//: every flush started has returned when this does, on a panic too.
	defer func() {
		//: no flush failed: no verdict to read.
		if flusher.wait() == nil {
			verdicts = nil
		}
	}()
	//: in order: the window stops at the first file that fails.
	for index, file := range window {
		temp, createErr := createTemp(ops, file.Name, perm)
		//: never created: nothing to remove.
		if createErr != nil {
			//: PublishFailed — the destination is untouched.
			return temps, flusher.verdicts, failPublish(createErr, kerrs.String("path", file.Name))
		}
		//: half-written: closed by writeTemp, removed by discard.
		if writeErr := writeTemp(temp.file, file.Data); writeErr != nil {
			//: PublishFailed, after the orphan is gone.
			return temps, flusher.verdicts, discard(ops, temp.name, file.Name, writeErr)
		}
		temps = append(temps, temp)
		flusher.start(index, temp.file)
	}
	//: every file of the window written; its flushes joined by the defer.
	return temps, flusher.verdicts, nil
}

// windowFlusher runs step 3 for the temporaries of one window, each on a
// goroutine of its own: a flush and its close, the verdict written at the
// temporary's index. Concurrent flushes of one filesystem are what a
// journal commits together (ext4's jbd2 among them): sixteen flushes one
// after the other cost sixteen device round trips, sixteen in flight a few.
// A window of one has nothing to overlap, and flushes on the caller's
// goroutine.
type windowFlusher struct {
	runner   *group.Group
	verdicts []error
}

// newWindowFlusher returns the flusher of a window of files temporaries.
func newWindowFlusher(files int) windowFlusher {
	flusher := windowFlusher{verdicts: make([]error, files)}
	//: more than one flush to overlap: a group, bounded by the window.
	if files > 1 {
		flusher.runner, _ = group.New(context.Background(), files)
	}
	//: ready for the window's first temporary.
	return flusher
}

// start flushes and closes the temporary file at index, on a goroutine of
// its own unless the window is of one.
func (f windowFlusher) start(index int, file tempFile) {
	//: a window of one: here, at once.
	if f.runner == nil {
		f.verdicts[index] = syncAndClose(file)
		//: flushed.
		return
	}
	f.runner.Go(func(context.Context) error {
		f.verdicts[index] = syncAndClose(file)
		//: the group keeps the first to fail; its slot names which file.
		return f.verdicts[index]
	})
}

// wait returns once every flush started has, re-raising a panic one of them
// raised after the others ([group.Group.Wait]), and reports a flush that
// failed — which, the verdicts say; nil when every one succeeded. The join
// is the happens-before edge that makes every verdict visible to the caller.
func (f windowFlusher) wait() error {
	//: a window of one flushed in start.
	if f.runner == nil {
		//: its verdict, or nil for an empty window.
		return cmp.Or(f.verdicts...)
	}
	//: the first flush to fail, in time; nil when none did.
	return f.runner.Wait()
}

// discardAll removes the temporaries of temps — the one of the file a
// window stopped at and every later one, all closed — and returns cause as
// PublishFailed for name, the first removal that failed kept beneath it, as
// [discard] does for one.
func discardAll(ops atomicOps, temps []tempHandle, name string, cause error) error {
	var removeErr error
	//: every temporary, whatever an earlier removal answered.
	for _, temp := range temps {
		removeErr = cmp.Or(removeErr, ops.remove(temp.name))
	}
	//: a removal that failed is still reported, just not instead of cause.
	return failPublish(cmp.Or(cause, removeErr), kerrs.String("path", name))
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
	temp, createErr := createTemp(ops, name, perm)
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
	//: a failed write is closed already.
	if writeErr := writeTemp(file, data); writeErr != nil {
		//: the write's failure.
		return writeErr
	}
	//: flushed, then closed.
	return syncAndClose(file)
}

// writeTemp writes the payload into a temporary in one Write — the payload
// is a whole file, never a stream — and closes the handle when it fails, so
// the descriptor is not leaked along with the failure: the removal that
// follows does not need it open.
func writeTemp(file tempFile, data []byte) error {
	//: one Write.
	if _, writeErr := file.Write(data); writeErr != nil {
		//: the first failure, the descriptor released.
		return cmp.Or(writeErr, file.Close())
	}
	//: written, still open for its flush.
	return nil
}

// syncAndClose flushes a written temporary to the device and closes it. It
// runs BEFORE the rename, or the rename publishes a name for bytes that may
// not be on the device yet.
func syncAndClose(file tempFile) error {
	//: a flush that fails still releases the descriptor.
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

// createTemp creates a temporary beside target, exclusively, and returns it
// with its name: the ordinary name first, then a random one while the name
// tried is taken, up to tempAttempts names. Any other refusal ends it at once.
// A name equal to target is never tried: a destination may itself be named
// like a temporary, and an absent one would be created as the temporary and
// written in place, visible half-written — the publication's whole point lost.
func createTemp(ops atomicOps, target string, perm fs.FileMode) (tempHandle, error) {
	dir := path.Dir(target)
	name, err := tempCandidate(tempPath, dir, target)
	//: try names until one is created or the bound is reached.
	for attempt := 1; err == nil; attempt++ {
		file, createErr := ops.create(name, perm)
		//: created, refused for another reason, or out of names.
		if !errors.Is(createErr, fs.ErrExist) || attempt == tempAttempts {
			//: the temporary, or the last refusal.
			return tempHandle{file: file, name: name}, createErr
		}
		//: taken: a name nobody can have planted ahead.
		name, err = tempCandidate(randomTempPath, dir, target)
	}
	//: no entropy: nothing was created.
	return tempHandle{}, err
}

// tempCandidate draws a name in dir with draw, and draws again while it is
// target, compared without case: on a case-insensitive filesystem (darwin's
// APFS by default) a destination differing only by case names the same
// entry. The counter has moved on by the next draw, and a random name does
// not repeat, so the loop ends on it.
func tempCandidate(draw func(dir string) (string, error), dir, target string) (string, error) {
	name, err := draw(dir)
	//: never the destination itself, whatever its case.
	for err == nil && strings.EqualFold(name, target) {
		name, err = draw(dir)
	}
	//: a name other than target, or the entropy's failure.
	return name, err
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
