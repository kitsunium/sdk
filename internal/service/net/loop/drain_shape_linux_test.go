//go:build linux

// Package loop — the drain cases' shape on Linux (conformance_drain_native_test.go).
package loop

import "syscall"

// drainNetworks are the listeners the drain is pinned on here: TCP, Unix, and
// Shards(0)'s per-loop TCP listeners.
var drainNetworks = []string{"tcp", "unix", networkShards}

// drainQueueCap bounds the queued connections of the drain cases: Linux's
// lanes raise the listen backlog to what the cases queue.
const drainQueueCap int = 1 << 20

// streamSocket is a non-blocking, close-on-exec stream socket of family.
func streamSocket(family int) (int, error) {
	return syscall.Socket(family, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, 0)
}
