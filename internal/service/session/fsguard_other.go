//go:build !(linux || darwin || freebsd || openbsd || netbsd || dragonfly)

// Package session — the honest refusal on platforms without the two mechanics
// the file store requires (ADR 0018 §(a): a uniform typed sentinel where a
// platform has no native mechanic, never a silent drop and never a build
// break).
//
// # Why Windows is refused rather than approximated
//
// The file store's first guarantee is that a session record is unreadable by
// any account but the one that wrote it. On Windows, Go's os.Chmod maps a
// FileMode to the read-only attribute and nothing else: 0600 does not describe
// an ACL, and a file created in a directory with an inheritable permissive DACL
// is readable by whoever that DACL admits.
//
// # The reason this file used to give is no longer the reason
//
// It said the right DACL needs CreateFileW with a security descriptor "which
// stdlib syscall does not expose". That stopped being the obstacle with ADR
// 0081 and ADR 0084: internal/service/lock binds LockFileEx from kernel32 and
// GetNamedSecurityInfoW and GetAce from advapi32 through syscall.NewLazyDLL,
// with no new dependency, and the same mechanism reaches every advapi32
// export. What is still missing is three things, and none of them is a reuse
// of that code:
//
//   - An owner-only DACL, BUILT and then VERIFIED. lock's reader
//     (GrantsAnyone, ADR 0084/0086/0095) builds nothing, and the one question
//     it answers — does an identifier meaning ANYBODY, Everyone, Authenticated
//     Users or BUILTIN\Users, hold a right — is weaker than this store's rule:
//     0700 and 0600 exclude every other account, a named colleague included.
//     A directory granting read to one named principal passes the reader and
//     fails the Unix rule, so reusing it would ship a weaker guarantee under
//     the same name. Applying a protected owner-only DACL at creation is new
//     ABI (SetNamedSecurityInfoW, or a SECURITY_ATTRIBUTES on the create),
//     with its own tests.
//   - A directory flush. Every rename and unlink here is followed by an fsync
//     of the directory, which is what makes Destroy a revocation a power cut
//     cannot undo. Windows has no equivalent — FlushFileBuffers on a directory
//     handle returns ERROR_ACCESS_DENIED (ADR 0056 D10) — which is why
//     internal/service/vfs refuses Windows as well.
//   - A lane that runs it. No Windows job runs this package, and ADR 0018's
//     runtime bar is not cleared by a green cross-compile.
//
// The lock half is the one piece that exists: LockFileEx (ADR 0081) could
// serialise this store's read-modify-write. It is not enough on its own.
//
// Approximating the rest — calling os.Chmod(0600), observing no error, skipping
// the directory flush, and reporting success — would produce exactly the store
// this domain refuses to be: one that makes a security claim it cannot keep,
// on the platform where nobody would think to check. So the constructor
// returns the typed proc.UnsupportedPlatform and the caller chooses a memory
// store, an external store, or another host.
package session

import (
	"io/fs"
	"os"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// unsupportedPlatform is what [plantable] reports having read where it cannot
// read anything.
const unsupportedPlatform string = "unsupported-platform"

// platformNative reports that this GOOS lacks at least one of the two mechanics
// the file store requires. NewFileStore reads it and refuses at CONSTRUCTION,
// so the refusal arrives where the program is wired rather than at the first
// login.
const platformNative bool = false

// tryLockExclusive reports the shared UnsupportedPlatform sentinel. It is
// unreachable in practice — the constructor refuses first — and exists so the
// package compiles on every GOOS, which is ADR 0018's build bar.
func tryLockExclusive(_ *os.File) (taken bool, err error) {
	//: the same typed answer every unimplemented primitive gives, everywhere.
	return false, coreproc.UnsupportedPlatform
}

// unlockFile reports the shared UnsupportedPlatform sentinel, as
// [tryLockExclusive] does and for the same reason.
func unlockFile(_ *os.File) error {
	//: uniform contract even where the capability is absent.
	return coreproc.UnsupportedPlatform
}

// plantable answers "plantable" for every directory. It is unreachable — the
// constructor refuses this platform before it audits a path — and it fails
// CLOSED rather than guessing, because on Windows os.Stat synthesises 0777 for
// every writable directory: a mode says nothing there, and the question has an
// answer only in a DACL this file does not read.
func plantable(_ fs.FileMode) (yes bool, observed string) {
	//: the refusal is the one answer that cannot be wrong in the direction
	//: that matters.
	return true, unsupportedPlatform
}
