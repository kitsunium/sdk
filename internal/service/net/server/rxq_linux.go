//go:build linux

package server

import "sync/atomic"

// enableKernelDrops switches SO_RXQ_OVFL on for source when it is the batched
// reader, which alone parses control messages, and says why when it cannot.
func enableKernelDrops(source datagramSource, drops *atomic.Uint64) (on bool, why string) {
	reader, ok := source.(*multiReader)
	//: a socket read one datagram a call has no control message to read.
	if !ok {
		return false, "the socket is read one datagram a call"
	}
	//: the kernel's answer.
	if err := reader.enableRxqOvfl(drops); err != nil {
		return false, "setsockopt SO_RXQ_OVFL: " + err.Error()
	}
	return true, ""
}
