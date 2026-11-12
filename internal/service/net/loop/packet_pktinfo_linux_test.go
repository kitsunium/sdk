//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — the io_uring datagram lane answers from the destination its
// peer reached (#972 joined to #852).
package loop

import (
	"encoding/binary"
	stdnet "net"
	"net/netip"
	"runtime"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// wildcardLane starts a lane on a socket bound to 0.0.0.0 that asks for
// IP_PKTINFO, as server asks it of a wildcard socket, and returns its port.
func wildcardLane(t *testing.T, h corenet.PacketLoopHandler, c *laneCounters) (*packetLane, int) {
	t.Helper()
	pc, err := stdnet.ListenUDP("udp4", &stdnet.UDPAddr{IP: stdnet.IPv4zero})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	raw, err := pc.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	dup := -1
	var derr error
	cerr := raw.Control(func(fd uintptr) {
		//: the destination asked, then the lane's own descriptor.
		if derr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_PKTINFO, 1); derr != nil {
			return
		}
		r, _, e := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_DUPFD_CLOEXEC, 0)
		if e != 0 {
			derr = e
			return
		}
		dup = int(r)
		derr = syscall.SetNonblock(dup, true)
	})
	if cerr != nil || derr != nil {
		t.Fatal(cerr, derr)
	}
	lane, err := StartPacket(&PacketConfig{Group: "dg", FD: dup, Lane: 3, Ceiling: 8192, Handler: h, Counters: c.of()})
	//: a kernel without the lane: refused by name.
	if err != nil {
		if !errs.HasCode(err, corenet.CodeLoopBackendUnavailable) {
			t.Fatalf("StartPacket: %v, want LoopBackendUnavailable", err)
		}
		requireLane(t, err)
	}
	t.Cleanup(lane.Stop)
	return lane.(*packetLane), pc.LocalAddr().(*stdnet.UDPAddr).Port
}

// pinnedExchange sends n datagrams from 127.0.0.1 to 127.0.0.2:port and reads
// want answers each, failing unless every one left from 127.0.0.2.
func pinnedExchange(t *testing.T, port, n, want int) {
	t.Helper()
	c, err := stdnet.ListenUDP("udp4", &stdnet.UDPAddr{IP: stdnet.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	dst := &stdnet.UDPAddr{IP: stdnet.IPv4(127, 0, 0, 2), Port: port}
	msg := make([]byte, 1000)
	buf := make([]byte, 2048)
	//: one at a time, its answers read.
	for i := range n {
		binary.BigEndian.PutUint32(msg, uint32(i))
		if _, err := c.WriteTo(msg, dst); err != nil {
			t.Fatal(err)
		}
		for range want {
			_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, from, err := c.ReadFrom(buf)
			if err != nil {
				t.Fatalf("answer lost: %v", err)
			}
			//: the address the peer wrote to.
			if got := from.(*stdnet.UDPAddr).IP; !got.Equal(dst.IP) {
				t.Fatalf("answer from %v, want %v", got, dst.IP)
			}
		}
	}
}

// TestPacketLane_answersFromTheDestination pins the source of every answer on
// a wildcard socket: one SENDMSG a reply, and a UDP_SEGMENT run of eight with
// the source's cmsg beside the segment size.
func TestPacketLane_answersFromTheDestination(t *testing.T) {
	for _, copies := range []int{1, 8} {
		t.Run(map[int]string{1: "one", 8: "gso run"}[copies], func(t *testing.T) {
			h := &echoLane{copies: copies}
			var c laneCounters
			lane, port := wildcardLane(t, h, &c)
			pinnedExchange(t, port, 50, copies)
			lane.Stop()
			if c.failures.Load() != 0 || h.refused.Load() != 0 || (copies > 1 && lane.gsoSends.Load() == 0) {
				t.Fatalf("failures=%d refused=%d gso sends=%d", c.failures.Load(), h.refused.Load(), lane.gsoSends.Load())
			}
		})
	}
}

// refusedSourceLane answers copies times, after naming a multicast address —
// a source the kernel refuses with EINVAL — as the peer's destination.
type refusedSourceLane struct {
	copies int
}

// OnPacket implements corenet.PacketLoopHandler.
func (h *refusedSourceLane) OnPacket(io corenet.PacketLoopIO, from netip.AddrPort, b []byte) {
	lp := io.(*packetLane)
	lp.forget()
	lp.remember(from, replySource{addr: [16]byte{239, 0, 0, 1}, n: 4})
	for range h.copies {
		_ = io.Reply(from, b)
	}
}

// OnBatchEnd implements corenet.PacketLoopHandler.
func (*refusedSourceLane) OnBatchEnd(corenet.PacketLoopIO) {}

// TestPacketLane_refusedSourceFallsBack pins server's pinWriter rule on the
// lane: a source the kernel refuses (EINVAL) is sent again from the kernel's
// choice — the record's control data cut of its source, a UDP_SEGMENT run kept
// whole —, never lost and never counted as failed.
func TestPacketLane_refusedSourceFallsBack(t *testing.T) {
	for _, copies := range []int{1, 8} {
		t.Run(map[int]string{1: "one", 8: "gso run"}[copies], func(t *testing.T) {
			var c laneCounters
			lane, port := wildcardLane(t, &refusedSourceLane{copies: copies}, &c)
			cl, err := stdnet.DialUDP("udp4", nil, &stdnet.UDPAddr{IP: stdnet.IPv4(127, 0, 0, 1), Port: port})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cl.Close() })
			if got := exchange(t, cl, 20, 500, copies); got != 20*copies {
				t.Fatalf("answers = %d, want %d", got, 20*copies)
			}
			lane.Stop()
			if c.failures.Load() != 0 {
				t.Fatalf("failures = %d, want 0", c.failures.Load())
			}
		})
	}
}

