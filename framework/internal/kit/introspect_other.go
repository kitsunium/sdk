//go:build !unix && !windows

// Package kit — how the source endpoint opens a file where neither the Unix
// nor the Windows way applies.
package kit

import "os"

// sourceOpen is how the source endpoint opens a file (readSeen). Here no
// flag keeps os.Root from following a link at the file's name: readSeen's
// check of the handle is the whole of the refusal.
const sourceOpen = os.O_RDONLY
