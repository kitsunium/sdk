//go:build !unix

// Package sdlisten — non-Unix stub. Socket activation relies on file-descriptor
// inheritance, which is a Unix mechanism, so every entry point returns the typed
// UnsupportedPlatform sentinel and an empty result rather than acting.
package sdlisten

import (
	"net"
	"os"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// Files returns no sockets and UnsupportedPlatform off Unix.
func Files(_ bool) ([]*os.File, error) {
	//: fd inheritance does not exist here; report the typed platform error.
	return nil, coreproc.UnsupportedPlatform
}

// Listeners returns no listeners and UnsupportedPlatform off Unix.
func Listeners(_ bool) ([]net.Listener, error) {
	//: no inherited sockets to wrap off Unix.
	return nil, coreproc.UnsupportedPlatform
}

// WithNames returns no grouping and UnsupportedPlatform off Unix.
func WithNames(_ bool) (map[string][]*os.File, error) {
	//: no named sockets to recover off Unix.
	return nil, coreproc.UnsupportedPlatform
}

// Prepare cannot pass sockets off Unix and returns UnsupportedPlatform.
func Prepare(_ *coreproc.Spec, _ map[string]net.Listener) error {
	//: an activator cannot hand off fds where inheritance is unavailable.
	return coreproc.UnsupportedPlatform
}
