//go:build linux

// Package rotfile — pins openFlags to include O_NOFOLLOW on Linux so a symlink
// planted between the Lstat check and OpenFile causes the open to fail with
// ELOOP rather than silently follow the link. See CWE-59. This applies to every
// reopen after a rotation, not just the first open.
package rotfile

import (
	"os"
	"syscall"
)

// openFlags is the OpenFile flag set used for the active file at construction
// and on every reopen. O_NOFOLLOW closes the TOCTOU window between the Lstat
// symlink check and the open.
const openFlags int = os.O_APPEND | os.O_CREATE | os.O_WRONLY | syscall.O_NOFOLLOW
