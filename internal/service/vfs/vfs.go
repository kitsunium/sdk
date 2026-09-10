// Package vfs implements the filesystem domain declared in internal/core/vfs
// (ADR 0056): two concrete filesystems — one backed by the operating system,
// one held in memory — behind one contract, plus the atomic publication both
// of them promise.
//
// # Two implementations, one set of refusals
//
// The memory filesystem exists so a consumer can test its own code without a
// temporary directory, and that is only worth anything if the two answer
// identically. They therefore share the core guards ([corevfs.ValidateWritePath],
// [corevfs.ValidatePerm]) and the same typed sentinels, and a table-driven
// conformance suite runs the SAME cases against both. Where they cannot be
// identical — symbolic links, durability, modification times — the difference
// is named in the doc comment rather than discovered by a reader.
//
// # What the operating-system filesystem confines, and what it does not
//
// Confinement is enforced by os.Root, which resolves every name relative to a
// held directory handle (openat2 with RESOLVE_BENEATH on Linux, an
// lstat-verified walk elsewhere). A name that would leave the tree is refused
// by the KERNEL, not by a string check in this package — that is the strong
// guarantee, and this package neither reimplements it nor second-guesses it.
//
// On top of it, this package refuses one thing os.Root permits: a WRITE whose
// final component is a symbolic link. os.Root would follow it, and the target
// is guaranteed to stay inside the root, so nothing escapes — but the bytes
// would land somewhere other than the name the caller gave, which is the
// classic symlink-plant. That refusal is [corevfs.PathEscaped].
//
// It is checked with an Lstat before the operation, so it is a refusal of the
// state this package OBSERVED, not a race-free guarantee: a link planted
// between the check and the open is still followed. Stated plainly because the
// distinction matters — the race-free property is confinement to the root, and
// that one is the kernel's.
//
// # Where it refuses to run at all
//
// The operating-system filesystem needs two mechanics that are not portable: a
// rename that atomically replaces, and a directory handle that can be flushed.
// Where either is missing it returns proc.UnsupportedPlatform at CONSTRUCTION
// (ADR 0018) rather than shipping a filesystem that makes a durability claim
// it cannot keep. See osguard_other.go for which platforms and why.
package vfs

import (
	corevfs "github.com/kitsunium/sdk/internal/core/vfs"
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
// one place this domain deliberately diverges from internal/service/session.
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
		Private:  "core/vfs: open, stat or readdir failed; the *fs.PathError cause is wrapped, not replaced, so errors.Is against fs.ErrNotExist still answers",
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
		Private:  "core/vfs: create, write, mkdir or remove failed; the *fs.PathError cause is wrapped, not replaced",
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
		Private:  "core/vfs: atomic publication aborted; the previous content is intact and the temporary was removed — the operation is safe to retry",
		ExitCode: exitIOErr,
	}, fields...)
}
