// Package session — the small filesystem helpers behind atomic publication.
package session

import (
	"cmp"
	"errors"
	"io/fs"
	"os"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	coresession "github.com/kitsunium/sdk/internal/core/session"
)

// tempRecord is the narrow view of a freshly created record file that the
// publication path uses. `*os.File` satisfies it; nothing else in the SDK does.
//
// It is an interface rather than the concrete handle for one reason worth
// stating: it makes the three helpers below testable against a double that
// FAILS. A `chmod` that reports success without honouring the mode, a `Sync`
// that errors, a `Close` on an already-closed descriptor — those are the paths
// the store's guarantees rest on, and they cannot be provoked on a real file
// without a hostile filesystem.
type tempRecord interface {
	// Chmod narrows the file to a mode.
	Chmod(mode fs.FileMode) error
	// Stat reports the mode the filesystem actually applied.
	Stat() (fs.FileInfo, error)
	// Write appends the sealed payload.
	Write(payload []byte) (int, error)
	// Sync flushes it to the device.
	Sync() error
	// Close releases the descriptor.
	Close() error
	// Name reports the path, which is what gets renamed or removed.
	Name() string
}

// assertPrivateFile narrows a freshly created file to owner-only and then
// CHECKS that the narrowing took.
//
// Both halves are load-bearing. os.CreateTemp asks for 0600, and a parent
// carrying a default POSIX ACL can hand back something wider, so the chmod is
// what makes the request true on an ordinary Linux box. And on a filesystem
// that does not implement Unix permissions at all — an exFAT stick, an SMB
// share, a container mount with blanket file_mode= options — BOTH calls report
// success and the file stays world-readable, which is what the stat catches.
// Without it the store would be writing session contents to a world-readable
// file while reporting success: precisely the "store that pretends" this domain
// exists not to be.
func assertPrivateFile(file tempRecord) error {
	//: the file was created by this call, so narrowing it surprises nobody.
	if chmodErr := file.Chmod(fileMode); chmodErr != nil {
		//: StoreUnavailable.
		return wrapAs(coresession.StoreUnavailable, chmodErr, kerrs.String("op", "chmod-temp"))
	}
	info, statErr := file.Stat()
	//: a file that cannot be stat'ed cannot be vouched for.
	if statErr != nil {
		//: a backend fault.
		return wrapAs(coresession.StoreUnavailable, statErr, kerrs.String("op", "stat-temp"))
	}
	//: any bit outside the owner triad is a refusal.
	if info.Mode().Perm()&^fileMode != 0 {
		//: DirectoryUnsafe covers the whole "this location cannot hold a
		//: session record safely" family; the field says which half failed.
		return wrapAs(DirectoryUnsafe, nil,
			kerrs.String("subject", "record"), kerrs.String("want", fileMode.String()))
	}
	//: owner-only.
	return nil
}

// writeAndSync writes payload, flushes it to the device, and closes the file.
func writeAndSync(file tempRecord, payload []byte) error {
	//: one Write; the payload is a single sealed box, never a stream.
	if _, writeErr := file.Write(payload); writeErr != nil {
		//: StoreUnavailable.
		return wrapAs(coresession.StoreUnavailable, writeErr, kerrs.String("op", "write"))
	}
	//: sync BEFORE the rename. An atomic rename over data still sitting in the
	//: page cache is atomic about nothing once the machine loses power.
	if syncErr := file.Sync(); syncErr != nil {
		//: StoreUnavailable.
		return wrapAs(coresession.StoreUnavailable, syncErr, kerrs.String("op", "sync"))
	}
	//: close last, so a close error is not mistaken for a write error.
	if closeErr := file.Close(); closeErr != nil {
		//: StoreUnavailable.
		return wrapAs(coresession.StoreUnavailable, closeErr, kerrs.String("op", "close"))
	}
	//: on disk, not yet published.
	return nil
}

// removeTemp deletes an abandoned temporary record and returns the failure that
// caused it to be abandoned. The previous record — if there is one — is
// untouched, which is the whole point: a failed write must never replace a good
// record with an empty one.
//
// The removal is CHECKED rather than discarded, and then deliberately
// subordinate to cause: replacing "the disk is full" with "the orphan would not
// unlink" would report the consequence instead of the problem.
func removeTemp(name string, cause error) error {
	//: an already-absent file is success; nothing was left behind either way.
	if removeErr := os.Remove(name); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
		//: a real removal failure, still subordinate to cause.
		return firstFailure(cause, removeErr, "remove-temp")
	}
	//: the original failure, unchanged.
	return cause
}

// firstFailure keeps cause when there is one, and promotes fallback otherwise.
func firstFailure(cause, fallback error, op string) error {
	//: cmp.Or returns the first non-zero argument, which is exactly the rule:
	//: the caller's own failure wins, and the cleanup failure is the answer only
	//: when there was nothing better to report. wrapAs is total, so the second
	//: argument is never nil and the result never is either.
	return cmp.Or(cause, wrapAs(coresession.StoreUnavailable, fallback, kerrs.String("op", op)))
}
