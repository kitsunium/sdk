//go:build !windows

// Package server — how a Unix kernel reports a datagram longer than the read
// buffer: by truncating it, which is not an error at all.
package server

// datagramTruncated reports whether a read failed because the datagram did not
// fit the buffer it was read into. Off Windows no read fails for that reason:
// recvfrom truncates and returns the length it copied, and the probe byte past
// the ceiling is what tells dispatch the datagram was oversized.
func datagramTruncated(_ error) bool {
	//: truncation is a successful read here.
	return false
}
