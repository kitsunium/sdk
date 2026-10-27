package server

import (
	"errors"
	stdnet "net"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// readLoop serves datagrams until its socket is closed.
//
// Goroutine lifecycle: one per datagram socket, started by bindPacketGroup and
// owned by the Server. It exits when Close or Shutdown closes the socket — the
// only signal that reliably interrupts a blocking read — and releases its
// inFlight token on exit.
//
// Handlers run inline on this goroutine rather than being dispatched to a pool.
// A datagram handler is expected to be short, and a hand-off would cost a
// channel send plus an allocation per packet, undoing the very batching this
// path exists for. A handler that must block should copy the payload and hand
// it to its own worker.
func (s *Server) readLoop(bound *boundPacketConn, group *PacketGroup, handler corenet.PacketHandler) {
	defer s.inFlight.Done()
	//: a handover waits on this to close the socket after the drain.
	defer bound.finish()
	ceiling := packetSize(group.limits)
	reader := bound.source
	slots := newSlots(batchSize(group.limits), reader.slotSize(ceiling))
	held := &packet{conn: bound.pc, sink: bound.sink, group: group.name}
	//: a handler that kept the Packet past its call must not pin the socket
	//: or the last read buffer once the loop is gone.
	defer held.reset()
	//: read until the socket is closed beneath us, or handed over.
	for {
		count, err := reader.readBatch(slots)
		//: a closed socket ends the loop; anything else is transient, because
		//: one malformed datagram must not stop the service.
		if err != nil {
			//: Close or Shutdown ended this loop deliberately.
			if errors.Is(err, stdnet.ErrClosed) {
				//: Close or Shutdown ended this loop deliberately.
				return
			}
			continue
		}
		bound.sink.begin()
		s.dispatch(slots[:count], held, handler, ceiling)
		//: the batch's replies leave together, before the next read.
		bound.sink.flush()
	}
}

// dispatch serves every datagram in a filled batch, dropping any past the
// group's ceiling, and each coalesced message as the datagrams it carries.
//
// An oversized datagram is NOT handed on. The kernel has already discarded its
// tail, so what remains is a prefix that reads exactly like a complete message:
// the handler has no way to tell it was cut, and a partial payload accepted as
// whole is a correctness bug rather than a capacity one. It is dropped and
// counted in State instead — the same answer the accept path gives a connection
// past its ceiling, because there is nobody above a read loop to return an
// error to and the counter is therefore the report. A message the kernel cut
// (datagram.cut) is dropped and counted the same way, in Datagrams.
func (s *Server) dispatch(slots []datagram, held *packet, handler corenet.PacketHandler, ceiling int) {
	//: serve each message in arrival order.
	for i := range slots {
		//: cut short: no datagram in it can be trusted whole.
		if slots[i].cut {
			s.datagrams.truncated.Add(1)
			continue
		}
		s.serveMessage(&slots[i], held, handler, ceiling)
	}
	//: drop the references so a slow next read cannot pin a buffer through the
	//: reused packet value.
	held.data = nil
	held.from = nil
}

// serveMessage serves each datagram of one message, one Packet each.
func (s *Server) serveMessage(slot *datagram, held *packet, handler corenet.PacketHandler, ceiling int) {
	//: one datagram, or every segment of a coalesced message in order.
	for j := 0; ; j++ {
		data, ok := slot.segment(j)
		//: past the message's last datagram.
		if !ok {
			//: every datagram served.
			return
		}
		s.total.Add(1)
		//: the probe byte was filled, so this datagram passed the ceiling and
		//: whatever the kernel copied is a truncated prefix.
		if len(data) > ceiling {
			s.oversized.Add(1)
			continue
		}
		held.data = data
		held.from = slot.addr
		held.id = s.nextID.Add(1)
		//: the handler's error ends this datagram and nothing else; there is
		//: nobody above a read loop to return it to, and one peer's malformed
		//: packet is not the server's failure.
		swallowErr(s.servePacket(held, handler))
	}
}

// servePacket runs the handler for one datagram, containing any panic.
func (s *Server) servePacket(p *packet, handler corenet.PacketHandler) error {
	//: a panicking handler must not take down the read loop, and with it every
	//: other peer of this socket.
	defer recoverHandler()
	//: the handler's own outcome, contained by the deferred recover above.
	return handler.ServePacket(s.runCtx, p)
}
