//go:build !linux

package service

// monotonicMicros is 0 off Linux: the shell reads no CLOCK_MONOTONIC there —
// FreeBSD's socket hears a MONOTONIC_USEC=, unread —, and the order of the
// messages decides a reload.
func monotonicMicros() uint64 {
	//: unknown.
	return 0
}
