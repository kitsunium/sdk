//go:build !unix

// Package rotfile — the fallback openFlags for the platforms whose syscall
// package has no O_NOFOLLOW at all: windows, plan9, js/wasm — and wasip1,
// which has the constant but not a kernel behind it (see open_flags_unix.go
// for why it is here rather than there).
//
// On these platforms the symlink refusal is refuseSymlink's os.Lstat and
// NOTHING ELSE, so the window between that check and the OpenFile one line
// later is open. That is a real difference in protection and it is written
// down in the package CLAUDE.md's platform table rather than smoothed over:
// a table claiming uniform protection would be worse than no table.
//
// Closing it here needs a different primitive, not a different flag — Windows
// has FILE_FLAG_OPEN_REPARSE_POINT, which OPENS the link and requires the
// handle to be rejected afterwards, the opposite shape from a failing open
// (ADR 0082 §D2 measured both for internal/service/lock). That is a separate
// piece of work and is named as one; this file does not pretend to be it.
package rotfile

import "os"

// openFlags is the OpenFile flag set used for the active file on platforms
// with no O_NOFOLLOW. refuseSymlink's os.Lstat is the whole of the symlink
// policy here; the kernel is asked nothing and enforces nothing.
const openFlags int = os.O_APPEND | os.O_CREATE | os.O_WRONLY
