//go:build windows

// Package kit — how the source endpoint opens a file on Windows: the link
// itself, never its target.
package kit

import (
	"os"
	"syscall"
)

// sourceOpen is how the source endpoint opens a file (readSeen). Windows
// has no O_NOFOLLOW; os.Root hands FILE_FLAG_OPEN_REPARSE_POINT to the
// system, which then opens a link at the file's own name — any reparse
// point — instead of what it leads to, and the handle says it is no regular
// file. A link on the path is still followed inside the root, as os.Root
// does.
const sourceOpen = os.O_RDONLY | syscall.FILE_FLAG_OPEN_REPARSE_POINT
