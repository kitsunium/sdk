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
	"io"
	"io/fs"
	stdnet "net"
	"net/http"
	"net/netip"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
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

// socketInode is the inode of the socket under sock, read from its
// descriptor's /proc/self/fd entry: the number /proc/net prints. A socket
// that offers no descriptor fails the test, so none is left uncounted.
func socketInode(t *testing.T, sock any) string {
	t.Helper()
	sc, ok := sock.(syscall.Conn)
	//: every socket the server binds is the standard library's, which offers one.
	if !ok {
		t.Fatalf("%T offers no descriptor", sock)
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		t.Fatalf("%T: %v", sock, err)
	}
	var link string
	var lerr error
	err = rc.Control(func(fd uintptr) { link, lerr = os.Readlink("/proc/self/fd/" + strconv.FormatUint(uint64(fd), 10)) })
	inode, ok := strings.CutPrefix(link, "socket:[")
	//: a descriptor that is no socket, or one closed under the test.
	if err = errors.Join(err, lerr); err != nil || !ok {
		t.Fatalf("%T: descriptor %q: %v", sock, link, err)
	}
	return strings.TrimSuffix(inode, "]")
}

// serverSocketInodes is the inodes of the sockets srv holds: its stream
// listeners — the goroutine engine's and a loop group's — and its datagram
// shards.
func serverSocketInodes(t *testing.T, srv *Server) map[string]bool {
	t.Helper()
	srv.mu.RLock()
	defer srv.mu.RUnlock()
	inodes := make(map[string]bool, len(srv.listeners)+len(srv.packetConns))
	for _, b := range srv.listeners {
		inodes[socketInode(t, b.ln)] = true
	}
	for _, b := range srv.packetConns {
		inodes[socketInode(t, b.pc)] = true
	}
	return inodes
}

// tableReads bounds how often kernelSocketsOn reads a table again because a
// socket it holds was missing from the read: a torn read skips a row only
// when a socket was hashed or unhashed ahead of it between two read calls.
const tableReads = 50

// kernelSocketsOn counts the sockets the kernel lists in state (0A a
// listening TCP socket, 07 a bound UDP one) on addr, the address State
// reports, whose inode is in held — open sockets of proto, the server's own
// (serverSocketInodes) and any the test adds: the count no view of the
// server's own can flatter. The kernel's tables are the host's, so a socket
// of another suite on the same port number — 127.0.0.1:P while the server is
// on [::1]:P, both drawn by the kernel — is no shard of this server's
// (#1378); nor is a socket of this process's on the very address, which a
// reuseport UDP socket bound to port 0 may be
// (TestKernelSocketsOn_countsThisServersOnly).
//
// A table is not read at once: each read call starts again at the row of the
// position reached, counted from the first bucket (udp_seq_start,
// net/ipv4/udp.c), so a socket hashed or unhashed ahead of a row between two
// calls lists that row twice or not at all
// (TestKernelSocketsOn_readsATornTableOnce). An inode is therefore counted
// once, and a read missing a socket of held is read again.
func kernelSocketsOn(t *testing.T, proto, addr, state string, held map[string]bool) int {
	t.Helper()
	want := procHexAddrPort(t, addr)
	for range tableReads {
		listed, on := make(map[string]bool, len(held)), make(map[string]bool, len(held))
		for _, s := range procSockets(t, proto) {
			//: a row of another socket's.
			if !held[s.inode] {
				continue
			}
			listed[s.inode] = true
			//: in the state, on the address; a row a torn read repeats is one socket.
			if s.state == state && s.local == want {
				on[s.inode] = true
			}
		}
		//: every socket held is listed: the read skipped none of theirs.
		if len(listed) == len(held) {
			return len(on)
		}
	}
	t.Fatalf("%d reads of the %s table, each missing a socket of the %d held", tableReads, proto, len(held))
	return 0
}

