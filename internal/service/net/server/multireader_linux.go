//go:build linux

// Package server — the recvmmsg batched datagram reader.
package server

import (
	stdnet "net"
	"syscall"
	"unsafe"
)

// multiReader reads several datagrams per syscall using recvmmsg(2).
//
// It reuses one message array for the life of the socket: the iovecs and name
// buffers are wired to the slot buffers once, so a read costs no allocation at
// all — which is the entire reason for the batched path.
type multiReader struct {
	// pc is the socket, kept for the fallback path and for addresses.
	pc stdnet.PacketConn
	// raw exposes the descriptor under the runtime's netpoller.
	raw syscall.RawConn
	// headers is the reusable mmsghdr array handed to the kernel.
	headers []mmsghdr
	// iovecs points each header at its slot's buffer.
	iovecs []syscall.Iovec
	// names receives each datagram's sender address.
	names []syscall.RawSockaddrAny
	// wired records the slot count the arrays were built for.
	wired int
}

// newMultiReader builds a batched reader over pc.
func newMultiReader(pc stdnet.PacketConn, sc rawConnProvider) (reader *multiReader, err error) {
	raw, rerr := sc.SyscallConn()
	//: without the raw descriptor there is nothing to batch on.
	if rerr != nil {
		//: without the descriptor there is nothing to batch on.
		return nil, rerr
	}
	//: the arrays are sized lazily, on the first read, when the slot count is known.
	return &multiReader{pc: pc, raw: raw}, nil
}

// wire points the message array at the caller's slots. It runs once per slot
// count, not once per read.
func (r *multiReader) wire(slots []datagram) {
	//: already wired for this batch size — nothing to redo.
	if r.wired == len(slots) {
		//: already wired for this batch size.
		return
	}
	count := len(slots)
	//: all three are indexed in lockstep with slots, never appended to.
	r.headers = make([]mmsghdr, count)
	r.iovecs = make([]syscall.Iovec, count)
	r.names = make([]syscall.RawSockaddrAny, count)
	//: each header borrows its slot's buffer for the socket's whole lifetime.
	for i := range slots {
		r.iovecs[i].Base = &slots[i].buf[0]
		r.iovecs[i].Len = uint64(len(slots[i].buf))
		r.headers[i].hdr.Name = (*byte)(unsafe.Pointer(&r.names[i]))
		r.headers[i].hdr.Namelen = syscall.SizeofSockaddrAny
		r.headers[i].hdr.Iov = &r.iovecs[i]
		r.headers[i].hdr.Iovlen = 1
	}
	r.wired = count
}

// readBatch implements datagramSource using one recvmmsg call.
func (r *multiReader) readBatch(slots []datagram) (n int, err error) {
	r.wire(slots)
	var received int
	var errno syscall.Errno
	//: driving the syscall inside RawConn.Read is what keeps the goroutine
	//: parked on the netpoller: returning false on EAGAIN hands control back to
	//: the runtime exactly as a blocking ReadFrom would.
	ctrlErr := r.raw.Read(func(fd uintptr) bool {
		result, _, callErrno := syscall.Syscall6(syscall.SYS_RECVMMSG, fd,
			uintptr(unsafe.Pointer(&r.headers[0])), uintptr(len(r.headers)),
			syscall.MSG_WAITFORONE, 0, 0)
		//: not ready yet — let the netpoller wake us when it is.
		if callErrno == syscall.EAGAIN {
			//: not ready — hand control back to the netpoller.
			return false
		}
		received, errno = int(result), callErrno
		//: the syscall completed, successfully or not.
		return true
	})
	//: the socket was closed, or the runtime refused the operation.
	if ctrlErr != nil {
		//: the socket was closed, or the runtime refused the operation.
		return 0, ctrlErr
	}
	//: a syscall error is reported as itself so the loop can classify it.
	if errno != 0 {
		//: report the raw errno so the loop can classify it.
		return 0, errno
	}
	r.fill(slots, received)
	//: every filled slot is ready for the handler.
	return received, nil
}

// fill copies the per-datagram results back into the caller's slots.
func (r *multiReader) fill(slots []datagram, received int) {
	//: only the slots the kernel actually filled carry meaningful data.
	for i := range received {
		slots[i].n = int(r.headers[i].length)
		slots[i].addr = sockaddrToAddr(&r.names[i])
	}
}
