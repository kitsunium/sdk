package server

import (
	"context"
	stdnet "net"
	"slices"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

const (
	// faultGSO is the Source of a fault naming a group a socket of which sends
	// without GSO (ADR 0192).
	faultGSO string = "gso"
	// handoverBudget bounds the wait for the handover's drains, inside Shutdown's
	// own budget: a socket that keeps receiving faster than one batch a read never
	// reaches EAGAIN, and the stream and loop drains after it share the same
	// context (ADR 0192 §D4).
	handoverBudget time.Duration = 250 * time.Millisecond
)

// newBoundPacket pairs a live datagram socket with its group, its source — the
// batched reader with UDP GRO on where the platform has it — and its sink:
// replies at once, or per batch under BatchReplies.
func (s *Server) newBoundPacket(group *PacketGroup, addr corenet.AddressValue, pc stdnet.PacketConn) *boundPacketConn {
	name := group.name
	//: built once, before the read loop starts, so nothing races on them.
	return &boundPacketConn{
		group: name, addr: addr, pc: pc,
		source: newDatagramSource(pc, true),
		sink:   newDatagramSink(pc, group.batchReplies, &s.datagrams, func() { s.recordGSOOff(name) }),
		done:   make(chan struct{}),
	}
}

// handoverPackets hands every datagram socket over before it is closed, and
// waits, within handoverBudget and ctx, for each read loop's drain to end:
// what the socket queued coalesced is served here, then GRO goes off, so
// nothing coalesced is left to a successor that may not split it (ADR 0192).
// A drain still running when the budget ends has GRO switched off at once, and
// the close that follows cuts it.
func (s *Server) handoverPackets(ctx context.Context) {
	s.mu.RLock()
	conns := slices.Clone(s.packetConns)
	s.mu.RUnlock()
	draining := make([]*boundPacketConn, 0, len(conns))
	//: every drain started first, so they run side by side.
	for _, bound := range conns {
		//: only a socket that coalesced has a drain to wait for.
		if bound.handover() {
			draining = append(draining, bound)
		}
	}
	wait, cancel := context.WithTimeout(ctx, handoverBudget)
	defer cancel()
	//: each drain ends at EAGAIN; the budget bounds the wait.
	for _, bound := range draining {
		select {
		//: drained: GRO is off, its successor reads from here.
		case <-bound.done:
		//: out of budget: GRO off now, the close that follows cuts the drain.
		case <-wait.Done():
			bound.stopGRO()
		}
	}
}

// recordGSOOff names group among those sending without GSO, once.
func (s *Server) recordGSOOff(group string) {
	s.gsoMu.Lock()
	defer s.gsoMu.Unlock()
	//: one fault per group, however many of its sockets were refused.
	if !slices.Contains(s.gsoOff, group) {
		s.gsoOff = append(s.gsoOff, group)
	}
}

// gsoFaults is one fault per group a socket of which sends without GSO.
func (s *Server) gsoFaults() []corenet.FaultValue {
	s.gsoMu.Lock()
	defer s.gsoMu.Unlock()
	var out []corenet.FaultValue
	//: each group, in the order the refusals came.
	for _, group := range s.gsoOff {
		out = append(out, corenet.FaultValue{
			Source: faultGSO, Group: group,
			Reason: "UDP GSO refused by the device (EIO: no checksum offload); replies leave one datagram a message",
		})
	}
	//: possibly none.
	return out
}

// stopGROPackets switches GRO off on every datagram socket, without a drain:
// Close's, which waits for nothing.
func (s *Server) stopGROPackets() {
	s.mu.RLock()
	conns := slices.Clone(s.packetConns)
	s.mu.RUnlock()
	//: each socket that coalesced.
	for _, bound := range conns {
		bound.stopGRO()
	}
}
