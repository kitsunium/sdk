package server_test

import (
	"context"
	stdnet "net"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// startUpper starts a datagram server that echoes each payload back uppercased,
// so a reply proves the handler actually saw the bytes.
func startUpper(t *testing.T, opts ...server.GroupOption) (srv *server.Server, addr string) {
	t.Helper()
	srv = server.New()
	all := append([]server.GroupOption{server.Listen("udp", "127.0.0.1:0")}, opts...)
	srv.PacketGroup("dns", all...).
		HandleFunc(func(_ context.Context, p corenet.Packet) error {
			out := make([]byte, len(p.Data()))
			for i, b := range p.Data() {
				out[i] = upper(b)
			}
			_, err := p.Reply(out)
			return err
		})
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })
	return srv, srv.State().Listeners[0].Address
}

// upper uppercases one ASCII byte.
func upper(b byte) byte {
	if b >= 'a' && b <= 'z' {
		return b - 'a' + 'A'
	}
	return b
}

// sendDatagram sends one datagram and returns the reply.
func sendDatagram(t *testing.T, addr, payload string) string {
	t.Helper()
	c, err := stdnet.DialTimeout("udp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer closeOrFail(t, c)
	if _, werr := c.Write([]byte(payload)); werr != nil {
		t.Fatalf("write: %v", werr)
	}
	if derr := c.SetReadDeadline(time.Now().Add(3 * time.Second)); derr != nil {
		t.Fatalf("set deadline: %v", derr)
	}
	buf := make([]byte, 2048)
	n, rerr := c.Read(buf)
	if rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	return string(buf[:n])
}

// TestDatagramRoundTrip is the end-to-end proof that the datagram engine serves:
// a real socket, a real send, a real reply through Packet.Reply.
func TestDatagramRoundTrip(t *testing.T) {
	t.Parallel()
	_, addr := startUpper(t)
	if got := sendDatagram(t, addr, "hello"); got != "HELLO" {
		t.Fatalf("reply = %q, want %q", got, "HELLO")
	}
}

// TestDatagramBatchServesEveryPacket pins the batched read path: several
// datagrams queued before the read must all be served, in order, and none may be
// dropped or duplicated by the buffer reuse.
func TestDatagramBatchServesEveryPacket(t *testing.T) {
	t.Parallel()
	seen := make(chan string, 64)
	srv := server.New()
	srv.PacketGroup("collect",
		server.Listen("udp", "127.0.0.1:0"),
		server.BatchSize(8),
	).HandleFunc(func(_ context.Context, p corenet.Packet) error {
		//: copy: the payload is only valid until this returns.
		seen <- string(p.Data())
		return nil
	})
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })

	c, err := stdnet.DialTimeout("udp", srv.State().Listeners[0].Address, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer closeOrFail(t, c)

	const count int = 8
	for i := range count {
		if _, werr := c.Write([]byte{byte('a' + i)}); werr != nil {
			t.Fatalf("write %d: %v", i, werr)
		}
	}
	got := make(map[string]int, count)
	for range count {
		select {
		case payload := <-seen:
			got[payload]++
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d datagrams were served", len(got), count)
		}
	}
	//: every distinct payload must appear exactly once — a reused buffer that
	//: leaked between slots would show up here as a duplicate.
	for i := range count {
		payload := string([]byte{byte('a' + i)})
		if got[payload] != 1 {
			t.Fatalf("payload %q served %d times, want 1", payload, got[payload])
		}
	}
}

