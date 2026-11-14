//go:build linux

// Package server — the shards of an address asked with port 0 (#1331): every
// engine that shards — the goroutine engine's streams, the datagram groups
// (#977), a loop group's per-loop listeners (#856) — binds its first socket,
// reads the port the kernel drew, and binds the others on that port, so State's
// one row names the address every shard listens on.
package server

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	stdnet "net"
	"net/http"
	"net/netip"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// procSocket is one row of /proc/net/{tcp,udp}{,6}: the local and remote
// address as the kernel prints them (hex address, colon, hex port), the
// state, and the socket's inode (0 for a socket no descriptor holds, a
// TIME_WAIT entry).
type procSocket struct {
	local, remote, state, inode string
}

// procSockets is every row of the kernel's tables of proto, both families.
// The tables are the network namespace's: they hold the sockets of every
// process on the host — the suites go test runs beside this one (#1378) —,
// so a count of this server's must say whose and where (kernelSocketsOn).
// A table that cannot be read fails the test, so no count reads zero for
// want of it; only the IPv6 table may be missing, on a kernel without IPv6.
func procSockets(t *testing.T, proto string) []procSocket {
	t.Helper()
	var rows []procSocket
	for _, table := range []string{"/proc/net/" + proto, "/proc/net/" + proto + "6"} {
		raw, err := os.ReadFile(table)
		//: a kernel without IPv6 has no second table.
		if err != nil && strings.HasSuffix(table, "6") && errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		for line := range strings.Lines(string(raw)) {
			fields := strings.Fields(line)
			if len(fields) < 10 || fields[0] == "sl" {
				continue
			}
			rows = append(rows, procSocket{local: fields[1], remote: fields[2], state: fields[3], inode: fields[9]})
		}
	}
	return rows
}

// procHexPort is a port as /proc/net prints it.
func procHexPort(port int) string {
	return fmt.Sprintf("%04X", port)
}

// procHexAddrPort is addr ("host:port") as /proc/net prints it: each 32-bit
// word of the address in the host's byte order, then the port.
func procHexAddrPort(t *testing.T, addr string) string {
	t.Helper()
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		t.Fatalf("%q: %v", addr, err)
	}
	ip := ap.Addr().AsSlice()
	var b strings.Builder
	for i := 0; i < len(ip); i += 4 {
		fmt.Fprintf(&b, "%08X", binary.NativeEndian.Uint32(ip[i:i+4]))
	}
	return b.String() + ":" + procHexPort(int(ap.Port()))
}

// ownSocketInodes is the inodes of the sockets this process holds a
// descriptor on, read from /proc/self/fd.
func ownSocketInodes(t *testing.T) map[string]bool {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("/proc/self/fd: %v", err)
	}
	own := make(map[string]bool, len(ents))
	for _, e := range ents {
		//: a descriptor closed since the listing reads as an error, and is no socket of ours.
		link, err := os.Readlink("/proc/self/fd/" + e.Name())
		if inode, ok := strings.CutPrefix(link, "socket:["); err == nil && ok {
			own[strings.TrimSuffix(inode, "]")] = true
		}
	}
	return own
}

// kernelSocketsOn counts the sockets the kernel lists in state (0A a
// listening TCP socket, 07 a bound UDP one) on addr, the address State
// reports, held by this process: the count no view of the server's own can
// flatter. The kernel's tables are the host's, so a socket of another suite
// on the same port number — 127.0.0.1:P while the server is on [::1]:P,
// both drawn by the kernel — is no shard of this server's (#1378).
func kernelSocketsOn(t *testing.T, proto, addr, state string) int {
	t.Helper()
	want, own := procHexAddrPort(t, addr), ownSocketInodes(t)
	n := 0
	for _, s := range procSockets(t, proto) {
		if s.state == state && s.local == want && own[s.inode] {
			n++
		}
	}
	return n
}

// portOf is the port of a host:port address.
func portOf(t *testing.T, addr string) int {
	t.Helper()
	_, p, err := stdnet.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("%q: %v", addr, err)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatalf("%q: %v", addr, err)
	}
	return n
}

// shardHosts are the loopback addresses the test asks with port 0, IPv6
// where the host has it.
func shardHosts(t *testing.T) []string {
	hosts := []string{"127.0.0.1:0"}
	ln, err := stdnet.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Logf("no IPv6 loopback, [::1]:0 not run: %v", err)
		return hosts
	}
	_ = ln.Close()
	return append(hosts, "[::1]:0")
}

