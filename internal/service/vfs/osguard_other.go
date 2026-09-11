//go:build !(linux || darwin || freebsd || openbsd || netbsd || dragonfly)

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
// expose — the same wall internal/service/session hit for the same reason.
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
package vfs

import (
	"os"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// platformNative reports that this GOOS lacks at least one mechanic the disk
// filesystem requires. NewOS reads it and refuses at CONSTRUCTION, so the
// refusal arrives where the program is wired rather than at the first publish.
const platformNative bool = false

// syncDirHandle reports the shared UnsupportedPlatform sentinel. It is
// unreachable in practice — the constructor refuses first — and exists so the
// package compiles on every GOOS, which is ADR 0018's build bar.
func syncDirHandle(_ *os.File) error {
	//: the same typed answer every unimplemented primitive gives, everywhere.
	return coreproc.UnsupportedPlatform
}
