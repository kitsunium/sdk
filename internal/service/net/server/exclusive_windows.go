//go:build windows

package server

import (
	"errors"
	"io"
	stdnet "net"
	"syscall"
	"time"
	"unsafe"
)

const (
	// soExclusiveAddrUse is SO_EXCLUSIVEADDRUSE, ((int)(~SO_REUSEADDR)) in
	// winsock2.h; the syscall package does not name it.
	soExclusiveAddrUse int = ^syscall.SO_REUSEADDR
	// wsaeAddrInUse is WSAEADDRINUSE: the address is bound, or still held by
	// the connections of a socket that bound it exclusively.
	wsaeAddrInUse syscall.Errno = 10048
	// rebindAttempts bounds the binds of one address: a restarted server waits
	// for the previous process's connections to end, never for ever.
	rebindAttempts int = 8
	// closeLinger bounds the read to zero of a graceful close: a peer that
	// never sends its FIN costs this long, then a plain close.
	closeLinger time.Duration = 500 * time.Millisecond
	// closeDrainMax bounds the bytes a graceful close reads and discards.
	closeDrainMax int64 = 64 << 10
)

// exclusiveAddrSupported reports that the platform has SO_EXCLUSIVEADDRUSE.
func exclusiveAddrSupported() bool {
	//: every Windows the SDK runs on.
	return true
}

// setExclusiveAddr sets SO_EXCLUSIVEADDRUSE on fd, before it is bound
// (Microsoft's "Using SO_REUSEADDR and SO_EXCLUSIVEADDRUSE"): no other socket,
// SO_REUSEADDR included, can then bind its address.
func setExclusiveAddr(fd uintptr) error {
	//: TRUE, before bind.
	return syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, soExclusiveAddrUse, 1)
}

// exclusiveAddrOf reads SO_EXCLUSIVEADDRUSE back from fd.
func exclusiveAddrOf(fd uintptr) (bool, error) {
	var v int32
	err := syscall.Getsockopt(syscall.Handle(fd), syscall.SOL_SOCKET, int32(soExclusiveAddrUse), (*byte)(unsafe.Pointer(&v)), new(int32(unsafe.Sizeof(v))))
	//: set when non-zero.
	return v != 0, err
}

// addrInUse reports whether err is a bind refused because the address is
// taken: the one refusal a rebind waits out.
func addrInUse(err error) bool {
	//: WSAEADDRINUSE, wrapped by the net package.
	return errors.Is(err, wsaeAddrInUse)
}

// closeConn closes c gracefully: shutdown(SD_SEND), then a read until the
// peer's zero, bounded by closeLinger and closeDrainMax, then the close. A
// socket closed this way is no longer active once its peer has answered, so
// the address it was accepted on can be bound again at once — the port reuse
// a closesocket with unread data or unacknowledged output would hold up
// (Microsoft's "Using SO_REUSEADDR and SO_EXCLUSIVEADDRUSE").
func closeConn(c stdnet.Conn) error {
	half, ok := c.(interface{ CloseWrite() error })
	//: a socket without a write half to shut, or one already closed.
	if !ok || half.CloseWrite() != nil {
		//: closed as it is.
		return c.Close()
	}
	//: the deadline bounds a peer that never answers.
	if c.SetReadDeadline(time.Now().Add(closeLinger)) == nil {
		_, err := io.Copy(io.Discard, io.LimitReader(c, closeDrainMax))
		//: the peer's zero, a timeout or a reset: the close follows either way.
		swallowErr(err)
	}
	//: the descriptor goes.
	return c.Close()
}
