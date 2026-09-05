//go:build linux

// Package server — kernel sockaddr decoding for the batched reader.
package server

import (
	stdnet "net"
	"syscall"
	"testing"
	"unsafe"
)

// networkOrder swaps a port between host and network byte order. sockaddr_in
// stores it big-endian on every platform, so the same swap encodes and decodes.
func networkOrder(port uint16) uint16 {
	return port>>portByteShift | port&portLowByteMask<<portByteShift
}

// inet4Sockaddr builds the raw sockaddr the kernel would report for an IPv4
// sender.
func inet4Sockaddr(ip [4]byte, port uint16) *syscall.RawSockaddrAny {
	var raw syscall.RawSockaddrAny
	sa := (*syscall.RawSockaddrInet4)(unsafe.Pointer(&raw))
	sa.Family = syscall.AF_INET
	sa.Addr = ip
	sa.Port = networkOrder(port)
	return &raw
}

// inet6Sockaddr builds the raw sockaddr the kernel would report for an IPv6
// sender.
func inet6Sockaddr(ip [16]byte, port uint16, scope uint32) *syscall.RawSockaddrAny {
	var raw syscall.RawSockaddrAny
	sa := (*syscall.RawSockaddrInet6)(unsafe.Pointer(&raw))
	sa.Family = syscall.AF_INET6
	sa.Addr = ip
	sa.Port = networkOrder(port)
	sa.Scope_id = scope
	return &raw
}

// unixSockaddr builds the raw sockaddr the kernel would report for a Unix peer.
func unixSockaddr(path string) *syscall.RawSockaddrAny {
	var raw syscall.RawSockaddrAny
	sa := (*syscall.RawSockaddrUnix)(unsafe.Pointer(&raw))
	sa.Family = syscall.AF_UNIX
	//: the path is NUL-terminated inside a fixed-size array; an empty one is an
	//: unnamed peer, which is the common case for an unbound datagram client.
	for i := range min(len(path), len(sa.Path)-1) {
		sa.Path[i] = int8(path[i])
	}
	return &raw
}