// TestShards_portZeroIsOnePort pins #1331: N shards of an address asked as
// host:0 are N sockets on ONE port, the one State's row reports with Shards N,
// counted in the kernel's own tables, for the goroutine engine's streams —
// with Shards(4) and with the default, one per core, an HTTP group of the
// simple API (the issue's HTTP("127.0.0.1:0", …)) —, a datagram group's
// Shards(4) and a loop group's Shards(0) of four loops, on 127.0.0.1:0 and
// [::1]:0.
func TestShards_portZeroIsOnePort(t *testing.T) {
	type path struct {
		name    string
		proto   string
		state   string
		declare func(srv *Server, addr string)
		bound   func(srv *Server) []string
		want    int
	}
	streams := func(srv *Server) []string {
		srv.mu.RLock()
		defer srv.mu.RUnlock()
		var out []string
		for _, b := range srv.listeners {
			out = append(out, b.ln.Addr().String())
		}
		return out
	}
	packets := func(srv *Server) []string {
		var out []string
		for _, b := range shardSockets(srv) {
			out = append(out, b.pc.LocalAddr().String())
		}
		return out
	}
	conns := corenet.ConnHandlerFunc(func(context.Context, corenet.Conn) error { return nil })
	paths := []path{
		{
			name: "stream/Shards(4)", proto: "tcp", state: "0A", want: 4, bound: streams,
			declare: func(srv *Server, addr string) {
				srv.Group("s", Listen("tcp", addr), Plaintext(), Shards(4)).Handle(conns)
			},
		},
		{
			name: "stream/default", proto: "tcp", state: "0A", want: runtime.GOMAXPROCS(0), bound: streams,
			declare: func(srv *Server, addr string) {
				srv.Group("s", Listen("tcp", addr), Plaintext()).Handle(conns)
			},
		},
		{
			name: "http/default", proto: "tcp", state: "0A", want: runtime.GOMAXPROCS(0), bound: streams,
			declare: func(srv *Server, addr string) {
				srv.HTTP(addr, http.NotFoundHandler(), Plaintext())
			},
		},
		{
			name: "datagram/Shards(4)", proto: "udp", state: "07", want: 4, bound: packets,
			declare: func(srv *Server, addr string) {
				srv.PacketGroup("p", Listen("udp", addr), Shards(4)).
					HandleFunc(func(context.Context, corenet.Packet) error { return nil })
			},
		},
		{
			name: "loop/Shards(0)", proto: "tcp", state: "0A", want: 4, bound: streams,
			declare: func(srv *Server, addr string) {
				srv.Group("l", Listen("tcp", addr), Plaintext(), Shards(0),
					Loop(corenet.LoopValue{Backend: corenet.LoopBackendEpoll, Loops: 4})).HandleLoop(&loopLineH{})
			},
		},
	}
	for _, host := range shardHosts(t) {
		for _, p := range paths {
			t.Run(host+"/"+p.name, func(t *testing.T) {
				srv := New()
				p.declare(srv, host)
				if err := srv.Start(t.Context()); err != nil {
					t.Fatalf("Start: %v", err)
				}
				t.Cleanup(func() { swallowErr(srv.Close()) })
				st := srv.State()
				if len(st.Listeners) != 1 || st.Listeners[0].Shards != p.want {
					t.Fatalf("Listeners = %+v, want one row of Shards %d", st.Listeners, p.want)
				}
				reported := st.Listeners[0].Address
				bound := p.bound(srv)
				if len(bound) != p.want {
					t.Fatalf("%d sockets bound, want %d", len(bound), p.want)
				}
				for i, a := range bound {
					if a != reported {
						t.Errorf("shard %d on %s, State reports %s", i, a, reported)
					}
				}
				if n := kernelSocketsOn(t, p.proto, reported, p.state); n != p.want {
					t.Fatalf("the kernel lists %d sockets of this process on %s, want %d — every shard on the reported address", n, reported, p.want)
				}
			})
		}
	}
}

// TestKernelSocketsOn_countsThisServersOnly holds #1378's remedy: a socket
// of another suite on the server's port number, at another address — the
// fifth socket the host lane listed on port 36439 while the run's server
// listened on [::1] — is in the kernel's tables, and no shard of this
// server's. The stray is this process's own, on 127.0.0.2 (Linux routes
// 127/8 to the loopback), so the address is what the count must tell apart.
func TestKernelSocketsOn_countsThisServersOnly(t *testing.T) {
	type tc struct {
		name string
		// shards is how many listeners the server binds on its address.
		shards int
		// strayHost is the address the stray listens on, at the server's
		// port.
		strayHost string
	}
	cases := []tc{{name: "four shards and a stray on 127.0.0.2", shards: 4, strayHost: "127.0.0.2"}}
	runCase := func(t *testing.T, c tc) {
		srv := New()
		srv.Group("s", Listen("tcp", "127.0.0.1:0"), Plaintext(), Shards(c.shards)).
			Handle(corenet.ConnHandlerFunc(func(context.Context, corenet.Conn) error { return nil }))
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(func() { swallowErr(srv.Close()) })
		reported := srv.State().Listeners[0].Address
		port := portOf(t, reported)
		stray, err := stdnet.Listen("tcp", stdnet.JoinHostPort(c.strayHost, strconv.Itoa(port)))
		if err != nil {
			t.Skipf("no listener on %s:%d beside the server: %v", c.strayHost, port, err)
		}
		defer func() { _ = stray.Close() }()
		onPort := 0
		for _, s := range procSockets(t, "tcp") {
			if s.state == "0A" && strings.HasSuffix(s.local, ":"+procHexPort(port)) {
				onPort++
			}
		}
		//: the stray is in the kernel's table, on the port: what #1378's count read.
		if onPort < c.shards+1 {
			t.Fatalf("the kernel lists %d listening sockets on port %d, want the %d shards and the stray", onPort, port, c.shards)
		}
		if n := kernelSocketsOn(t, "tcp", reported, "0A"); n != c.shards {
			t.Fatalf("kernelSocketsOn counts %d sockets on %s, want the %d shards alone", n, reported, c.shards)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// OwnListenersOn is kernelSocketsOn's count of this process's listening TCP
// sockets on addr, for the external suite (TestServer_Close, #1386).
func OwnListenersOn(t *testing.T, addr string) int {
	t.Helper()
	//: the state of a listening TCP socket.
	return kernelSocketsOn(t, "tcp", addr, "0A")
}
