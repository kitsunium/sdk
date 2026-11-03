//go:build linux

package datagram

import (
	"io"
	"net"
	"runtime"
	"syscall"
	"unsafe"
)

// mmsghdrAlign is the alignment C gives struct mmsghdr, which it inherits from
// struct msghdr — the widest member decides, and msghdr holds the pointers.
const mmsghdrAlign uintptr = unsafe.Alignof(syscall.Msghdr{})

// mmsghdrStride is the distance the kernel advances between array elements:
// struct msghdr, then the uint32 length, rounded up to mmsghdrAlign.
const mmsghdrStride uintptr = (unsafe.Sizeof(syscall.Msghdr{}) +
	unsafe.Sizeof(uint32(0)) + mmsghdrAlign - 1) / mmsghdrAlign * mmsghdrAlign

// The Go layout must equal the kernel stride, and the length must sit
// immediately after the header — asserted as constants, so a mismatch is a
// compile error on every architecture the tree is cross-built for, as
// internal/service/net/server asserts its own copy of the same layout.
const (
	_ uintptr = unsafe.Sizeof(mmsghdr{}) - mmsghdrStride
	_ uintptr = mmsghdrStride - unsafe.Sizeof(mmsghdr{})
	_ uintptr = unsafe.Offsetof(mmsghdr{}.length) - unsafe.Sizeof(syscall.Msghdr{})
	_ uintptr = unsafe.Sizeof(syscall.Msghdr{}) - unsafe.Offsetof(mmsghdr{}.length)
)

// mmsghdr mirrors struct mmsghdr from <sys/socket.h>:
//
//	struct mmsghdr {
//	    struct msghdr msg_hdr;
//	    unsigned int  msg_len;
//	};
//
// golang.org/x/sys would supply it, banned SDK-wide (ADR 0016 / ADR 0018), so
// the layout is declared here. No explicit padding: Go pads the trailing
// uint32 out exactly as C does on 64-bit and leaves it flush on 32-bit, which
// the assertions above hold.
type mmsghdr struct {
	// hdr is the message: one iovec, no name — the socket is connected.
	hdr syscall.Msghdr
	// length is what the kernel sent of the message, written back.
	length uint32
}

// mmsgBatch sends a backlog with sendmmsg(2) on a connected socket, up to
// MaxBatch datagrams a call, each datagram one iovec over the caller's bytes.
// Its headers and iovecs are allocated once, so a send allocates nothing.
type mmsgBatch struct {
	// raw exposes the descriptor under the runtime's netpoller.
	raw syscall.RawConn
	// call is sendmmsg; a test swaps it to answer what a kernel would.
	call func(fd uintptr, headers []mmsghdr) (sent int, errno syscall.Errno)
	// headers and iovecs describe one call's datagrams, in lockstep.
	headers [MaxBatch]mmsghdr
	iovecs  [MaxBatch]syscall.Iovec
	// pending is the headers of the call in flight; sent and errno are what
	// it answered. write reads and sets them, so the poller callback is one
	// method value bound once rather than a closure allocated per call.
	pending []mmsghdr
	sent    int
	errno   syscall.Errno
	// write is m.writePending, bound at construction.
	write func(fd uintptr) bool
}

// newBatch returns the sendmmsg(2) path for a conn that exposes its
// descriptor, nil for one that does not.
func newBatch(conn net.Conn) func(payloads [][]byte) (sent int, err error) {
	m := newMmsgBatch(conn)
	//: no descriptor: the portable loop.
	if m == nil {
		return nil
	}
	//: the batched path, bound once.
	return m.send
}

