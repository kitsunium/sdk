//go:build unix

package logfile

import (
	"os"
	"syscall"
)

// openFlags is the OpenFile flag set Open uses. O_NOFOLLOW closes the TOCTOU
// window between RefuseSymlink's os.Lstat and the open: with O_CREATE it
// refuses a DANGLING link exactly as it refuses a live one, which matters
// because the dangling one is the shape the attack takes — plant the link, let
// the victim's O_CREATE make the file.
//
// It governs the FINAL component only. A symbolic link at a PARENT component is
// still traversed; that is the caller's own directory tree, and each sink's
// "do NOT place the log file under an attacker-writable directory"
// pre-condition is what covers it.
const openFlags int = os.O_APPEND | os.O_CREATE | os.O_WRONLY | syscall.O_NOFOLLOW