// TestPacketLane_sourceFor pins the lookup: the newest destination of a peer,
// an IPv4-mapped peer matching its IPv4 form, nothing for a peer unseen, the
// oldest overwritten past packetSeenMax, and nothing once forgotten.
func TestPacketLane_sourceFor(t *testing.T) {
	lp := &packetLane{}
	a := netip.MustParseAddrPort("127.0.0.1:1000")
	b := netip.MustParseAddrPort("127.0.0.1:2000")
	s1 := replySource{addr: [16]byte{127, 0, 0, 2}, n: 4}
	s2 := replySource{addr: [16]byte{127, 0, 0, 3}, n: 4}
	lp.remember(a, s1)
	lp.remember(b, s1)
	lp.remember(a, s2)
	if got := lp.sourceFor(a); got != s2 {
		t.Fatalf("sourceFor(a) = %v, want the newest", got)
	}
	if got := lp.sourceFor(netip.AddrPortFrom(netip.AddrFrom16(a.Addr().As16()), a.Port())); got != s2 {
		t.Fatalf("mapped peer = %v, want %v", got, s2)
	}
	if got := lp.sourceFor(netip.MustParseAddrPort("127.0.0.1:3000")); got.n != 0 {
		t.Fatalf("unseen peer = %v", got)
	}
	lp.remember(a, replySource{})
	if lp.seenLen != 3 {
		t.Fatalf("a zero source was remembered: %d", lp.seenLen)
	}
	for i := range packetSeenMax {
		lp.remember(netip.AddrPortFrom(a.Addr(), uint16(10000+i)), s1)
	}
	if lp.seenLen != packetSeenMax || lp.sourceFor(b).n != 0 {
		t.Fatalf("len=%d b=%v, want the oldest overwritten", lp.seenLen, lp.sourceFor(b))
	}
	lp.forget()
	if lp.sourceFor(netip.AddrPortFrom(a.Addr(), uint16(10000+packetSeenMax-1))).n != 0 {
		t.Fatal("a sender outlived forget")
	}
}

