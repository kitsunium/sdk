//go:build linux

// Package server — kernel sockaddr decoding for the batched reader.
package server

import (
	stdnet "net"
	"syscall"
	"unsafe"
)

const (
	// portByteShift converts the network-byte-order port the kernel reports
	// into host order. sockaddr_in stores it big-endian regardless of platform.
	portByteShift uint16 = 8
	// portLowByteMask isolates the low octet during that conversion.
	portLowByteMask uint16 = 0xFF
)

// sockaddrToAddr converts a kernel sockaddr into a net.Addr.
//
// recvmmsg reports the sender as a raw sockaddr, so the batched path has to do
// what ReadFrom does internally. Only the families the datagram engine binds are
// decoded; anything else yields nil rather than a wrong address, because a
// handler replying to a misdecoded address is worse than one that sees none.
func sockaddrToAddr(raw *syscall.RawSockaddrAny) stdnet.Addr {
	//: decode only the families the datagram engine binds.
	switch raw.Addr.Family {
	//: IPv4 sender.
	case syscall.AF_INET:
		//: decode the sender for this family.
		return inet4Addr(raw)
	//: IPv6 sender.
	case syscall.AF_INET6:
		//: decode the sender for this family.
		return inet6Addr(raw)
	//: a Unix datagram peer, which may legitimately be unnamed.
	case syscall.AF_UNIX:
		//: decode the sender for this family.
		return unixAddr(raw)
	//: an unexpected family is reported as no address at all.
	default:
		//: an unexpected family yields no address; a misdecoded one would be
		//: worse, since a handler would reply to the wrong peer.
		return nil
	}
}

// inet4Addr decodes an IPv4 sockaddr.
func inet4Addr(raw *syscall.RawSockaddrAny) stdnet.Addr {
	sa := (*syscall.RawSockaddrInet4)(unsafe.Pointer(raw))
	//: copy rather than index, so the octet positions stay implicit.
	ip := make(stdnet.IP, stdnet.IPv4len)
	copy(ip, sa.Addr[:])
	//: a unixgram socket never reports an IP family, so this is always UDP.
	return &stdnet.UDPAddr{IP: ip, Port: hostPort(sa.Port)}
}

// inet6Addr decodes an IPv6 sockaddr.
func inet6Addr(raw *syscall.RawSockaddrAny) stdnet.Addr {
	sa := (*syscall.RawSockaddrInet6)(unsafe.Pointer(raw))
	ip := make(stdnet.IP, stdnet.IPv6len)
	copy(ip, sa.Addr[:])
	//: the scope identifies the interface for a link-local peer, and dropping
	//: it would make a reply unroutable.
	zone, _ := zoneOf(sa.Scope_id)
	//: an unresolved scope yields an empty zone, which is the correct default.
	return &stdnet.UDPAddr{IP: ip, Port: hostPort(sa.Port), Zone: zone}
}

// unixAddr decodes a Unix-domain sockaddr.
func unixAddr(raw *syscall.RawSockaddrAny) stdnet.Addr {
	sa := (*syscall.RawSockaddrUnix)(unsafe.Pointer(raw))
	//: an unnamed peer — the common case for an unbound datagram client —
	//: reports an empty path rather than a bogus one.
	if sa.Path[0] == 0 {
		//: an unnamed peer is normal for an unbound datagram client.
		return &stdnet.UnixAddr{Net: "unixgram"}
	}
	buf := make([]byte, 0, len(sa.Path))
	//: the path is NUL-terminated within a fixed-size array.
	//: the path is NUL-terminated inside a fixed-size array.
	for _, c := range sa.Path {
		//: stop at the terminator rather than copying the whole array.
		if c == 0 {
			break
		}
		buf = append(buf, byte(c))
	}
	//: a named peer can be replied to by path.
	return &stdnet.UnixAddr{Name: string(buf), Net: "unixgram"}
}

// hostPort converts a network-byte-order port to host order.
func hostPort(port uint16) int {
	//: sockaddr_in stores the port big-endian on every platform.
	return int(port>>portByteShift) | int(port&portLowByteMask)<<portByteShift
}

// zoneOf resolves an IPv6 scope identifier to an interface name.
//
// It reports whether a zone was found rather than leaning on the empty string,
// because "no zone" and "an index we could not resolve" are different facts even
// though both end up as an empty net.UDPAddr.Zone.
func zoneOf(scope uint32) (zone string, ok bool) {
	//: scope zero means "no zone", which is the overwhelming majority.
	if scope == 0 {
		//: no scope at all — the overwhelming majority of datagrams.
		return "", false
	}
	iface, err := stdnet.InterfaceByIndex(int(scope))
	//: an unresolvable index yields no zone rather than a fabricated name.
	if err != nil {
		//: an index we cannot resolve is reported as no zone, never guessed.
		return "", false
	}
	//: the interface name is what a caller needs to reply to a link-local peer.
	return iface.Name, true
}
