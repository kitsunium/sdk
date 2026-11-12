//go:build linux

// Package server — the shards of an address asked with port 0 (#1331): every
// engine that shards — the goroutine engine's streams, the datagram groups
// (#977), a loop group's per-loop listeners (#856) — binds its first socket,
// reads the port the kernel drew, and binds the others on that port, so State's
// one row names the address every shard listens on.
package server

import (
	"bufio"
	"context"
	stdnet "net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// kernelSocketsOn counts the sockets the kernel lists on port in state
// (0A a listening TCP socket, 07 a bound UDP one) in /proc/net's tables of
// both families: the count no view of the server's own can flatter.
func kernelSocketsOn(t *testing.T, proto string, port int, state string) int {
	t.Helper()
	want := strings.ToUpper(strconv.FormatInt(int64(port), 16))
	for len(want) < 4 {
		want = "0" + want
	}
	n := 0
	for _, table := range []string{"/proc/net/" + proto, "/proc/net/" + proto + "6"} {
		f, err := os.Open(table)
		if err != nil {
			//: a kernel without IPv6 has no second table.
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 4 || fields[3] != state {
				continue
			}
			if _, p, ok := strings.Cut(fields[1], ":"); ok && p == want {
				n++
			}
		}
		_ = f.Close()
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
				port := portOf(t, reported)
				if n := kernelSocketsOn(t, p.proto, port, p.state); n != p.want {
					t.Fatalf("the kernel lists %d sockets on port %d, want %d — every shard on the reported port", n, port, p.want)
				}
			})
		}
	}
}
