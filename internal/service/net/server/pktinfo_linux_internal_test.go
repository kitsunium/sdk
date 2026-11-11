//go:build linux

package server

import (
	"bytes"
	"context"
	"errors"
	stdnet "net"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// pktinfoCase is one bound socket and one destination the client aims at.
type pktinfoCase struct {
	name, network, bind, target string
	// want is the IP To must report and the reply must come from.
	want stdnet.IP
}

// pktinfoCases are the wildcard sockets of #972's acceptance.
func pktinfoCases(t *testing.T) []pktinfoCase {
	t.Helper()
	return []pktinfoCase{
		{name: "ipv4 wildcard", network: "udp4", bind: "0.0.0.0:0", target: "127.0.0.2", want: stdnet.ParseIP("127.0.0.2")},
		{name: "ipv6 wildcard", network: "udp6", bind: "[::]:0", target: "::1", want: stdnet.ParseIP("::1")},
		{name: "ipv4-mapped peer", network: "udp", bind: "[::]:0", target: "127.0.0.2", want: stdnet.ParseIP("127.0.0.2")},
	}
}

// skipUnlessIPv6 skips a case on a host whose loopback lacks ::1 (a container
// with IPv6 off), after saying so.
func skipUnlessIPv6(t *testing.T, target string) {
	t.Helper()
	ip := stdnet.ParseIP(target)
	if ip.To4() != nil {
		return
	}
	c, err := stdnet.ListenPacket("udp6", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback on this host: %v", err)
	}
	swallowErr(c.Close())
}

// startPktinfoServer runs one datagram group that echoes what it reads and
// records its destination.
func startPktinfoServer(t *testing.T, tc pktinfoCase, env map[string]string, batched bool) (srv *Server, port int, seen <-chan stdnet.Addr) {
	t.Helper()
	got := make(chan stdnet.Addr, 16)
	srv = newServer(envOf(env))
	opts := []GroupOption{Listen(tc.network, tc.bind)}
	if batched {
		opts = append(opts, BatchReplies())
	}
	srv.PacketGroup("pkt", opts...).HandleFunc(func(_ context.Context, p corenet.Packet) error {
		select {
		case got <- p.To():
		default:
		}
		_, err := p.Reply(p.Data())
		return err
	})
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { swallowErr(srv.Close()) })
	addr, err := stdnet.ResolveUDPAddr("udp", srv.State().Listeners[0].Address)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return srv, addr.Port, got
}

// exchange sends one datagram to target:port and returns the reply's source.
func pktinfoExchange(t *testing.T, tc pktinfoCase, port int, payload []byte) *stdnet.UDPAddr {
	t.Helper()
	network := "udp4"
	if stdnet.ParseIP(tc.target).To4() == nil {
		network = "udp6"
	}
	client, err := stdnet.ListenPacket(network, net4or6Loopback(network))
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	t.Cleanup(func() { swallowErr(client.Close()) })
	if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	dst := &stdnet.UDPAddr{IP: stdnet.ParseIP(tc.target), Port: port}
	if _, err := client.WriteTo(payload, dst); err != nil {
		t.Fatalf("send: %v", err)
	}
	buf := make([]byte, 2048)
	n, from, err := client.ReadFrom(buf)
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if !bytes.Equal(buf[:n], payload) {
		t.Fatalf("reply = %q", buf[:n])
	}
	addr, ok := from.(*stdnet.UDPAddr)
	if !ok {
		t.Fatalf("source is a %T", from)
	}
	return addr
}

// net4or6Loopback is the client's own bind address.
func net4or6Loopback(network string) string {
	if network == "udp6" {
		return "[::1]:0"
	}
	return "127.0.0.1:0"
}

// assertDestination reads what the handler saw and what the reply's source was.
func assertDestination(t *testing.T, seen <-chan stdnet.Addr, from *stdnet.UDPAddr, want stdnet.IP, port int) {
	t.Helper()
	var to stdnet.Addr
	select {
	case to = <-seen:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never ran")
	}
	addr, ok := to.(*stdnet.UDPAddr)
	if !ok || !addr.IP.Equal(want) || addr.Port != port {
		t.Fatalf("To = %v, want %v:%d", to, want, port)
	}
	if !from.IP.Equal(want) {
		t.Fatalf("the reply left from %v, want %v", from.IP, want)
	}
}

