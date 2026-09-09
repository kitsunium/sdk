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
// is readable by whoever that DACL admits. Building the right DACL means
// CreateFileW with a SECURITY_ATTRIBUTES carrying a security descriptor, which
// stdlib syscall does not expose and golang.org/x/sys does not either at the
// level internal/service is allowed to depend on.
//
// Approximating it — calling os.Chmod(0600), observing no error, and reporting
// success — would produce exactly the store this domain refuses to be: one that
// makes a security claim it cannot keep, on the platform where nobody would
// think to check. So the constructor returns the typed proc.UnsupportedPlatform
// and the caller chooses a memory store, an external store, or another host.
//
// The gap is real and has a known closure — see internal/service/session's
// CLAUDE.md §Platform matrix — but an untested implementation of a security
// boundary is worth less than an honest refusal.
package session

import (
	"os"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// platformNative reports that this GOOS lacks at least one of the two mechanics
// the file store requires. NewFileStore reads it and refuses at CONSTRUCTION,
// so the refusal arrives where the program is wired rather than at the first
// login.
const platformNative bool = false

// lockExclusive reports the shared UnsupportedPlatform sentinel. It is
// unreachable in practice — the constructor refuses first — and exists so the
// package compiles on every GOOS, which is ADR 0018's build bar.
func lockExclusive(_ *os.File) error {
	//: the same typed answer every unimplemented primitive gives, everywhere.
	return coreproc.UnsupportedPlatform
}

// unlockFile reports the shared UnsupportedPlatform sentinel, as
// [lockExclusive] does and for the same reason.
func unlockFile(_ *os.File) error {
	//: uniform contract even where the capability is absent.
	return coreproc.UnsupportedPlatform
}
