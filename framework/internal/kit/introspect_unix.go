//go:build unix

package kit

import (
	"os"
	"syscall"
)

// sourceOpen is how the source endpoint opens a file (readSeen). On Unix,
// os.Root opens every name with O_NOFOLLOW already and, meeting a link,
// reads it and follows it inside the root: a caller's O_NOFOLLOW changes
// nothing (go1.27.1, os/root_unix.go), so what readSeen refuses is the
// handle that is not the file it saw. O_NONBLOCK keeps a named pipe put at
// the name from holding the open until a writer comes; a regular file reads
// the same with it.
const sourceOpen int = os.O_RDONLY | syscall.O_NONBLOCK
