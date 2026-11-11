package loop

import (
	"sync/atomic"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// The states of a datagram lane, which Handover and Stop move forward only.
const (
	// laneServe is a lane serving its socket.
	laneServe int32 = iota
	// laneHandover is a lane asked to drain its socket and stop.
	laneHandover
	// laneStop is a lane asked to stop now.
	laneStop
)

// PacketConfig is what server gives one datagram lane to start (#852): the
// socket, as a duplicated non-blocking descriptor the lane owns from the call
// on, started or refused, the lane's index, the group's ceiling, its handler,
// and the counters State sums.
type PacketConfig struct {
	// Group is the group's name.
	Group string
	// FD is the UDP socket's duplicated descriptor, close-on-exec.
	FD int
	// Lane is the lane's index, PacketLoopIO.Loop's answer.
	Lane int
	// Ceiling is the group's MaxPacketSize: a datagram past it is counted in
	// Counters.Oversized and never handed on.
	Ceiling int
	// Handler is the group's datagram loop handler.
	Handler corenet.PacketLoopHandler
	// Counters are where the lane counts what State reports.
	Counters PacketCountersValue
	// OnGSOOff is called once, on the lane's thread, when the device refuses a
	// UDP_SEGMENT send with EIO and the lane sends one datagram a message from
	// then on. Nil calls nothing.
	OnGSOOff func()
}

// PacketCountersValue are server's counters a datagram lane adds to, once a
// batch: every one must be set.
type PacketCountersValue struct {
	// Total counts the datagrams received.
	Total *atomic.Uint64
	// Oversized counts the datagrams past the ceiling, dropped.
	Oversized *atomic.Uint64
	// Truncated counts the messages the kernel cut, dropped.
	Truncated *atomic.Uint64
	// ReplyFailures counts the queued replies the kernel refused.
	ReplyFailures *atomic.Uint64
	// GSOOversized counts the UDP_SEGMENT sends refused with EMSGSIZE.
	GSOOversized *atomic.Uint64
	// KernelDrops adds the growth of the socket's SO_RXQ_OVFL count.
	KernelDrops *atomic.Uint64
}
