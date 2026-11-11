//go:build !unix && !windows

package server

// setBuffers does nothing: a platform with neither Unix sockets nor Winsock has
// no buffer to size, and State reports zero.
func setBuffers(uintptr, int, int) error { return nil }

// readBuffers is unknown.
func readBuffers(uintptr) (receive, send int) { return 0, 0 }