// TestPktinfo_wildcardAnswersFromTheDestination pins the destination named and
// the reply's source across the wildcard sockets, on the at-once and the batched
// paths.
func TestPktinfo_wildcardAnswersFromTheDestination(t *testing.T) {
	for _, tc := range pktinfoCases(t) {
		for _, batched := range []bool{false, true} {
			name := tc.name + "/at once"
			if batched {
				name = tc.name + "/batched"
			}
			t.Run(name, func(t *testing.T) {
				skipUnlessIPv6(t, tc.target)
				srv, port, seen := startPktinfoServer(t, tc, nil, batched)
				from := pktinfoExchange(t, tc, port, []byte("ping"))
				assertDestination(t, seen, from, tc.want, port)
				if rows := srv.State().Datagrams.Capabilities; !hasCapability(rows, corenet.CapabilityUDPPktinfo, statusOn) {
					t.Fatalf("capabilities = %+v, want udp-pktinfo on", rows)
				}
			})
		}
	}
}

// hasCapability reports whether rows holds name at status.
func hasCapability(rows []corenet.CapabilityValue, name corenet.Capability, status string) bool {
	for _, row := range rows {
		if row.Name == name && row.Status == status {
			return true
		}
	}
	return false
}

// TestPktinfo_disabledByTheOperator pins the switch: To is the bound address and
// the reply's source the kernel's.
func TestPktinfo_disabledByTheOperator(t *testing.T) {
	tc := pktinfoCase{network: "udp4", bind: "0.0.0.0:0", target: "127.0.0.2"}
	env := map[string]string{corenet.EnvDisable: string(corenet.CapabilityUDPPktinfo)}
	srv, port, seen := startPktinfoServer(t, tc, env, false)
	from := pktinfoExchange(t, tc, port, []byte("ping"))
	var to stdnet.Addr
	select {
	case to = <-seen:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never ran")
	}
	if !to.(*stdnet.UDPAddr).IP.IsUnspecified() {
		t.Fatalf("To = %v, want the bound wildcard", to)
	}
	if from.IP.Equal(tc.want) || !from.IP.IsLoopback() {
		t.Fatalf("the reply left from %v, want the kernel's choice", from.IP)
	}
	rows := srv.State().Datagrams.Capabilities
	if !hasCapability(rows, corenet.CapabilityUDPPktinfo, statusDisabled) {
		t.Fatalf("capabilities = %+v, want udp-pktinfo disabled", rows)
	}
}

// TestPktinfo_boundToOneAddress pins that a socket bound to one address does not
// ask, reports that address, and has no row.
func TestPktinfo_boundToOneAddress(t *testing.T) {
	tc := pktinfoCase{network: "udp4", bind: "127.0.0.1:0", target: "127.0.0.1", want: stdnet.ParseIP("127.0.0.1")}
	srv, port, seen := startPktinfoServer(t, tc, nil, false)
	from := pktinfoExchange(t, tc, port, []byte("ping"))
	assertDestination(t, seen, from, tc.want, port)
	if rows := srv.State().Datagrams.Capabilities; hasCapability(rows, corenet.CapabilityUDPPktinfo, statusOn) {
		t.Fatalf("capabilities = %+v, want no udp-pktinfo row", rows)
	}
	srv.mu.RLock()
	sc := packetSocket(srv.packetConns[0].pc)
	srv.mu.RUnlock()
	raw, err := sc.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var value int
	var gerr error
	if cerr := raw.Control(func(fd uintptr) { value, gerr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_PKTINFO) }); cerr != nil || gerr != nil {
		t.Fatalf("getsockopt: %v %v", cerr, gerr)
	}
	if value != 0 {
		t.Fatalf("IP_PKTINFO = %d on a socket bound to one address", value)
	}
}

// pktinfoReader is a reader over a wildcard IPv4 socket with the destination
// asked for, GRO as given.
func pktinfoReader(t *testing.T, gro bool) (*multiReader, *stdnet.UDPConn) {
	t.Helper()
	pc, err := stdnet.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { swallowErr(pc.Close()) })
	udp := pc.(*stdnet.UDPConn)
	reader := newDatagramSource(udp, gro).(*multiReader)
	asked, on, why := enableDestination(reader, udp)
	if !asked || !on {
		t.Fatalf("asked=%v on=%v why=%q", asked, on, why)
	}
	return reader, udp
}