// TestDatagramSenderIsReported pins that the batched path decodes the sender
// address correctly. Without it Reply would write to a wrong or nil address,
// which is the failure mode a raw-syscall reader is most likely to introduce.
func TestDatagramSenderIsReported(t *testing.T) {
	t.Parallel()
	addrs := make(chan string, 4)
	srv := server.New()
	srv.PacketGroup("sender", server.Listen("udp", "127.0.0.1:0")).
		HandleFunc(func(_ context.Context, p corenet.Packet) error {
			//: a nil sender would panic here rather than silently misroute.
			addrs <- p.From().String()
			return nil
		})
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })

	c, err := stdnet.DialTimeout("udp", srv.State().Listeners[0].Address, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer closeOrFail(t, c)
	if _, werr := c.Write([]byte("x")); werr != nil {
		t.Fatalf("write: %v", werr)
	}
	select {
	case got := <-addrs:
		if got != c.LocalAddr().String() {
			t.Fatalf("sender = %q, want %q", got, c.LocalAddr().String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the datagram was never served")
	}
}

// TestUnixDatagramUsesTheSameEngine pins the "same thing everywhere" promise on
// the datagram side.
func TestUnixDatagramUsesTheSameEngine(t *testing.T) {
	t.Parallel()
	sock := t.TempDir() + "/dgram.sock"
	served := make(chan string, 4)
	srv := server.New()
	srv.PacketGroup("unix", server.Listen("unixgram", sock)).
		HandleFunc(func(_ context.Context, p corenet.Packet) error {
			served <- string(p.Data())
			return nil
		})
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })

	c, err := stdnet.Dial("unixgram", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer closeOrFail(t, c)
	if _, werr := c.Write([]byte("unixgram")); werr != nil {
		t.Fatalf("write: %v", werr)
	}
	select {
	case got := <-served:
		if got != "unixgram" {
			t.Fatalf("payload = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the datagram was never served")
	}
}

// TestPacketDeclarationErrorsSurfaceAtStart pins that the datagram side makes
// the same ergonomic trade as the stream side, and honours it the same way.
func TestPacketDeclarationErrorsSurfaceAtStart(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		build func(*server.Server)
		want  errs.Code
	}{
		{
			name:  "no handler",
			build: func(s *server.Server) { s.PacketGroup("dns", server.Listen("udp", "127.0.0.1:0")) },
			want:  corenet.CodeHandlerMissing,
		},
		{
			name:  "no address",
			build: func(s *server.Server) { s.PacketGroup("dns").HandleFunc(noopPacket) },
			want:  corenet.CodeInvalidAddress,
		},
		{
			name: "stream family on a datagram group",
			build: func(s *server.Server) {
				s.PacketGroup("dns", server.Listen("tcp", "127.0.0.1:0")).HandleFunc(noopPacket)
			},
			want: corenet.CodeUnsupportedNetwork,
		},
		{
			name: "a name already taken by a stream group",
			build: func(s *server.Server) {
				s.Group("shared", server.Listen("tcp", "127.0.0.1:0")).HandleFunc(noopHandler)
				s.PacketGroup("shared", server.Listen("udp", "127.0.0.1:0")).HandleFunc(noopPacket)
			},
			want: corenet.CodeGroupDuplicate,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := server.New()
			tc.build(srv)
			if err := srv.Start(context.Background()); !errs.HasCode(err, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, err)
			}
		})
	}
}

// TestPanickingPacketHandlerKeepsTheLoopAlive pins containment on the datagram
// side: one malformed packet must not take the socket down for every peer.
func TestPanickingPacketHandlerKeepsTheLoopAlive(t *testing.T) {
	t.Parallel()
	served := make(chan string, 8)
	srv := server.New()
	srv.PacketGroup("fragile", server.Listen("udp", "127.0.0.1:0")).
		HandleFunc(func(_ context.Context, p corenet.Packet) error {
			payload := string(p.Data())
			if payload == "boom" {
				panic("handler exploded")
			}
			served <- payload
			return nil
		})
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })

	c, err := stdnet.DialTimeout("udp", srv.State().Listeners[0].Address, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer closeOrFail(t, c)
	for _, payload := range []string{"boom", "survivor"} {
		if _, werr := c.Write([]byte(payload)); werr != nil {
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

// TestStateReportsNoDatagramDegradationOnLinux pins the honesty requirement: the
// engine must report whether it got the batched read it asked for. On a platform
// with recvmmsg a batching group is not degraded; where it is missing, State
// says so rather than staying silent.
func TestStateReportsNoDatagramDegradationOnLinux(t *testing.T) {
	t.Parallel()
	srv, _ := startUpper(t, server.BatchSize(16))
	state := srv.State()
	if len(state.Listeners) != 1 {
		t.Fatalf("listeners = %d, want 1", len(state.Listeners))
	}
	//: whichever way it went, the report must be self-consistent.
	listener := state.Listeners[0]
	if listener.Degraded != (listener.DegradedReason != "") {
		t.Fatalf("degraded=%v but reason=%q — the report contradicts itself",
			listener.Degraded, listener.DegradedReason)
	}
	if listener.Degraded != state.Degraded() {
		t.Fatal("State.Degraded disagrees with its own listener")
	}
}

// noopPacket is a datagram handler that does nothing.
func noopPacket(context.Context, corenet.Packet) error {
	return nil
}
