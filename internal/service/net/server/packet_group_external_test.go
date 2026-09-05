// Package server_test — the datagram group's declaration surface.
package server_test

import (
	"context"
	stdnet "net"
	"slices"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// upperPacketHandler answers each datagram with its payload uppercased, as a
// named type, for Handle.
type upperPacketHandler struct{}

// ServePacket implements corenet.PacketHandler.
func (upperPacketHandler) ServePacket(_ context.Context, p corenet.Packet) error {
	out := make([]byte, len(p.Data()))
	for i, b := range p.Data() {
		out[i] = upper(b)
	}
	_, err := p.Reply(out)
	return err
}

// TestPacketGroup_Name pins the group's identity on the datagram side, which is
// the dimension every log line, metric and State row is tagged with.
func TestPacketGroup_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// declared is the name the caller gives the group.
		declared string
	}
	tests := []tc{
		{name: "a simple name", declared: "dns"},
		{name: "a path-like name", declared: "internal/sip"},
		{name: "an empty name", declared: ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		seen := make(chan string, 1)
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		group := srv.PacketGroup(c.declared, server.Listen("udp", "127.0.0.1:0"))
		group.HandleFunc(func(_ context.Context, p corenet.Packet) error {
			seen <- p.Group()
			return nil
		})

		if group.Name() != c.declared {
			t.Fatalf("Name = %q, want %q", group.Name(), c.declared)
		}
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		if got := srv.State().Listeners[0].Group; got != c.declared {
			t.Errorf("State reports group %q, want %q", got, c.declared)
		}
		conn, err := stdnet.DialTimeout("udp", srv.State().Listeners[0].Address, 2*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer closeOrFail(t, conn)
		if _, werr := conn.Write([]byte("x")); werr != nil {
			t.Fatalf("write: %v", werr)
		}
		select {
		case got := <-seen:
			if got != c.declared {
				t.Fatalf("the handler saw group %q, want %q", got, c.declared)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the datagram was never served")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestPacketGroup_Handle pins that the LAST handler wins, and that the datagram
// side is wired exactly like the stream one — which is the whole point of the
// domain.
func TestPacketGroup_Handle(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// replacements is how many times the handler is replaced first.
		replacements int
	}
	tests := []tc{
		{name: "one handler"},
		{name: "a replaced handler", replacements: 1},
		{name: "several replacements", replacements: 3},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		group := srv.PacketGroup("dns", server.Listen("udp", "127.0.0.1:0"))
		for range c.replacements {
			group.Handle(corenet.PacketHandlerFunc(func(context.Context, corenet.Packet) error {
				return nil
			}))
		}

		chained := group.Handle(upperPacketHandler{})

		if chained != group {
			t.Fatal("Handle did not return the group for chaining")
		}
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		addr := srv.State().Listeners[0].Address
		if got := sendDatagram(t, addr, "hello"); got != "HELLO" {
			t.Fatalf("reply = %q, want %q — an earlier handler is still installed", got, "HELLO")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestPacketGroup_HandleFunc is the end-to-end proof that the datagram engine
// serves: a real socket, a real send, and a real reply through Packet.Reply —
// which is what lets a handler answer without ever seeing the socket.
func TestPacketGroup_HandleFunc(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// network and unixSocket select the datagram family.
		network    string
		unixSocket bool
		// payload is what the peer sends.
		payload string
		// want is the reply it must receive.
		want string
	}
	tests := []tc{
		{name: "a UDP round trip", network: "udp", payload: "hello", want: "HELLO"},
		{name: "a payload already uppercase", network: "udp", payload: "HELLO", want: "HELLO"},
		//: the same engine, the same declaration, a different family.
		{name: "a unix datagram round trip", network: "unixgram", unixSocket: true, payload: "unix", want: "UNIX"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		addr := "127.0.0.1:0"
		if c.unixSocket {
			addr = t.TempDir() + "/dgram.sock"
		}
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		group := srv.PacketGroup("dns", server.Listen(c.network, addr))

		chained := group.HandleFunc(func(_ context.Context, p corenet.Packet) error {
			out := make([]byte, len(p.Data()))
			for i, b := range p.Data() {
				out[i] = upper(b)
			}
			_, err := p.Reply(out)
			return err
		})

		if chained != group {
			t.Fatal("HandleFunc did not return the group for chaining")
		}
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		bound := srv.State().Listeners[0].Address
		//: a unixgram reply needs a bound return path, so the peer binds one.
		if c.unixSocket {
			local := t.TempDir() + "/peer.sock"
			conn, err := stdnet.DialUnix("unixgram",
				&stdnet.UnixAddr{Name: local, Net: "unixgram"},
				&stdnet.UnixAddr{Name: bound, Net: "unixgram"})
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer closeOrFail(t, conn)
			if _, werr := conn.Write([]byte(c.payload)); werr != nil {
				t.Fatalf("write: %v", werr)
			}
			if derr := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); derr != nil {
				t.Fatalf("set deadline: %v", derr)
			}
			buf := make([]byte, 64)
			n, rerr := conn.Read(buf)
			if rerr != nil {
				t.Fatalf("read: %v", rerr)
			}
			if string(buf[:n]) != c.want {
				t.Fatalf("reply = %q, want %q", buf[:n], c.want)
			}
			return
		}
		if got := sendDatagram(t, bound, c.payload); got != c.want {
			t.Fatalf("reply = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestPacketGroup_HandleFunc_SenderIsReported pins that the read path decodes
// the sender correctly.
//
// Without it Reply would write to a wrong or nil address, which is the failure
// mode a raw-syscall reader is most likely to introduce — and on a
// connectionless socket nothing downstream would notice a reply going to the
// wrong peer.
func TestPacketGroup_HandleFunc_SenderIsReported(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// payload is what the peer sends.
		payload string
	}
	tests := []tc{
		{name: "a one-byte datagram", payload: "x"},
		{name: "a longer datagram", payload: "a rather longer payload"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		addrs := make(chan string, 4)
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		srv.PacketGroup("sender", server.Listen("udp", "127.0.0.1:0")).
			HandleFunc(func(_ context.Context, p corenet.Packet) error {
				//: a nil sender would panic here rather than silently misroute.
				addrs <- p.From().String()
				return nil
			})
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}

		conn, err := stdnet.DialTimeout("udp", srv.State().Listeners[0].Address, 2*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer closeOrFail(t, conn)
		if _, werr := conn.Write([]byte(c.payload)); werr != nil {
			t.Fatalf("write: %v", werr)
		}

		select {
		case got := <-addrs:
			if got != conn.LocalAddr().String() {
				t.Fatalf("sender = %q, want %q", got, conn.LocalAddr().String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the datagram was never served")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestPacketGroup_HandleFunc_ContainsAPanic pins containment on the datagram
// side.
//
// On the stream side a panicking handler costs one connection. Here there is one
// read loop for every peer of the socket, so the blast radius of the same
// mistake is the entire service — which is why the recover lives in the engine
// rather than being left to the handler.
func TestPacketGroup_HandleFunc_ContainsAPanic(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// payloads are sent in order; "boom" makes the handler panic.
		payloads []string
	}
	tests := []tc{
		{name: "a panic then a good datagram", payloads: []string{"boom", "survivor"}},
		{name: "several panics then a good datagram", payloads: []string{"boom", "boom", "survivor"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		served := make(chan string, 8)
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		srv.PacketGroup("fragile", server.Listen("udp", "127.0.0.1:0")).
			HandleFunc(func(_ context.Context, p corenet.Packet) error {
				payload := string(p.Data())
				if payload == "boom" {
					panic("handler exploded")
				}
				served <- payload
				return nil
			})
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}

		conn, err := stdnet.DialTimeout("udp", srv.State().Listeners[0].Address, 2*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer closeOrFail(t, conn)
		for _, payload := range c.payloads {
			if _, werr := conn.Write([]byte(payload)); werr != nil {
				t.Fatalf("write %q: %v", payload, werr)
			}
		}

		select {
		case got := <-served:
			if got != "survivor" {
				t.Fatalf("served %q, want \"survivor\"", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the read loop died with the panicking handler")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestPacketGroup_Use pins the middleware order on the datagram side, and that
// the chain runs per datagram rather than per socket.
func TestPacketGroup_Use(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// labels name the middlewares, in declaration order.
		labels []string
		// datagrams is how many are sent through the chain.
		datagrams int
	}
	tests := []tc{
		{name: "no middleware at all", datagrams: 1},
		{name: "one middleware", labels: []string{"a"}, datagrams: 1},
		{name: "several middlewares", labels: []string{"outer", "middle", "inner"}, datagrams: 1},
		//: the chain is composed once but runs per datagram.
		{name: "several datagrams", labels: []string{"outer", "inner"}, datagrams: 3},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		steps := make(chan string, (len(c.labels)+1)*c.datagrams)
		srv := server.New()
		t.Cleanup(func() { closeOrFail(t, srv) })
		group := srv.PacketGroup("dns", server.Listen("udp", "127.0.0.1:0"))
		group.HandleFunc(func(context.Context, corenet.Packet) error {
			steps <- "handler"
			return nil
		})
		middlewares := make([]corenet.Middleware[corenet.PacketHandler], 0, len(c.labels))
		for _, label := range c.labels {
			middlewares = append(middlewares, func(next corenet.PacketHandler) corenet.PacketHandler {
				return corenet.PacketHandlerFunc(func(ctx context.Context, p corenet.Packet) error {
					steps <- label
					return next.ServePacket(ctx, p)
				})
			})
		}

		chained := group.Use(middlewares...)

		if chained != group {
			t.Fatal("Use did not return the group for chaining")
		}
		if err := srv.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		conn, err := stdnet.DialTimeout("udp", srv.State().Listeners[0].Address, 2*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer closeOrFail(t, conn)

		want := append(slices.Clone(c.labels), "handler")
		for d := range c.datagrams {
			if _, werr := conn.Write([]byte("x")); werr != nil {
				t.Fatalf("write %d: %v", d, werr)
			}
			for i, expected := range want {
				select {
				case got := <-steps:
					//: outermost first, exactly as the declaration reads.
					if got != expected {
						t.Fatalf("datagram %d step %d ran %q, want %q", d, i, got, expected)
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("datagram %d stopped after %d of %d steps", d, i, len(want))
				}
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