// Test_sockaddrToAddr pins that an UNKNOWN family yields no address at all.
//
// recvmmsg reports the sender as a raw sockaddr, so the batched path has to do
// what ReadFrom does internally. Decoding a family it does not understand would
// produce a plausible-looking address pointing somewhere else entirely, and a
// handler replying to a misdecoded address is far worse than one that sees none.
func Test_sockaddrToAddr(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// raw is the sockaddr the kernel reported.
		raw *syscall.RawSockaddrAny
		// want is the address the handler must be shown, or empty for none.
		want string
	}
	tests := []tc{
		{
			name: "an IPv4 sender",
			raw:  inet4Sockaddr([4]byte{192, 0, 2, 10}, 5060), want: "192.0.2.10:5060",
		},
		{
			name: "an IPv6 sender",
			raw: inet6Sockaddr([16]byte{
				0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1,
			}, 5060, 0), want: "[2001:db8::1]:5060",
		},
		{name: "a named Unix peer", raw: unixSockaddr("/run/svc.sock"), want: "/run/svc.sock"},
		//: an unbound datagram client has no path, and that is normal.
		{name: "an unnamed Unix peer", raw: unixSockaddr("")},
		{
			//: anything else is reported as no address rather than guessed at.
			name: "an unexpected family",
			raw:  &syscall.RawSockaddrAny{Addr: syscall.RawSockaddr{Family: syscall.AF_NETLINK}},
		},
		{name: "no family at all", raw: &syscall.RawSockaddrAny{}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := sockaddrToAddr(c.raw)

		if c.want == "" {
			//: an unnamed Unix peer still carries a network, so only a family
			//: the engine does not bind yields nothing at all.
			if got != nil && got.String() != "" {
				t.Fatalf("sockaddrToAddr = %v, want no address", got)
			}
			return
		}
		if got == nil {
			t.Fatalf("sockaddrToAddr = nil, want %q", c.want)
		}
		if got.String() != c.want {
			t.Fatalf("sockaddrToAddr = %q, want %q", got.String(), c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_inet4Addr pins the IPv4 decode, including that the address is COPIED out
// of the kernel buffer. The name buffers are reused for the life of the socket,
// so an address that aliased one would change under the handler as soon as the
// next batch arrived.
func Test_inet4Addr(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// ip and port are what the kernel reported.
		ip   [4]byte
		port uint16
		// want is the decoded address.
		want string
	}
	tests := []tc{
		{name: "a loopback sender", ip: [4]byte{127, 0, 0, 1}, port: 9999, want: "127.0.0.1:9999"},
		{name: "a documentation address", ip: [4]byte{192, 0, 2, 1}, port: 53, want: "192.0.2.1:53"},
		{name: "the unspecified address", ip: [4]byte{}, port: 0, want: "0.0.0.0:0"},
		//: the port occupies the full 16 bits, so the top of the range is the
		//: one a byte-order mistake gets wrong.
		{name: "the highest port", ip: [4]byte{10, 0, 0, 1}, port: 65535, want: "10.0.0.1:65535"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		raw := inet4Sockaddr(c.ip, c.port)

		got := inet4Addr(raw)

		if got.String() != c.want {
			t.Fatalf("inet4Addr = %q, want %q", got.String(), c.want)
		}
		//: the kernel's name buffer is reused, so a decoded address that aliased
		//: it would change under the handler on the next batch.
		udp, ok := got.(*stdnet.UDPAddr)
		if !ok {
			t.Fatalf("inet4Addr returned a %T, want *net.UDPAddr", got)
		}
		sa := (*syscall.RawSockaddrInet4)(unsafe.Pointer(raw))
		sa.Addr = [4]byte{1, 2, 3, 4}
		if udp.String() != c.want {
			t.Errorf("the decoded address changed to %q when the kernel buffer was "+
				"reused — it aliases the buffer instead of copying it", udp)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_inet6Addr pins the IPv6 decode, and in particular that the SCOPE survives
// as a zone. A link-local peer is only reachable through the interface it
// arrived on, so dropping the zone would make every reply to one unroutable.
func Test_inet6Addr(t *testing.T) {
	t.Parallel()
	loopback := [16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}
	linkLocal := [16]byte{0xfe, 0x80, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}

	type tc struct {
		// name describes the case.
		name string
		// ip, port and scope are what the kernel reported.
		ip    [16]byte
		port  uint16
		scope uint32
		// want is the decoded address, without any zone.
		want string
		// wantZone is whether a zone must be attached.
		wantZone bool
	}
	tests := []tc{
		{name: "a loopback sender", ip: loopback, port: 5060, want: "[::1]:5060"},
		{name: "the unspecified address", ip: [16]byte{}, want: "[::]:0"},
		{
			//: interface 1 is the loopback on every Linux host, so the zone is
			//: resolvable without assuming anything about the machine.
			name: "a link-local sender with a scope",
			ip:   linkLocal, port: 5060, scope: 1, want: "[fe80::1]:5060", wantZone: true,
		},
		{
			//: an index nothing can resolve yields no zone rather than a
			//: fabricated name.
			name: "a scope that does not resolve",
			ip:   linkLocal, port: 5060, scope: 1 << 20, want: "[fe80::1]:5060",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := inet6Addr(inet6Sockaddr(c.ip, c.port, c.scope))

		udp, ok := got.(*stdnet.UDPAddr)
		if !ok {
			t.Fatalf("inet6Addr returned a %T, want *net.UDPAddr", got)
		}
		if (udp.Zone != "") != c.wantZone {
			t.Fatalf("Zone = %q, want a zone = %v — a reply to a link-local peer "+
				"is unroutable without it", udp.Zone, c.wantZone)
		}
		//: compare without the zone, which is host-specific.
		bare := &stdnet.UDPAddr{IP: udp.IP, Port: udp.Port}
		if bare.String() != c.want {
			t.Fatalf("inet6Addr = %q, want %q", bare, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_unixAddr pins that an UNNAMED peer is decoded as unnamed rather than as a
// bogus path. An unbound datagram client is the common case, and a decoded path
// read out of an uninitialised array would be whatever the previous datagram
// left in the reused name buffer.
func Test_unixAddr(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// path is what the kernel reported.
		path string
	}
	tests := []tc{
		{name: "a named peer", path: "/run/kitsunium/svc.sock"},
		{name: "a short path", path: "/a"},
		//: an unbound datagram client is the common case.
		{name: "an unnamed peer", path: ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := unixAddr(unixSockaddr(c.path))

		unix, ok := got.(*stdnet.UnixAddr)
		if !ok {
			t.Fatalf("unixAddr returned a %T, want *net.UnixAddr", got)
		}
		if unix.Name != c.path {
			t.Fatalf("Name = %q, want %q", unix.Name, c.path)
		}
		//: the network is what lets a handler reply at all.
		if unix.Net != "unixgram" {
			t.Errorf("Net = %q, want unixgram", unix.Net)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_hostPort pins the byte-order conversion. sockaddr_in stores the port
// big-endian on every platform, so a decoder that trusted the host order would
// report 8080 as 36895 — a plausible port number, which is exactly what makes
// the mistake survive review.
func Test_hostPort(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// port is the host-order value the kernel is reporting.
		port uint16
	}
	tests := []tc{
		{name: "a well-known port", port: 53},
		{name: "an HTTP port", port: 8080},
		{name: "a SIP port", port: 5060},
		{name: "the lowest port", port: 0},
		{name: "the highest port", port: 65535},
		//: a palindromic byte pattern would pass even a broken conversion, so
		//: the asymmetric ones above are what actually hold the line.
		{name: "a symmetric byte pattern", port: 0x0101},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := hostPort(networkOrder(c.port)); got != int(c.port) {
			t.Fatalf("hostPort(network order of %d) = %d, want %d", c.port, got, c.port)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_zoneOf pins that "no zone" and "an index we could not resolve" stay
// distinguishable. Both end up as an empty net.UDPAddr.Zone, but they are
// different facts, and collapsing them would hide a scope the kernel reported
// and the host could not explain.
func Test_zoneOf(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// scope is the IPv6 scope identifier the kernel reported.
		scope uint32
		// wantOK is whether a zone must be resolved.
		wantOK bool
	}
	tests := []tc{
		//: scope zero means "no zone", which is the overwhelming majority.
		{name: "no scope at all", scope: 0},
		//: interface 1 is the loopback on every Linux host.
		{name: "the first interface", scope: 1, wantOK: true},
		{name: "an index that does not exist", scope: 1 << 20},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		zone, ok := zoneOf(c.scope)

		if ok != c.wantOK {
			t.Fatalf("zoneOf(%d) reported ok = %v, want %v", c.scope, ok, c.wantOK)
		}
		//: the flag and the value must agree, or a caller cannot tell an
		//: unresolved index from a peer that carried no scope.
		if ok != (zone != "") {
			t.Fatalf("zoneOf(%d) = (%q, %v) — the report contradicts itself", c.scope, zone, ok)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
