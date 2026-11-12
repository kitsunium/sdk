//go:build linux

// Package server — the junction of #972 and #852: a reply a datagram lane
// sends leaves from the destination its peer reached.
package server

import (
	"context"
	stdnet "net"
	"net/netip"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/loop"
)

// pinnedEcho echoes each datagram, from OnPacket, or from OnBatchEnd when
// deferred: a reply decided after the datagram's own callback.
type pinnedEcho struct {
	deferred bool
	// queued are the batch's echoes when deferred; the lane's thread alone
	// touches them (one lane).
	queued []queuedEcho
}

// queuedEcho is one deferred echo.
type queuedEcho struct {
	to netip.AddrPort
	b  []byte
}

// OnPacket implements corenet.PacketLoopHandler.
func (h *pinnedEcho) OnPacket(io corenet.PacketLoopIO, from netip.AddrPort, b []byte) {
	//: decided at the batch's end.
	if h.deferred {
		h.queued = append(h.queued, queuedEcho{to: from, b: append([]byte(nil), b...)})
		return
	}
	_ = io.Reply(from, b)
}

// OnBatchEnd implements corenet.PacketLoopHandler.
func (h *pinnedEcho) OnBatchEnd(io corenet.PacketLoopIO) {
	//: each deferred echo.
	for _, q := range h.queued {
		_ = io.Reply(q.to, q.b)
	}
	h.queued = h.queued[:0]
}

// startPinnedLane serves tc's wildcard socket with a group HandleLoop serves,
// on io_uring or, with epoll, on the batched reader.
func startPinnedLane(t *testing.T, tc pktinfoCase, h *pinnedEcho, epoll bool) (srv *Server, port int) {
	t.Helper()
	srv = newServer(envOf(nil))
	//: the batched reader, as a refused probe leaves it.
	if epoll {
		srv.startLane = func(*Server, *boundPacketConn, *PacketGroup) (loop.PacketLane, error) {
			return nil, errs.Wrap(corenet.LoopBackendUnavailable, errs.WrapParams{},
				errs.String("stage", "recvmsg"), errs.String("reason", "injected"))
		}
	}
	srv.PacketGroup("dg", Listen(tc.network, tc.bind)).HandleLoop(h)
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { swallowErr(srv.Close()) })
	addr, err := stdnet.ResolveUDPAddr("udp", srv.State().Listeners[0].Address)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	//: io_uring asked for: absent here, the refusal is named and the case skips.
	if !epoll {
		row, ok := laneRow(srv)
		if !ok || row.Status != statusOn {
			t.Skipf("io_uring datagram lane not serving here: %+v", row)
		}
	}
	return srv, addr.Port
}

// TestPacketLoop_replyLeavesFromTheDestination is the junction of #972 and
// #852: on a wildcard socket a group HandleLoop serves, a reply to a peer
// leaves from the address that peer reached — 127.0.0.2, not the kernel's
// 127.0.0.1 —, on the io_uring lane and on the batched reader's, whether the
// handler replies in OnPacket or in OnBatchEnd.
func TestPacketLoop_replyLeavesFromTheDestination(t *testing.T) {
	for _, tc := range pktinfoCases(t) {
		for _, epoll := range []bool{false, true} {
			for _, deferred := range []bool{false, true} {
				name := tc.name + map[bool]string{false: "/uring", true: "/epoll"}[epoll] +
					map[bool]string{false: "/OnPacket", true: "/OnBatchEnd"}[deferred]
				t.Run(name, func(t *testing.T) {
					skipUnlessIPv6(t, tc.target)
					srv, port := startPinnedLane(t, tc, &pinnedEcho{deferred: deferred}, epoll)
					from := pktinfoExchange(t, tc, port, []byte("ping"))
					//: the source the peer reached.
					if !from.IP.Equal(tc.want) {
						t.Fatalf("the reply left from %v, want %v", from.IP, tc.want)
					}
					rows := srv.State().Datagrams.Capabilities
					if !hasCapability(rows, corenet.CapabilityUDPPktinfo, statusOn) {
						t.Fatalf("capabilities = %+v, want udp-pktinfo on", rows)
					}
				})
			}
		}
	}
}

// TestPacketLoop_replyToAnUnseenPeer pins the other half: a reply to a peer
// that sent nothing in the batch leaves from the kernel's choice, and still
// leaves.
func TestPacketLoop_replyToAnUnseenPeer(t *testing.T) {
	for _, epoll := range []bool{false, true} {
		t.Run(map[bool]string{false: "uring", true: "epoll"}[epoll], func(t *testing.T) {
			other, err := stdnet.ListenPacket("udp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { swallowErr(other.Close()) })
			h := &redirectEcho{to: other.LocalAddr().(*stdnet.UDPAddr).AddrPort()}
			tc := pktinfoCase{network: "udp4", bind: "0.0.0.0:0", target: "127.0.0.2"}
			srv := newServer(envOf(nil))
			if epoll {
				srv.startLane = func(*Server, *boundPacketConn, *PacketGroup) (loop.PacketLane, error) {
					return nil, errs.Wrap(corenet.LoopBackendUnavailable, errs.WrapParams{},
						errs.String("stage", "recvmsg"), errs.String("reason", "injected"))
				}
			}
			srv.PacketGroup("dg", Listen(tc.network, tc.bind)).HandleLoop(h)
			if err := srv.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { swallowErr(srv.Close()) })
			if row, ok := laneRow(srv); !epoll && (!ok || row.Status != statusOn) {
				t.Skipf("io_uring datagram lane not serving here: %+v", row)
			}
			addr, err := stdnet.ResolveUDPAddr("udp", srv.State().Listeners[0].Address)
			if err != nil {
				t.Fatal(err)
			}
			sender, err := stdnet.Dial("udp4", (&stdnet.UDPAddr{IP: stdnet.ParseIP("127.0.0.2"), Port: addr.Port}).String())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { swallowErr(sender.Close()) })
			if _, err := sender.Write([]byte("ping")); err != nil {
				t.Fatal(err)
			}
			if err := other.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 64)
			n, from, err := other.ReadFrom(buf)
			if err != nil {
				t.Fatalf("the redirected reply never came: %v", err)
			}
			if string(buf[:n]) != "ping" || !from.(*stdnet.UDPAddr).IP.IsLoopback() {
				t.Fatalf("reply %q from %v", buf[:n], from)
			}
		})
	}
}

// redirectEcho sends every datagram to one other peer, which sent nothing.
type redirectEcho struct {
	to netip.AddrPort
}

// OnPacket implements corenet.PacketLoopHandler.
func (h *redirectEcho) OnPacket(io corenet.PacketLoopIO, _ netip.AddrPort, b []byte) {
	_ = io.Reply(h.to, b)
}

// OnBatchEnd implements corenet.PacketLoopHandler.
func (*redirectEcho) OnBatchEnd(corenet.PacketLoopIO) {}
