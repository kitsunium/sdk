//go:build linux

package server

import (
	"cmp"
	stdnet "net"
	"sync"
	"syscall"
	"unsafe"
)

const (
	// in6PktinfoBytes is the size of struct in6_pktinfo, the larger of the two
	// destination control messages (include/uapi/linux/ipv6.h).
	in6PktinfoBytes int = 20
	// inPktinfoBytes is the size of struct in_pktinfo (include/linux/in.h).
	inPktinfoBytes int = 12
	// pinControlBytes is one sendmsg's room for the control message.
	pinControlBytes int = 64
)

// enableDestination asks the socket to name each datagram's destination, when
// source is the batched reader and the socket is bound to a wildcard address
// (#972): the kernel then writes IP_PKTINFO or IPV6_PKTINFO beside every message.
func enableDestination(source datagramSource, pc stdnet.PacketConn) (asked, on bool, why string) {
	udp, ok := pc.LocalAddr().(*stdnet.UDPAddr)
	//: only a UDP socket bound to no address in particular has a destination
	//: worth asking for; one bound to an address IS that address.
	if !ok || udp.IP == nil || !udp.IP.IsUnspecified() {
		//: not asked.
		return false, false, ""
	}
	reader, isBatched := source.(*multiReader)
	//: read one datagram a call: no control message to read.
	if !isBatched {
		//: asked, and unavailable.
		return true, false, destinationUnavailable
	}
	//: the kernel's answer.
	if err := reader.enablePktinfo(udp); err != nil {
		//: asked, and refused.
		return true, false, "setsockopt IP_PKTINFO: " + err.Error()
	}
	//: on.
	return true, true, ""
}

// enablePktinfo switches the socket's destination control message on, for its
// family: IP_PKTINFO on an IPv4 socket, IPV6_RECVPKTINFO on an IPv6 one, which
// names an IPv4-mapped destination for a mapped peer. It runs before the first
// read, so the control room is sized for it.
func (r *multiReader) enablePktinfo(bound *stdnet.UDPAddr) error {
	family, level, option := syscall.AF_INET, syscall.IPPROTO_IP, syscall.IP_PKTINFO
	//: the socket's own family, whatever the address was spelled as.
	if bound.IP.To4() == nil {
		family, level, option = syscall.AF_INET6, syscall.IPPROTO_IPV6, syscall.IPV6_RECVPKTINFO
	}
	var serr error
	cerr := r.raw.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), level, option, 1)
	})
	//: the socket's failure first, then the kernel's answer.
	if err := cmp.Or(cerr, serr); err != nil {
		//: refused.
		return err
	}
	r.dstFamily = family
	//: asked: fill reads it from the next message on.
	return nil
}

// pktinfoOf returns the destination the control data names on a socket of
// family, zero when it names none.
func pktinfoOf(control []byte, family int) localAddress {
	//: the IPv4 form on an IPv4 socket.
	if family == syscall.AF_INET {
		payload := controlMessage(control, syscall.IPPROTO_IP, syscall.IP_PKTINFO, inPktinfoBytes)
		//: no destination on this message.
		if payload == nil {
			//: unknown.
			return localAddress{}
		}
		//: the message's own fields.
		return localOf4((*syscall.Inet4Pktinfo)(unsafe.Pointer(&payload[0])))
	}
	payload := controlMessage(control, syscall.IPPROTO_IPV6, syscall.IPV6_PKTINFO, in6PktinfoBytes)
	//: no destination on this message.
	if payload == nil {
		//: unknown.
		return localAddress{}
	}
	//: the message's own fields, an IPv4-mapped address for a mapped peer.
	return localOf6((*syscall.Inet6Pktinfo)(unsafe.Pointer(&payload[0])))
}

// localOf4 reads an in_pktinfo: ipi_addr is the destination the sender used, and
// ipi_spec_dst the local address the kernel would answer from, which it fills in
// for a broadcast or a multicast destination too.
func localOf4(info *syscall.Inet4Pktinfo) localAddress {
	var local localAddress
	local.toLen = stdnet.IPv4len
	copy(local.to[:], info.Addr[:])
	//: an all-zero answer names no source.
	if info.Spec_dst != [stdnet.IPv4len]byte{} {
		local.sourceLen = stdnet.IPv4len
		copy(local.source[:], info.Spec_dst[:])
	}
	//: the destination, and the source to answer from.
	return local
}

// localOf6 reads an in6_pktinfo, whose one address is both: it is a source only
// when it is a unicast address, a group or a mapped broadcast being none.
func localOf6(info *syscall.Inet6Pktinfo) localAddress {
	var local localAddress
	local.toLen = stdnet.IPv6len
	local.to = info.Addr
	ip := stdnet.IP(local.to[:])
	//: a reply cannot leave from a group, a mapped broadcast or nothing.
	if !ip.IsMulticast() && !ip.IsUnspecified() && !ip.Equal(stdnet.IPv4bcast) {
		local.sourceLen = stdnet.IPv6len
		local.source = info.Addr
	}
	//: the destination, and the source to answer from.
	return local
}

