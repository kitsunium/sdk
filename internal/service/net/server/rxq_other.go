//go:build !linux

package server

import "sync/atomic"

// enableKernelDrops reports that SO_RXQ_OVFL is Linux's: no other platform
// counts the datagrams its receive queue dropped on the message that follows.
func enableKernelDrops(datagramSource, *atomic.Uint64) (on bool, why string) {
	return false, "SO_RXQ_OVFL is Linux's"
}