// newMmsgBatch returns the sendmmsg(2) state for a conn that exposes its
// descriptor, nil for one that does not.
func newMmsgBatch(conn net.Conn) *mmsgBatch {
	sc, ok := conn.(syscall.Conn)
	//: a test double or a wrapper: no descriptor to hand the kernel.
	if !ok {
		//: the portable loop.
		return nil
	}
	raw, err := sc.SyscallConn()
	//: a conn that refuses its descriptor takes the portable loop too.
	if err != nil {
		//: the portable loop.
		return nil
	}
	m := &mmsgBatch{raw: raw, call: sendmmsg}
	m.write = m.writePending
	//: the batched path.
	return m
}

// send sends payloads MaxBatch at a time, resuming after a short count from
// the first datagram the kernel did not send.
func (m *mmsgBatch) send(payloads [][]byte) (sent int, err error) {
	//: until every datagram left, or one was refused.
	for sent < len(payloads) {
		n, cerr := m.sendChunk(payloads[sent:min(len(payloads), sent+MaxBatch)])
		sent += n
		//: the refusal ends the batch; what left before it is counted.
		if cerr != nil {
			return sent, cerr
		}
	}
	//: every one.
	return sent, nil
}

// sendChunk sends at most MaxBatch datagrams in one sendmmsg(2), waiting on
// the poller while the socket buffer is full, and reports how many left.
func (m *mmsgBatch) sendChunk(chunk [][]byte) (sent int, err error) {
	//: one header and one iovec per datagram, over the caller's bytes.
	for i, p := range chunk {
		m.iovecs[i] = syscall.Iovec{}
		//: an empty payload is an empty datagram: no base to point at.
		if len(p) > 0 {
			m.iovecs[i].Base = &p[0]
		}
		setKernelLen(&m.iovecs[i].Len, len(p))
		m.headers[i] = mmsghdr{}
		m.headers[i].hdr.Iov = &m.iovecs[i]
		setKernelLen(&m.headers[i].hdr.Iovlen, 1)
	}
	m.pending = m.headers[:len(chunk)]
	werr := m.raw.Write(m.write)
	sent, errno := m.sent, m.errno
	//: the caller's bytes stay reachable until the kernel read them, and no
	//: pointer to them outlives the call.
	runtime.KeepAlive(chunk)
	clear(m.iovecs[:len(chunk)])
	m.pending = nil
	//: the descriptor was closed or the poller refused.
	if werr != nil {
		return 0, werr
	}
	//: the first datagram was refused.
	if errno != 0 {
		return 0, errno
	}
	//: a kernel that sends nothing without an error would spin the caller.
	if sent == 0 {
		return 0, io.ErrShortWrite
	}
	//: how many left, perhaps fewer than asked.
	return sent, nil
}

// writePending is the poller callback: one sendmmsg(2) of the pending
// headers, its answer kept. A full socket buffer (EAGAIN) asks the poller to
// wait for room; anything else is final.
func (m *mmsgBatch) writePending(fd uintptr) bool {
	m.sent, m.errno = m.call(fd, m.pending)
	//: a full socket buffer waits on the poller; anything else is final.
	return m.errno != syscall.EAGAIN
}

// sendmmsg is the sendmmsg(2) the batch drives on a connected socket — no
// datagram names a peer: how many of headers left, or the errno of the
// first that did not.
func sendmmsg(fd uintptr, headers []mmsghdr) (sent int, errno syscall.Errno) {
	result, _, callErrno := syscall.Syscall6(sysSendmmsg, fd,
		uintptr(unsafe.Pointer(&headers[0])), uintptr(len(headers)), 0, 0, 0)
	//: the first message failed: nothing left.
	if callErrno != 0 {
		//: its errno.
		return 0, callErrno
	}
	//: the count the kernel sent, which may be fewer than asked.
	return int(result), 0
}

// setKernelLen stores a length into a kernel ABI field whose width the
// architecture decides — Iovec.Len and Msghdr.Iovlen are uint64 on a 64-bit
// Linux and uint32 on a 32-bit one; the standard library's syscall package
// offers no setter for Iovlen, so one generic store serves every cell.
func setKernelLen[T ~uint32 | ~uint64](dst *T, n int) {
	*dst = T(n)
}
