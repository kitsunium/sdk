//go:build linux

// Package file: open_flags_linux.go pins openFlags to include O_NOFOLLOW
// on Linux so a symlink planted between Lstat and OpenFile causes open
// to fail with ELOOP rather than silently follow the link. See CWE-59.
package file

import (
	"os"
	"syscall"
)

// openFlags is the OpenFile flag set used by fileSink.New.
// The Linux-specific O_NOFOLLOW closes the TOCTOU window between the Lstat
// symlink check in New and the actual open call. Non-Linux builds fall
// back to the generic bitmask in open_flags_other.go; they still carry
// the Lstat pre-check, just without kernel-level reinforcement.
const openFlags int = os.O_APPEND | os.O_CREATE | os.O_WRONLY | syscall.O_NOFOLLOW
