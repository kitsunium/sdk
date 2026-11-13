//go:build linux

// Package server — the probe before the shards (#1330), on every engine that
// shards: the goroutine engine's streams, a datagram group's Shards (#977) and
// a loop group's per-loop listeners (#856).
package server

import (
	"context"
	stdnet "net"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// shardPath is one engine's sharded declaration on an address.
type shardPath struct {
	name, proto, state string
	want               int
	declare            func(srv *Server, addr string, extra ...GroupOption)
}

// shardPaths are the three engines that shard, each asked for three sockets.
func shardPaths() []shardPath {
	conns := corenet.ConnHandlerFunc(func(context.Context, corenet.Conn) error { return nil })
	return []shardPath{
		{
			name: "stream/Shards(3)", proto: "tcp", state: "0A", want: 3,
			declare: func(srv *Server, addr string, extra ...GroupOption) {
				srv.Group("s", append([]GroupOption{Listen("tcp", addr), Plaintext(), Shards(3)}, extra...)...).Handle(conns)
			},
		},
		{
			name: "datagram/Shards(3)", proto: "udp", state: "07", want: 3,
			declare: func(srv *Server, addr string, extra ...GroupOption) {
				srv.PacketGroup("p", append([]GroupOption{Listen("udp", addr), Shards(3)}, extra...)...).
					HandleFunc(func(context.Context, corenet.Packet) error { return nil })
			},
		},
		{
			name: "loop/Shards(0)", proto: "tcp", state: "0A", want: 3,
			declare: func(srv *Server, addr string, extra ...GroupOption) {
				srv.Group("l", append([]GroupOption{
					Listen("tcp", addr), Plaintext(), Shards(0),
					Loop(corenet.LoopValue{Backend: corenet.LoopBackendEpoll, Loops: 3}),
				}, extra...)...).HandleLoop(&loopLineH{})
			},
		},
	}
}

// holdReusePort binds proto's address 127.0.0.1:0 with SO_REUSEPORT, as
// another process's shard would, and returns the address; it is closed when
// the test ends.
func holdReusePort(t *testing.T, proto string) string {
	t.Helper()
	cfg := stdnet.ListenConfig{Control: reusePortControl}
	if proto == "udp" {
		pc, err := cfg.ListenPacket(t.Context(), "udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { swallowErr(pc.Close()) })
		return pc.LocalAddr().String()
	}
	ln, err := cfg.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { swallowErr(ln.Close()) })
	return ln.Addr().String()
}

// freePort returns a 127.0.0.1 address whose port proto had free a moment ago.
func freePort(t *testing.T, proto string) string {
	t.Helper()
	var addr string
	if proto == "udp" {
		pc, err := stdnet.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr = pc.LocalAddr().String()
		swallowErr(pc.Close())
		return addr
	}
	ln, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr = ln.Addr().String()
	swallowErr(ln.Close())
	return addr
}

// TestShards_probeRefusesAHeldPort pins #1330 on each engine: an address a
// SO_REUSEPORT socket already holds — what another process's shard is — fails
// Start with AddressInUse naming it, nothing bound, where the shards used to
// join it; with ShareAddress the shards join it on purpose and State says
// Shared.
func TestShards_probeRefusesAHeldPort(t *testing.T) {
	for _, p := range shardPaths() {
		t.Run(p.name+"/refused", func(t *testing.T) {
			held := holdReusePort(t, p.proto)
			srv := New()
			t.Cleanup(func() { swallowErr(srv.Close()) })
			p.declare(srv, held)
			err := srv.Start(t.Context())
			if !errs.HasCode(err, corenet.CodeAddressInUse) {
				t.Fatalf("Start on a held %s = %v, want ADDRESS_IN_USE", held, err)
			}
			named := false
			for _, f := range errs.FieldsOf(err) {
				named = named || (f.Key() == "address" && strings.Contains(f.StringValue(), held))
			}
			if !named {
				t.Fatalf("refusal %v does not name %s", errs.FieldsOf(err), held)
			}
			if n := len(srv.State().Listeners); n != 0 {
				t.Fatalf("%d listeners bound beside the refusal", n)
			}
		})
		t.Run(p.name+"/shared", func(t *testing.T) {
			held := holdReusePort(t, p.proto)
			srv := New()
			t.Cleanup(func() { swallowErr(srv.Close()) })
			p.declare(srv, held, ShareAddress())
			if err := srv.Start(t.Context()); err != nil {
				t.Fatalf("ShareAddress Start on a held %s = %v", held, err)
			}
			st := srv.State()
			if len(st.Listeners) != 1 || !st.Listeners[0].Shared || st.Listeners[0].Shards != p.want {
				t.Fatalf("Listeners = %+v, want one Shared row of Shards %d", st.Listeners, p.want)
			}
			//: the holder's socket and the group's three, this process's on the held address.
			if n := kernelSocketsOn(t, p.proto, held, p.state); n != p.want+1 {
				t.Fatalf("the kernel lists %d sockets of this process on %s, want %d", n, held, p.want+1)
			}
		})
	}
}

// TestShards_namedPortStillShards pins #1330's second criterion: a probe
// that finds the address free leaves the shards as they were — explicitly
// requested shards still open N sockets on one named port within one
// process, counted in the kernel's tables, on every engine. Only the probe
// knows of no other socket; a port another test drew in between is drawn
// again.
func TestShards_namedPortStillShards(t *testing.T) {
	for _, p := range shardPaths() {
		t.Run(p.name, func(t *testing.T) {
			var srv *Server
			var addr string
			for attempt := range 5 {
				addr = freePort(t, p.proto)
				srv = New()
				p.declare(srv, addr)
				err := srv.Start(t.Context())
				if err == nil {
					break
				}
				swallowErr(srv.Close())
				if !errs.HasCode(err, corenet.CodeAddressInUse) || attempt == 4 {
					t.Fatalf("Start on %s = %v", addr, err)
				}
			}
			t.Cleanup(func() { swallowErr(srv.Close()) })
			st := srv.State()
			if len(st.Listeners) != 1 || st.Listeners[0].Shards != p.want || st.Listeners[0].Shared {
				t.Fatalf("Listeners = %+v, want one unshared row of Shards %d", st.Listeners, p.want)
			}
			if st.Listeners[0].Address != addr {
				t.Fatalf("bound %s, asked %s", st.Listeners[0].Address, addr)
			}
			if n := kernelSocketsOn(t, p.proto, addr, p.state); n != p.want {
				t.Fatalf("the kernel lists %d sockets of this process on %s, want %d", n, addr, p.want)
			}
		})
	}
}
