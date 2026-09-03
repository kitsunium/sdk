// Package server — the datagram read loop.
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
	slots := newSlots(batchSize(group.limits), packetSize(group.limits))
	reader := newDatagramSource(bound.pc)
	held := &packet{conn: bound.pc, group: group.name}
	//: read until the socket is closed beneath us.
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
		s.dispatch(slots[:count], held, handler)
	}
}

// dispatch serves every datagram in a filled batch.
func (s *Server) dispatch(slots []datagram, held *packet, handler corenet.PacketHandler) {
	//: serve each datagram in arrival order.
	for i := range slots {
		s.total.Add(1)
		held.data = slots[i].payload()
		held.from = slots[i].addr
		held.id = s.nextID.Add(1)
		//: the handler's error ends this datagram and nothing else; there is
		//: nobody above a read loop to return it to, and one peer's malformed
		//: packet is not the server's failure.
		swallowErr(s.servePacket(held, handler))
	}
	//: drop the references so a slow next read cannot pin a buffer through the
	//: reused packet value.
	held.data = nil
	held.from = nil
}

// servePacket runs the handler for one datagram, containing any panic.
func (s *Server) servePacket(p *packet, handler corenet.PacketHandler) error {
	//: a panicking handler must not take down the read loop, and with it every
	//: other peer of this socket.
	defer recoverHandler()
	//: the handler's own outcome, contained by the deferred recover above.
	return handler.ServePacket(s.runCtx, p)
}
