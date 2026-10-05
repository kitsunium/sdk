// Package vfs — the in-memory filesystem, and the read half of its contract.
//
// Package vfs — an open directory in the in-memory filesystem.
//
// Package vfs — an open regular file in the in-memory filesystem.
//
// Package vfs — the fs.FileInfo the in-memory filesystem reports.
//
// Package vfs — one entry in the in-memory tree.
//
// Package vfs — the in-memory filesystem's write half.
//
// Package vfs — the disk-backed filesystem: its constructor, its read half,
// and the guards every write goes through.
//
// Package vfs — atomic publication: the five mechanics, the order they run in,
// and the cleanup that makes a failure indistinguishable from never having
// started.
//
// Package vfs — the disk filesystem's non-atomic write verbs.
//
// Package vfs — the honest refusal on platforms without the mechanics the disk
// filesystem requires (ADR 0018 §(a): a uniform typed sentinel where a platform
// has no native mechanic, never a silent drop and never a build break).
//
// # Why Windows is refused rather than approximated
//
// Two of this domain's three promises cannot be kept there, and the third is
// weaker than it looks.
//
// Flushing a directory has no equivalent. FlushFileBuffers on a directory
// handle returns ERROR_ACCESS_DENIED, so after a crash a published name and
// the bytes it names can disagree — which is the precise failure atomic
// publication exists to prevent. There is no API that orders the two, so this
// is not a matter of writing more code.
//
// A permission mode is not an ACL. Go's os package maps an fs.FileMode to the
// read-only attribute and nothing else, so 0600 does not exclude any account:
// a file created in a directory carrying an inheritable permissive DACL is
// readable by whoever that DACL admits. Honouring the mode would mean building
// a security descriptor through CreateFileW, which stdlib syscall does not
// expose — the same wall internal/service/security/session hit for the same reason.
//
// And MoveFileEx with MOVEFILE_REPLACE_EXISTING, the closest thing to
// rename(2), fails outright when the destination is open by another process
// without FILE_SHARE_DELETE. A publisher whose swap fails because a reader is
// reading is not the primitive this domain promises.
//
// Approximating all three — calling Chmod, observing no error, skipping the
// directory flush, and reporting success — would produce exactly the filesystem
// this domain exists not to be: one that makes a durability and a permission
// claim it cannot keep, on the platform where nobody would think to check. So
// the constructor returns the typed proc.UnsupportedPlatform and the caller
// chooses NewMem, an external store, or another host.
//
// The gap is real and has a known closure — a Windows backend built on
// CreateFileW + SetFileSecurity + MoveFileEx, with the directory-flush step
// documented as absent rather than faked — and it is a separate change with its
// own ADR. An untested implementation of a durability boundary is worth less
// than an honest refusal.
//
// Package vfs — the operating-system mechanics the disk filesystem needs, on
// the platforms that have them.
//
// Two of the three guarantees are portable and one is not. Confinement comes
// from os.Root, which the standard library implements on every GOOS. Atomic
// replacement comes from rename(2), which POSIX requires to be atomic. Flushing
// a DIRECTORY — the step that makes a published name survive a power loss — is
// the one that is not: it is fsync(2) on a directory descriptor here, and has
// no equivalent on Windows. That is the reason this file has a build tag and a
// sibling that refuses.
//
// Package vfs implements the filesystem domain declared in internal/core/data/vfs
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