// writePktinfo writes the control message that names source on a socket of
// family into control, and returns the bytes it took. The interface index stays
// zero, so the routing table still decides the way out: pinning the interface
// would break a reply asymmetric routing sends through another, and a source
// alone answers the client that matches addresses.
func writePktinfo(control []byte, family int, source localAddress) int {
	cmsg := (*syscall.Cmsghdr)(unsafe.Pointer(&control[0]))
	data := syscall.CmsgLen(0)
	//: the socket's family decides the form, as it decided the one read.
	if family == syscall.AF_INET6 {
		cmsg.Level = syscall.IPPROTO_IPV6
		cmsg.Type = syscall.IPV6_PKTINFO
		cmsg.SetLen(syscall.CmsgLen(in6PktinfoBytes))
		info := (*syscall.Inet6Pktinfo)(unsafe.Pointer(&control[data]))
		info.Addr = source.source
		info.Ifindex = 0
		//: one in6_pktinfo.
		return syscall.CmsgSpace(in6PktinfoBytes)
	}
	cmsg.Level = syscall.IPPROTO_IP
	cmsg.Type = syscall.IP_PKTINFO
	cmsg.SetLen(syscall.CmsgLen(inPktinfoBytes))
	info := (*syscall.Inet4Pktinfo)(unsafe.Pointer(&control[data]))
	info.Ifindex = 0
	copy(info.Spec_dst[:], source.source[:stdnet.IPv4len])
	info.Addr = [stdnet.IPv4len]byte{}
	//: one in_pktinfo.
	return syscall.CmsgSpace(inPktinfoBytes)
}

// pinWriter sends one reply from a named source with a one-message sendmmsg(2),
// the unbatched path's (sendmsg has no syscall number of its own on linux/386):
// its message, its name and its function are built once, so a reply allocates
// nothing, and a mutex guards them, a handler that kept its Packet replying from
// another goroutine.
type pinWriter struct {
	// pc is the socket, written directly when the peer has no sockaddr here.
	pc stdnet.PacketConn
	// raw exposes the descriptor under the runtime's netpoller.
	raw syscall.RawConn
	// family is the socket's.
	family int
	// mu guards everything below.
	mu sync.Mutex
	// name and namelen address the peer, iov carries the bytes, oob the
	// control message, and headers the one message sendmmsg takes.
	name    syscall.RawSockaddrInet6
	namelen uint32
	iov     syscall.Iovec
	oob     [pinControlBytes]byte
	headers [1]mmsghdr
	// sent and errno are what sendmmsg returned, the bytes of the message;
	// write is the function the descriptor runs, bound once.
	sent  int
	errno syscall.Errno
	write func(fd uintptr) bool
}

// newPinWriter builds a writer over pc, or nil when the descriptor cannot be
// reached or is of no family this writer addresses: a reply then leaves as
// before.
func newPinWriter(pc stdnet.PacketConn, sc rawConnProvider) *pinWriter {
	raw, err := sc.SyscallConn()
	//: without the descriptor there is nothing to sendmsg on.
	if err != nil {
		//: WriteTo serves.
		return nil
	}
	family := socketFamily(raw)
	//: only the two IP families name a source.
	if family != syscall.AF_INET && family != syscall.AF_INET6 {
		//: WriteTo serves.
		return nil
	}
	w := &pinWriter{pc: pc, raw: raw, family: family}
	w.write = func(fd uintptr) bool {
		var sent int
		sent, w.errno = sendmmsg(fd, w.headers[:1])
		w.sent = 0
		//: the one message left: its bytes.
		if sent == 1 {
			w.sent = int(w.headers[0].length)
		}
		//: a full send buffer waits for room, as WriteTo would.
		return w.errno != syscall.EAGAIN
	}
	//: ready for the read loop's first reply.
	return w
}

// socketFamily returns the socket's family, or zero when unreadable.
func socketFamily(raw syscall.RawConn) int {
	family := 0
	cerr := raw.Control(func(fd uintptr) {
		sa, err := syscall.Getsockname(int(fd))
		//: the family or nothing.
		if err != nil {
			return
		}
		switch sa.(type) {
		//: IPv4.
		case *syscall.SockaddrInet4:
			family = syscall.AF_INET
		//: IPv6, IPv4-mapped peers included.
		case *syscall.SockaddrInet6:
			family = syscall.AF_INET6
		}
	})
	//: a closed socket has no family to report.
	if cerr != nil {
		//: unreadable.
		return 0
	}
	//: AF_INET, AF_INET6 or zero.
	return family
}

// send writes b to to, leaving from source. A peer this writer cannot address, a
// zoned one included, and a source the kernel refuses (EINVAL: a broadcast
// address on a dual-stack socket), leave by WriteTo, from the kernel's choice.
func (w *pinWriter) send(b []byte, to stdnet.Addr, source localAddress) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	//: only an unzoned UDP peer of the socket's family has a sockaddr here.
	if !encodeSockaddr(w.family, to, &w.name, &w.namelen) {
		//: the write's own answer.
		return w.pc.WriteTo(b, to)
	}
	w.layout(b, source)
	w.sent, w.errno = 0, 0
	cerr := w.raw.Write(w.write)
	w.iov.Base = nil
	//: the socket's failure first: closed, typically.
	if cerr != nil {
		//: as a write reports it.
		return 0, cerr
	}
	//: the kernel will not send from this source.
	if w.errno == syscall.EINVAL {
		//: the write's own answer.
		return w.pc.WriteTo(b, to)
	}
	//: any other refusal, as a write reports it.
	if w.errno != 0 {
		//: reported to the handler.
		return 0, w.errno
	}
	//: how much left.
	return w.sent, nil
}

// layout fills msg for b and source.
func (w *pinWriter) layout(b []byte, source localAddress) {
	used := writePktinfo(w.oob[:], w.family, source)
	var empty byte
	base := &empty
	//: an empty reply still needs a valid base.
	if len(b) > 0 {
		base = &b[0]
	}
	w.iov.Base = base
	setKernelLen(&w.iov.Len, len(b))
	hdr := &w.headers[0].hdr
	hdr.Name = (*byte)(unsafe.Pointer(&w.name))
	hdr.Namelen = w.namelen
	hdr.Iov = &w.iov
	hdr.Control = &w.oob[0]
	hdr.Flags = 0
	setKernelLen(&hdr.Iovlen, 1)
	setKernelLen(&hdr.Controllen, used)
}