// processSocketsOn counts the sockets of this process (/proc/self/fd) the
// kernel lists in state on addr, each inode once, in one read: how a socket
// still held after Close, or one beside the server's, is counted. A torn
// read may miss one of them; it cannot add one.
func processSocketsOn(t *testing.T, proto, addr, state string) int {
	t.Helper()
	want, own := procHexAddrPort(t, addr), ownSocketInodes(t)
	on := make(map[string]bool)
	for _, s := range procSockets(t, proto) {
		//: a socket of this process in the state, on the address.
		if s.state == state && s.local == want && own[s.inode] {
			on[s.inode] = true
		}
	}
	return len(on)
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
				n := kernelSocketsOn(t, p.proto, reported, p.state, serverSocketInodes(t, srv))
				//: other sockets of this process on the address are no shard,
				//: and are named for whoever reads the run (#1378).
				if all := processSocketsOn(t, p.proto, reported, p.state); all > n {
					t.Logf("%d other sockets of this process on %s, not the server's", all-n, reported)
				}
				//: every shard of the server's on the reported address, and no other.
				if n != p.want {
					t.Fatalf("the kernel lists %d sockets of the server on %s, want %d — every shard on the reported address", n, reported, p.want)
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
//
// The second case is the datagram run's fifth socket (#1378, reopened): a
// reuseport UDP socket of this process on the server's very address. Linux
// gives a socket bound to port 0 with SO_REUSEPORT a port a reuseport socket
// of the same uid already holds (net/ipv4/udp.c, udp_lib_lport_inuse: such a
// holder is left out of the bitmap of ports in use), and the newcomer joins
// its group, so another suite's socket can sit on the address the server
// reports. It is this process's, on the address: only the server's own
// descriptors tell it apart.
func TestKernelSocketsOn_countsThisServersOnly(t *testing.T) {
	type tc struct {
		name string
		// proto is the kernel's table: tcp for a stream group, udp for a
		// datagram one.
		proto string
		// shards is how many sockets the server binds on its address.
		shards int
		// strayHost is the address the stray is bound on, at the server's
		// port.
		strayHost string
	}
	cases := []tc{
		{name: "four stream shards and a stray on 127.0.0.2", proto: "tcp", shards: 4, strayHost: "127.0.0.2"},
		{name: "four datagram shards and a reuseport stray on their address", proto: "udp", shards: 4, strayHost: "127.0.0.1"},
	}
	runCase := func(t *testing.T, c tc) {
		srv := New()
		state := "0A"
		//: the server and its stray, of the case's kind.
		switch c.proto {
		//: a datagram group, and a socket that joins its SO_REUSEPORT group.
		case "udp":
			state = "07"
			srv.PacketGroup("p", Listen("udp", "127.0.0.1:0"), Shards(c.shards)).
				HandleFunc(func(context.Context, corenet.Packet) error { return nil })
		//: a stream group, and a plain listener at another address.
		default:
			srv.Group("s", Listen("tcp", "127.0.0.1:0"), Plaintext(), Shards(c.shards)).
				Handle(corenet.ConnHandlerFunc(func(context.Context, corenet.Conn) error { return nil }))
		}
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(func() { swallowErr(srv.Close()) })
		reported := srv.State().Listeners[0].Address
		port := portOf(t, reported)
		strayAddr := stdnet.JoinHostPort(c.strayHost, strconv.Itoa(port))
		stray := bindStray(t, c.proto, strayAddr)
		defer func() { _ = stray.Close() }()
		//: the stray is in the kernel's table, on the port: what #1378's count read.
		if n := kernelSocketsOn(t, c.proto, strayAddr, state, map[string]bool{socketInode(t, stray): true}); n != 1 {
			t.Fatalf("the kernel lists the stray %d times on %s, want once", n, strayAddr)
		}
		//: the server's shards alone, the stray left out.
		if n := kernelSocketsOn(t, c.proto, reported, state, serverSocketInodes(t, srv)); n != c.shards {
			t.Fatalf("kernelSocketsOn counts %d sockets on %s, want the %d shards alone", n, reported, c.shards)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestKernelSocketsOn_readsATornTableOnce holds the other reading of #1378's
// fifth socket: no socket at all, but a row read twice. /proc/net/udp is read
// in several calls, each resuming at the row of the position reached
// (udp_seq_start, net/ipv4/udp.c); a socket hashed ahead of the shards'
// rows between two calls lists one of them again, one unhashed skips one,
// and the host's other suites bind and close sockets all the time. Here a
// socket is bound and closed again and again on the server's port at
// 127.0.0.2 — the shards' own bucket, at its head —, while more and more
// holders on that port, at 127.0.0.3 on, move the shards' rows across the
// boundaries of the reads; every count must still be the four shards.
func TestKernelSocketsOn_readsATornTableOnce(t *testing.T) {
	type tc struct {
		name string
		// shards is how many sockets the datagram group binds.
		shards int
		// holders is how many sockets are added ahead of the shards' rows,
		// one at a time; reads is how many counts are taken after each.
		holders, reads int
	}
	cases := []tc{{name: "four datagram shards under churn in their bucket", shards: 4, holders: 40, reads: 25}}
	runCase := func(t *testing.T, c tc) {
		srv := New()
		srv.PacketGroup("p", Listen("udp4", "127.0.0.1:0"), Shards(c.shards)).
			HandleFunc(func(context.Context, corenet.Packet) error { return nil })
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(func() { swallowErr(srv.Close()) })
		reported := srv.State().Listeners[0].Address
		port := strconv.Itoa(portOf(t, reported))
		ours := serverSocketInodes(t, srv)
		stop := make(chan struct{})
		churned := make(chan struct{})
		go func() {
			defer close(churned)
			for {
				//: the test is over.
				select {
				case <-stop:
					return
				//: a socket hashed then unhashed ahead of the shards' rows.
				default:
				}
				//: a bind that fails changes no row, and the loop goes on.
				if pc, err := stdnet.ListenPacket("udp4", stdnet.JoinHostPort("127.0.0.2", port)); err == nil {
					_ = pc.Close()
				}
			}
		}()
		defer func() { close(stop); <-churned }()
		for i := range c.holders {
			holder, err := stdnet.ListenPacket("udp4", stdnet.JoinHostPort("127.0.0."+strconv.Itoa(3+i), port))
			//: an address the host does not route to the loopback.
			if err != nil {
				t.Skipf("no holder beside the server: %v", err)
			}
			defer func() { _ = holder.Close() }()
			for range c.reads {
				//: every read counts the shards once each, whatever the churn.
				if n := kernelSocketsOn(t, "udp", reported, "07", ours); n != c.shards {
					t.Fatalf("kernelSocketsOn counts %d sockets on %s with %d holders ahead, want the %d shards", n, reported, i+1, c.shards)
				}
			}
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// bindStray binds the stray of TestKernelSocketsOn_countsThisServersOnly on
// addr: a plain TCP listener, or a UDP socket with SO_REUSEPORT, which joins
// the server's group when addr is the server's own. The test skips when the
// host will not bind it.
func bindStray(t *testing.T, proto, addr string) io.Closer {
	t.Helper()
	var lc stdnet.ListenConfig
	var stray io.Closer
	var err error
	//: the stray of the case's kind.
	if proto == "udp" {
		lc.Control = reusePortControl
		stray, err = lc.ListenPacket(t.Context(), proto, addr)
	} else {
		stray, err = lc.Listen(t.Context(), proto, addr)
	}
	//: an address the host does not route to the loopback.
	if err != nil {
		t.Skipf("no %s socket on %s beside the server: %v", proto, addr, err)
	}
	return stray
}

// OwnListenersOn is kernelSocketsOn's count of this process's listening TCP
// sockets on addr, for the external suite (TestServer_Close, #1386).
func OwnListenersOn(t *testing.T, addr string) int {
	t.Helper()
	//: the state of a listening TCP socket.
	return processSocketsOn(t, "tcp", addr, "0A")
}
