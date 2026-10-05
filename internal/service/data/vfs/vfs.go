package vfs

import (
	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// exitIOErr matches sysexits EX_IOERR (74). The filesystem itself failed.
const exitIOErr int = 74

// exitConfig matches sysexits EX_CONFIG (78). A root that cannot be opened is
// a wiring fault: the same constructor call will fail identically forever.
const exitConfig int = 78

// verdict returns a core sentinel as the error ORIGIN — its code, reason,
// public and private win under errs' origin-wins rule — with any extra fields
// attached.
//
// It is the shape used for the domain's OWN judgements: a path this package
// declined, a mode it refused, a kind of file it will not overwrite. Those have
// no operating-system cause to preserve, so there is nothing to wrap.
func verdict(sentinel *kerrs.Error, fields ...kerrs.FieldValue) error {
	//: origin-wins keeps the sentinel's identity; fields are diagnostic.
	return kerrs.Wrap(sentinel, kerrs.WrapParams{}, fields...)
}

// failRead wraps a filesystem cause onto the core ReadFailed sentinel by
// restating its Reason/Public/Private/ExitCode.
//
// Restating rather than passing the sentinel is the whole point, and it is the
// one place this domain deliberately diverges from internal/service/security/session.
// A session store hides the operating-system cause on purpose. A filesystem
// must NOT: errors.Is(err, fs.ErrNotExist) is the sentence every Go program
// that touches files already contains, and a filesystem that breaks it is a
// filesystem nobody can adopt incrementally. Wrapping the *fs.PathError as the
// CAUSE keeps that answer true and adds the dotted-quad code on top.
//
// TestWrapHelpersRestateTheirSentinelExactly pins the restatement to the
// sentinel, so the two cannot drift.
func failRead(cause error, fields ...kerrs.FieldValue) error {
	//: the cause stays in the chain — that is the contract.
	return kerrs.Wrap(cause, kerrs.WrapParams{
		Code:     corevfs.CodeReadFailed,
		Reason:   "READ_FAILED",
		Public:   "The filesystem could not read that path",
		Private:  "core/data/vfs: open, stat or readdir failed; the *fs.PathError cause is wrapped, not replaced, so errors.Is against fs.ErrNotExist still answers",
		ExitCode: exitIOErr,
	}, fields...)
}

// failWrite wraps a filesystem cause onto the core WriteFailed sentinel, for
// the same reason and in the same shape as [failRead].
func failWrite(cause error, fields ...kerrs.FieldValue) error {
	//: create, write, mkdir and remove all land here.
	return kerrs.Wrap(cause, kerrs.WrapParams{
		Code:     corevfs.CodeWriteFailed,
		Reason:   "WRITE_FAILED",
		Public:   "The filesystem could not write that path",
		Private:  "core/data/vfs: create, write, mkdir or remove failed; the *fs.PathError cause is wrapped, not replaced",
		ExitCode: exitIOErr,
	}, fields...)
}

// failPublish wraps a filesystem cause onto the core PublishFailed sentinel.
//
// Returning it is an ASSERTION, not a description: the caller may read it as
// "the previous bytes are still there and no temporary survives". Every path
// that produces it has already cleaned up, and publish_internal_test.go proves
// it by hashing the destination before and after a failure injected at each
// step.
func failPublish(cause error, fields ...kerrs.FieldValue) error {
	//: the operation is safe to retry and safe to abandon.
	return kerrs.Wrap(cause, kerrs.WrapParams{
		Code:     corevfs.CodePublishFailed,
		Reason:   "PUBLISH_FAILED",
		Public:   "The filesystem could not publish that file",
		Private:  "core/data/vfs: atomic publication aborted; the previous content is intact and the temporary was removed — the operation is safe to retry",
		ExitCode: exitIOErr,
	}, fields...)
}
