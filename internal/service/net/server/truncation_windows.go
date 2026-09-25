//go:build windows

// Package server — how Windows reports a datagram longer than the read buffer.
package server

import (
	"errors"
	"syscall"
)

// wsaeMsgSize is WSAEMSGSIZE (winsock2.h / winerror.h, 10040): "a message sent
// on a datagram socket was larger than the internal message buffer or some
// other network limit, or the buffer used to receive a datagram into was
// smaller than the datagram itself". The standard library declares it only in
// internal/syscall/windows, which nothing outside the standard library may
// import, so it is hand-defined and cited here — ADR 0018's discipline for an
// ABI constant, adopted because golang.org/x/sys is banned SDK-wide.
const wsaeMsgSize syscall.Errno = 10040

// datagramTruncated reports whether a read failed because the datagram did not
// fit the buffer it was read into.
//
// Every Unix kernel TRUNCATES in silence: recvfrom copies what fits and returns
// that length, so the probe byte newSlots adds past the ceiling is filled and
// dispatch counts the datagram as oversized. Windows fails the read instead,
// with WSAEMSGSIZE, and the portable reader used to hand that up as an error
// the read loop skipped — so a datagram past the ceiling was neither delivered
// nor COUNTED, and State.OversizedPackets read zero for drops that happened.
// The first run of the suite on Windows found it (TestMaxPacketSize, "far over
// the ceiling").
func datagramTruncated(err error) bool {
	//: net wraps the errno in an *OpError and an *os.SyscallError; errors.Is
	//: walks both.
	return errors.Is(err, wsaeMsgSize)
}
