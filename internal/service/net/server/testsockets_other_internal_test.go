//go:build !unix

// Package server — no descriptor to adopt where a socket cannot become a file.
package server

import (
	"os"
	"runtime"
	"testing"
)

// listenerFile skips the case: outside Unix a socket cannot become an *os.File
// (net's File answers "not supported by windows"), so there is no descriptor a
// supervisor could pass and adoption has nothing to take.
func listenerFile(t *testing.T) *os.File {
	t.Helper()
	t.Skip("a socket cannot become an *os.File on " + runtime.GOOS + ": no descriptor to adopt")
	return nil
}

// packetFile is listenerFile for a datagram socket.
func packetFile(t *testing.T) *os.File {
	t.Helper()
	t.Skip("a socket cannot become an *os.File on " + runtime.GOOS + ": no descriptor to adopt")
	return nil
}
