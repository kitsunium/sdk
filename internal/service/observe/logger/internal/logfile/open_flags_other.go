//go:build !unix

package logfile

import "os"

// openFlags is the OpenFile flag set Open uses on platforms with no
// O_NOFOLLOW. RefuseSymlink's os.Lstat is the whole of the symlink policy here;
// the kernel is asked nothing and enforces nothing.
const openFlags int = os.O_APPEND | os.O_CREATE | os.O_WRONLY
