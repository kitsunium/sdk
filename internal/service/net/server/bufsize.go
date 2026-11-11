package server

import (
	"syscall"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// bufferControl sets the socket buffers a group asked for, before bind
// (#973): SO_RCVBUF and SO_SNDBUF, for each that is above zero. A listener's
// accepted sockets, and a datagram socket's own, then start with them; the
// kernel doubles a value and clamps it to rmem_max and wmem_max, which State
// reads back and reports (socketBuffers), never as a fault.
func bufferControl(spec socketSpec, rawConn syscall.RawConn) error {
	//: neither asked: the kernel's default stands.
	if spec.receiveBuffer <= 0 && spec.sendBuffer <= 0 {
		return nil
	}
	var setErr error
	//: the descriptor, before bind.
	if err := rawConn.Control(func(fd uintptr) { setErr = setBuffers(fd, spec.receiveBuffer, spec.sendBuffer) }); err != nil {
		//: the descriptor could not be reached.
		return err
	}
	//: a buffer the kernel refused outright: the bind fails with it.
	return setErr
}

// socketBuffers reads a bound socket's buffers back, in bytes, as the kernel
// holds them; zero for what the platform cannot say.
func socketBuffers(sc syscall.Conn) (receive, send int) {
	//: a socket with no descriptor has nothing to read.
	if sc == nil {
		return 0, 0
	}
	raw, err := sc.SyscallConn()
	//: unreadable counts as unknown.
	if err != nil {
		return 0, 0
	}
	//: read, never written.
	if cerr := raw.Control(func(fd uintptr) { receive, send = readBuffers(fd) }); cerr != nil {
		return 0, 0
	}
	return receive, send
}

// bufferSpec is the part of a socketSpec a group's limits ask for.
func bufferSpec(limits corenet.LimitsValue) socketSpec {
	return socketSpec{receiveBuffer: limits.ReceiveBuffer, sendBuffer: limits.SendBuffer}
}

// adoptedBuffers applies the group's buffers to a socket it took over and reads
// back what the kernel holds. The bind that sets them on a socket of its own
// never ran here, and SO_RCVBUF and SO_SNDBUF take effect after bind too; a
// listener's next accepted sockets inherit them. A setsockopt the kernel refused
// outright is the socket's report, reason, not an adoption failure: the
// supervisor's socket still serves, on the size it had.
func adoptedBuffers(sc syscall.Conn, spec socketSpec) (receive, send int, reason string) {
	//: a socket with a descriptor, and something asked of it.
	if sc != nil && (spec.receiveBuffer > 0 || spec.sendBuffer > 0) {
		//: reported, the socket keeps the size it had.
		if err := applyBuffers(sc, spec); err != nil {
			reason = "socket buffers: " + err.Error()
		}
	}
	receive, send = socketBuffers(sc)
	return receive, send, reason
}

// applyBuffers sets the group's sizes on a socket already bound.
func applyBuffers(sc syscall.Conn, spec socketSpec) error {
	raw, err := sc.SyscallConn()
	//: the descriptor could not be reached.
	if err != nil {
		return err
	}
	return bufferControl(spec, raw)
}

// valueRefusalBuffers judges the buffers' values: a negative size is no size.
func (g *StreamGroup) valueRefusalBuffers() error {
	//: negative receive buffer.
	if g.kinds.has(corenet.OptionReceiveBuffer) && g.limits.ReceiveBuffer < 0 {
		return g.valueInvalid(corenet.OptionReceiveBuffer)
	}
	//: negative send buffer.
	if g.kinds.has(corenet.OptionSendBuffer) && g.limits.SendBuffer < 0 {
		return g.valueInvalid(corenet.OptionSendBuffer)
	}
	//: both in range, or none given.
	return nil
}
