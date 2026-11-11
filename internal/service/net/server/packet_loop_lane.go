package server

import (
	stdnet "net"
	"net/netip"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// packetLoopLane is a datagram lane served by the batched reader (#852): the
// epoll lane of a group PacketGroup.HandleLoop serves, where io_uring's is
// absent, switched off or not built — the netpoller's recvmmsg, GRO and the
// batch's sendmmsg, behind corenet.PacketLoopIO.
type packetLoopLane struct {
	// sink sends the batch's replies; always a batched one.
	sink datagramSink
	// lane is the lane's index.
	lane int
	// handed is a datagram handed on since the last OnBatchEnd.
	handed bool
	// batch is the batch being handed on, whose destinations a reply leaves
	// from (#972); nil between batches.
	batch []datagram
}

// Reply implements corenet.PacketLoopIO: queued in the batch's sink, sent
// when the batch ends, from the destination to reached in this batch (#972).
func (l *packetLoopLane) Reply(to netip.AddrPort, p []byte) error {
	addr := stdnet.UDPAddrFromAddrPort(to)
	//: a peer of the batch on a socket that reads destinations.
	if local := l.sourceFor(to); local.sourceLen != 0 {
		_, err := l.sink.replyFrom(p, addr, local)
		//: the sink's answer.
		return err
	}
	_, err := l.sink.reply(p, addr)
	//: the sink's answer.
	return err
}

// sourceFor is the destination of the newest datagram to sent in the batch,
// an IPv4-mapped peer matching its IPv4 form; zero, the kernel's choice, for a
// peer that sent none or a socket that reads no destination.
func (l *packetLoopLane) sourceFor(to netip.AddrPort) localAddress {
	want := to.Addr().Unmap()
	//: newest first.
	for i := len(l.batch) - 1; i >= 0; i-- {
		d := &l.batch[i]
		//: a cut message was handed nothing; no destination, nothing to pin.
		if d.cut || d.local.sourceLen == 0 {
			continue
		}
		from := addrPortOf(d.addr)
		//: the same peer.
		if from.Port() == to.Port() && from.Addr().Unmap() == want {
			//: its destination.
			return d.local
		}
	}
	//: unseen.
	return localAddress{}
}

// Loop implements corenet.PacketLoopIO: the lane's index.
func (l *packetLoopLane) Loop() int {
	//: fixed at bind.
	return l.lane
}

// dispatchLoop hands every datagram of a filled batch to h, one OnPacket
// each, a coalesced message split, a cut one or one past the ceiling counted
// and dropped as dispatch does, then OnBatchEnd once if any was handed on.
func (s *Server) dispatchLoop(slots []datagram, l *packetLoopLane, h corenet.PacketLoopHandler, ceiling int) {
	l.batch = slots
	defer func() { l.batch = nil }()
	//: each message in arrival order.
	for i := range slots {
		//: cut short: no datagram in it can be trusted whole.
		if slots[i].cut {
			s.datagrams.truncated.Add(1)
			continue
		}
		from := addrPortOf(slots[i].addr)
		//: one datagram, or every segment of a coalesced message in order.
		for j := 0; ; j++ {
			data, ok := slots[i].segment(j)
			//: past the message's last datagram.
			if !ok {
				break
			}
			s.total.Add(1)
			//: past the ceiling: a truncated prefix, dropped.
			if len(data) > ceiling {
				s.oversized.Add(1)
				continue
			}
			l.handed = true
			servePacketLoop(h, l, from, data)
		}
	}
	//: the handler's flush point, once a batch that handed one on.
	if l.handed {
		l.handed = false
		endPacketLoop(h, l)
	}
}

// servePacketLoop runs OnPacket, containing a panic as servePacket does.
func servePacketLoop(h corenet.PacketLoopHandler, l *packetLoopLane, from netip.AddrPort, data []byte) {
	//: one handler's panic must not end the lane.
	defer recoverHandler()
	h.OnPacket(l, from, data)
}

// endPacketLoop runs OnBatchEnd, containing a panic.
func endPacketLoop(h corenet.PacketLoopHandler, l *packetLoopLane) {
	//: one handler's panic must not end the lane.
	defer recoverHandler()
	h.OnBatchEnd(l)
}

// addrPortOf is a UDP sender's address as a value; the zero AddrPort for any
// other.
func addrPortOf(a stdnet.Addr) netip.AddrPort {
	udp, ok := a.(*stdnet.UDPAddr)
	//: not UDP.
	if !ok || udp == nil {
		//: none.
		return netip.AddrPort{}
	}
	//: the value.
	return udp.AddrPort()
}
