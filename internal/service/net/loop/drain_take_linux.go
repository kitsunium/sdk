//go:build linux

package loop

import "syscall"

// takeAfter says what an accept4 failure means for the drain's take, live
// the connections the group still holds.
func takeAfter(err error, live int64) uint8 {
	//: by errno.
	switch err {
	//: out of descriptors: the drain's closes make room, unless the group has
	//: no connection left to close — nothing would ever free one.
	case syscall.EMFILE, syscall.ENFILE:
		//: none to close: over, as the portable backend gives up.
		if live == 0 {
			//: over.
			return takeOver
		}
		//: next batch.
		return takeLater
	//: one connection lost, the queue still there: the peer abandoned it, a
	//: signal, or a network error accept(2) says to retry on.
	case syscall.ECONNABORTED, syscall.EINTR, syscall.EPROTO, syscall.ENOPROTOOPT, syscall.EHOSTDOWN,
		syscall.ENONET, syscall.EHOSTUNREACH, syscall.EOPNOTSUPP, syscall.ENETUNREACH, syscall.ENETDOWN:
		//: the next one.
		return takeNext
	//: EAGAIN, the queue empty; anything else, the listener failed.
	default:
		//: over.
		return takeOver
	}
}
