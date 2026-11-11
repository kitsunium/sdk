package server

import (
	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// PacketGroup is a set of datagram sockets sharing one handler, one middleware
// chain and one policy. It mirrors StreamGroup deliberately: the whole point of
// the domain is that a UDP service is wired the same way a TCP one is.
type PacketGroup struct {
	// name identifies the group in logs, metrics and State.
	name string
	// addrs are the addresses to bind.
	addrs []corenet.AddressValue
	// handler serves each received datagram.
	handler corenet.PacketHandler
	// middlewares decorate the handler, outermost first.
	middlewares []corenet.Middleware[corenet.PacketHandler]
	// limits bounds what the group may consume.
	limits corenet.LimitsValue
	// adopt names inherited sockets to take over instead of binding.
	adopt []string
	// batchReplies queues the replies of a read batch and sends them when it
	// ends (BatchReplies, ADR 0192).
	batchReplies bool
	// shardsSet records that Shards was given, a zero included: a group without
	// it keeps one socket per address (#977).
	shardsSet bool
	// steer is the Steer option's value, SteerHash without the option.
	steer corenet.SteerValue
	// loopHandler serves the group on the loop engine, set by HandleLoop
	// (#852); lanes are its bound sockets, gathered at Start before any lane
	// starts, so OnStart learns their count first.
	loopHandler corenet.PacketLoopHandler
	lanes       []*boundPacketConn
}

// Name returns the group's name.
func (g *PacketGroup) Name() string {
	//: fixed at declaration; used as the metrics and log dimension.
	return g.name
}

// Handle sets the group's handler, replacing any previous one.
func (g *PacketGroup) Handle(h corenet.PacketHandler) *PacketGroup {
	g.handler = h
	//: returned for chaining, so declaring a group stays a single expression.
	return g
}

// HandleFunc sets the group's handler from a plain function.
func (g *PacketGroup) HandleFunc(f corenet.PacketHandlerFunc) *PacketGroup {
	//: the func adapter already satisfies the port.
	return g.Handle(f)
}

// HandleLoop serves the group's datagrams on the loop engine (#852): one lane
// a socket, h called on the lane's own thread with every datagram and once a
// batch, its replies queued and sent together after the batch. On Linux a lane
// runs on io_uring after a probe of the running kernel — a multishot RECVMSG
// into provided buffers, SENDMSG replies, UDP_SEGMENT runs —, the row
// uring-multishot-recvmsg on in State; a probe that fails, the operator's
// KITSUNIUM_NET_DISABLE=uring-multishot-recvmsg, or an engine capped below
// io_uring, serve it on the batched reader of ADR 0192 (recvmmsg, GRO,
// sendmmsg under BatchReplies), the row absent or disabled and no fault. Every
// other platform serves it on that reader. A lane serves a UDP socket: a
// unixgram address is refused with LoopOptionUnsupported. Use's middlewares
// decorate a PacketHandler and are refused with it the same way, and a group
// given Handle or HandleFunc too is refused with HandlerAlreadySet. A panic
// of h is contained on the batched reader, as Handle's is: the datagram is
// lost and the lane goes on. On io_uring it is not, as on every loop
// thread: it ends the process, so a handler that may panic recovers itself.
func (g *PacketGroup) HandleLoop(h corenet.PacketLoopHandler) *PacketGroup {
	g.loopHandler = h
	//: returned for chaining.
	return g
}

// Use appends middlewares, outermost first.
func (g *PacketGroup) Use(middlewares ...corenet.Middleware[corenet.PacketHandler]) *PacketGroup {
	g.middlewares = append(g.middlewares, middlewares...)
	//: returned for chaining.
	return g
}

// resolved returns the handler with the group's middlewares applied.
func (g *PacketGroup) resolved() corenet.PacketHandler {
	//: composed once at Start rather than per datagram, so the chain costs
	//: nothing on the hot path.
	return corenet.Chain(g.handler, g.middlewares...)
}