// TestPktinfo_withAndWithoutGRO pins the destination read from a slot on both
// read shapes, its control room restored before every read (ADR 0192 §D1).
func TestPktinfo_withAndWithoutGRO(t *testing.T) {
	for _, gro := range []bool{false, true} {
		name := "GRO off"
		if gro {
			name = "GRO on"
		}
		t.Run(name, func(t *testing.T) {
			reader, udp := pktinfoReader(t, gro)
			port := udp.LocalAddr().(*stdnet.UDPAddr).Port
			sender, err := stdnet.Dial("udp4", (&stdnet.UDPAddr{IP: stdnet.ParseIP("127.0.0.2"), Port: port}).String())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { swallowErr(sender.Close()) })
			slots := newSlots(4, reader.slotSize(1200))
			for range 3 {
				if _, err := sender.Write(make([]byte, 100)); err != nil {
					t.Fatal(err)
				}
				if n, err := reader.readBatch(slots); err != nil || n != 1 {
					t.Fatalf("readBatch = %d, %v", n, err)
				}
				to := slots[0].local.addrOf(port)
				if to == nil || !to.IP.Equal(stdnet.ParseIP("127.0.0.2")) {
					t.Fatalf("local = %+v", slots[0].local)
				}
				if slots[0].cut {
					t.Fatal("the message was cut")
				}
				if got := int(reader.headers[0].hdr.Controllen); got == reader.controlSpace {
					t.Fatalf("the kernel left Controllen at the capacity %d: nothing was written", got)
				}
				reader.prepare()
				if got := int(reader.headers[0].hdr.Controllen); got != reader.controlSpace {
					t.Fatalf("prepare restored Controllen to %d, want %d", got, reader.controlSpace)
				}
			}
		})
	}
}

// TestPktinfo_pinnedSendsAreConcurrencySafe pins the unbatched writer's own
// guard, a handler replying from a goroutine after its call.
func TestPktinfo_pinnedSendsAreConcurrencySafe(t *testing.T) {
	pc, err := stdnet.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { swallowErr(pc.Close()) })
	writer := newPinWriter(pc, pc.(*stdnet.UDPConn))
	if writer == nil {
		t.Fatal("no pin writer on a UDP socket")
	}
	peer, err := stdnet.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { swallowErr(peer.Close()) })
	if err := peer.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var source localAddress
	source.sourceLen = stdnet.IPv4len
	copy(source.source[:], stdnet.ParseIP("127.0.0.2").To4())
	var wait sync.WaitGroup
	for range 8 {
		wait.Go(func() {
			if n, serr := writer.send([]byte("x"), peer.LocalAddr(), source); serr != nil || n != 1 {
				t.Errorf("send = %d, %v", n, serr)
			}
		})
	}
	wait.Wait()
	buf := make([]byte, 8)
	for range 8 {
		_, from, rerr := peer.ReadFrom(buf)
		if rerr != nil {
			t.Fatalf("read: %v", rerr)
		}
		if !from.(*stdnet.UDPAddr).IP.Equal(stdnet.ParseIP("127.0.0.2")) {
			t.Fatalf("source = %v", from)
		}
	}
}

// TestPktinfo_unsourceableAddressFallsBack pins that a source the kernel will
// not send from is answered from its own choice, not lost: EINVAL then WriteTo.
func TestPktinfo_unsourceableAddressFallsBack(t *testing.T) {
	pc, err := stdnet.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { swallowErr(pc.Close()) })
	writer := newPinWriter(pc, pc.(*stdnet.UDPConn))
	peer, err := stdnet.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { swallowErr(peer.Close()) })
	if err := peer.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var source localAddress
	source.sourceLen = stdnet.IPv4len
	// A multicast source: the kernel refuses it with EINVAL.
	copy(source.source[:], stdnet.ParseIP("239.0.0.1").To4())
	if _, serr := writer.send([]byte("x"), peer.LocalAddr(), source); serr != nil {
		t.Fatalf("send: %v", serr)
	}
	buf := make([]byte, 8)
	if _, _, rerr := peer.ReadFrom(buf); rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	// A closed socket is not retried as success: the error reaches the caller.
	swallowErr(pc.Close())
	if _, serr := writer.send([]byte("x"), peer.LocalAddr(), source); serr == nil || !(errors.Is(serr, stdnet.ErrClosed) || errors.Is(serr, os.ErrClosed)) {
		t.Fatalf("send on a closed socket = %v", serr)
	}
}
