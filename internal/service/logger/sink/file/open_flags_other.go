//go:build !linux

// Package file: open_flags_other.go supplies openFlags for non-Linux
// builds where syscall.O_NOFOLLOW is not reliably portable. The Lstat
// pre-check in refuseSymlink still rejects symlinks; this build path
// simply omits the kernel-level TOCTOU reinforcement available on Linux.
package file

import "os"

// openFlags is the OpenFile flag set used by fileSink.New on non-Linux
// platforms. The Lstat guard in refuseSymlink provides the policy
// enforcement; kernel-level O_NOFOLLOW is added on Linux-specific builds.
const openFlags int = os.O_APPEND | os.O_CREATE | os.O_WRONLY