// TestPacketLane_pktinfoSource pins the reading: IPv4's ipi_spec_dst, IPv6's
// address unless a group, a mapped broadcast or nothing, any other message
// none.
func TestPacketLane_pktinfoSource(t *testing.T) {
	v4 := make([]byte, inPktinfoBytes)
	copy(v4[4:8], []byte{127, 0, 0, 2})
	copy(v4[8:12], []byte{255, 255, 255, 255})
	if src, ok := pktinfoSource(ipprotoIP, ipPktinfo, v4); !ok || src.n != 4 || [4]byte(src.addr[:4]) != [4]byte{127, 0, 0, 2} {
		t.Fatalf("IPv4 = %v %v", src, ok)
	}
	if src, ok := pktinfoSource(ipprotoIP, ipPktinfo, make([]byte, inPktinfoBytes)); !ok || src.n != 0 {
		t.Fatalf("IPv4 zero = %v %v", src, ok)
	}
	for _, c := range []struct {
		addr string
		n    uint8
	}{{"::1", 16}, {"ff02::1", 0}, {"::", 0}, {"::ffff:255.255.255.255", 0}, {"::ffff:127.0.0.2", 16}} {
		v6 := make([]byte, in6PktinfoBytes)
		a := netip.MustParseAddr(c.addr).As16()
		copy(v6, a[:])
		if src, ok := pktinfoSource(ipprotoIPv6, ipv6Pktinfo, v6); !ok || src.n != c.n {
			t.Fatalf("%s = %v %v, want n=%d", c.addr, src, ok, c.n)
		}
	}
	if _, ok := pktinfoSource(solUDP, udpGRO, make([]byte, 4)); ok {
		t.Fatal("UDP_GRO read as a destination")
	}
}

// TestPacketLane_allocsPinned pins the pinned hot path at zero allocations: a
// datagram whose IP_PKTINFO is read, its reply queued from that source, the
// SENDMSG built with the cmsg and submitted.
func TestPacketLane_allocsPinned(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pc, fd := udpServer(t)
	cl := client(t, pc)
	var c laneCounters
	lp := &packetLane{
		done: make(chan struct{}), efd: -1,
		cfg: PacketConfig{FD: fd, Ceiling: 8192, Handler: &echoLane{}, Counters: c.of()},
	}
	defer lp.release()
	//: no lane here.
	if stage, why := lp.setup(); stage != "" {
		requireLane(t, errs.Wrap(corenet.LoopBackendUnavailable, errs.WrapParams{}, errs.String("stage", stage), errs.String("reason", why)))
	}
	name := make([]byte, packetNameRoom)
	binary.LittleEndian.PutUint16(name, afInet)
	binary.BigEndian.PutUint16(name[2:4], uint16(cl.LocalAddr().(*stdnet.UDPAddr).Port))
	copy(name[4:8], []byte{127, 0, 0, 1})
	ctrl := make([]byte, cmsgSpace(inPktinfoBytes))
	binary.LittleEndian.PutUint64(ctrl[0:8], uint64(cmsgHdrBytes+inPktinfoBytes))
	binary.LittleEndian.PutUint32(ctrl[8:12], uint32(ipprotoIP))
	binary.LittleEndian.PutUint32(ctrl[12:16], uint32(ipPktinfo))
	copy(ctrl[cmsgHdrBytes+4:], []byte{127, 0, 0, 1})
	payload := make([]byte, 64)
	n := testing.AllocsPerRun(200, func() {
		lp.deliver(name, ctrl, payload, 0)
		lp.endBatch()
		lp.u.submit()
		lp.u.reap(int(lp.u.cqLen), lp.onCQE)
	})
	//: nothing allocated per datagram.
	if n != 0 {
		t.Fatalf("allocs per datagram = %v, want 0", n)
	}
	//: the replies left, none refused.
	if c.failures.Load() != 0 {
		t.Fatalf("failures = %d", c.failures.Load())
	}
}
