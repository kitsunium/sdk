package server

import (
	"errors"
	"sync"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/loop"
)

// networkUnixgram is the Unix datagram family, which a lane does not serve.
const networkUnixgram string = "unixgram"

// laneProbe is what the lanes of the groups HandleLoop serves said of
// io_uring (#852): how many tried, how many serve on it, and the first
// refusal's reason and whether it was the platform's.
type laneProbe struct {
	// mu guards the fields.
	mu sync.Mutex
	// tried and on count the lanes.
	tried, on int
	// why is the first refusal; platform is set when no lane exists here.
	why      string
	platform bool
	// disabled is set when the environment ruled io_uring out.
	disabled bool
}

// packetLoopRefusal refuses a group HandleLoop serves that also has a
// PacketHandler, middlewares, or a Unix datagram address.
func packetLoopRefusal(group *PacketGroup) error {
	//: not a loop group.
	if group.loopHandler == nil {
		//: nothing to refuse.
		return nil
	}
	//: two handlers.
	if group.handler != nil {
		//: refused by name.
		return errs.Wrap(corenet.HandlerAlreadySet, errs.WrapParams{}, errs.String("group", group.name))
	}
	//: middlewares decorate a PacketHandler.
	if len(group.middlewares) > 0 {
		//: refused by name.
		return errs.Wrap(corenet.LoopOptionUnsupported, errs.WrapParams{},
			errs.String("group", group.name), errs.String("option", corenet.OptionMiddleware.String()))
	}
	//: a lane serves UDP.
	for _, addr := range group.addrs {
		//: a Unix datagram address.
		if addr.Network == networkUnixgram {
			//: refused by name.
			return errs.Wrap(corenet.LoopOptionUnsupported, errs.WrapParams{},
				errs.String("group", group.name), errs.String("network", networkUnixgram))
		}
	}
	//: served.
	return nil
}

// startReader starts bound's read loop for a PacketHandler, or gathers it as
// a lane of a group HandleLoop serves.
//
// Goroutine lifecycle: one read loop per socket of a PacketHandler's group,
// owned by the Server, holding an inFlight token until Close or Shutdown
// closes its socket; a lane starts nothing here.
func (s *Server) startReader(bound *boundPacketConn, group *PacketGroup, handler corenet.PacketHandler) {
	//: a lane, started with its group's others.
	if group.loopHandler != nil {
		bound.laneIndex = len(group.lanes)
		group.lanes = append(group.lanes, bound)
		//: gathered.
		return
	}
	s.inFlight.Add(1)
	go s.readLoop(bound, group, handler)
}

// startPacketLanes tells the handler the lane count, then starts each lane:
// on io_uring when the environment allows it and the probe passes, on the
// batched reader otherwise — the row says which, and no fault is raised.
//
// Goroutine lifecycle: one per lane — a read loop, or a waiter on the io_uring
// lane's end —, owned by the Server and holding an inFlight token until the
// lane ends.
func (s *Server) startPacketLanes(group *PacketGroup) {
	//: the count first, as a stream loop handler learns its lanes.
	if st, ok := group.loopHandler.(corenet.LoopStarter); ok {
		st.OnStart(len(group.lanes))
	}
	try := s.laneAllowed()
	//: each lane.
	for _, bound := range group.lanes {
		//: io_uring, as the environment allows.
		if try && s.startURingLane(bound, group) {
			continue
		}
		s.inFlight.Add(1)
		go s.readLoop(bound, group, nil)
	}
}

// startURingLane starts bound's io_uring lane, recording the probe's verdict,
// and reports whether it serves.
//
// Goroutine lifecycle: one waiter per lane served on io_uring, holding an
// inFlight token until the lane's thread returned — on Stop, or once a
// handover drained the socket.
func (s *Server) startURingLane(bound *boundPacketConn, group *PacketGroup) bool {
	lane, err := s.startLane(s, bound, group)
	s.lanes.mu.Lock()
	defer s.lanes.mu.Unlock()
	s.lanes.tried++
	//: refused: the batched reader serves it.
	if err != nil {
		//: the first reason is the one reported.
		if s.lanes.why == "" {
			s.lanes.why, s.lanes.platform = laneReason(err)
		}
		//: not served here.
		return false
	}
	s.lanes.on++
	bound.lane = lane
	s.inFlight.Add(1)
	go s.awaitLane(bound, lane)
	//: served on io_uring.
	return true
}

// awaitLane waits for an io_uring lane's end, then marks its socket done.
func (s *Server) awaitLane(bound *boundPacketConn, lane loop.PacketLane) {
	defer s.inFlight.Done()
	<-lane.Done()
	bound.finish()
}

// laneAllowed reports whether the environment lets a lane try io_uring, and
// records it when it does not.
func (s *Server) laneAllowed() bool {
	//: switched off by name, or io_uring ruled out.
	if s.operator.kernelDisabled(corenet.CapabilityURingRecvmsg) || !s.operator.allows(corenet.CapabilityIOURing) {
		s.lanes.mu.Lock()
		s.lanes.disabled = true
		s.lanes.mu.Unlock()
		//: not tried.
		return false
	}
	//: tried.
	return true
}

// laneReason is a refusal's stage and reason, and whether it is the
// platform's.
func laneReason(err error) (string, bool) {
	e, ok := errors.AsType[*errs.Error](err)
	//: not the leaf's.
	if !ok {
		//: as rendered.
		return err.Error(), false
	}
	var stage, reason string
	//: by key.
	for _, f := range e.Fields() {
		switch f.Key() {
		case "stage":
			stage = f.StringValue()
		case "reason":
			reason = f.StringValue()
		}
	}
	//: the stage first.
	return stage + ": " + reason, stage == "arch"
}

// laneCapability is the uring-multishot-recvmsg row, or none while no group
// HandleLoop serves exists.
func (s *Server) laneCapability() []corenet.CapabilityValue {
	row := corenet.CapabilityValue{Name: corenet.CapabilityURingRecvmsg, Class: classKernel}
	s.lanes.mu.Lock()
	defer s.lanes.mu.Unlock()
	switch {
	//: the environment ruled it out.
	case s.lanes.disabled:
		_, row.Reason = s.operator.kernelSwitches()
		row.Status = statusDisabled
	//: no lane tried.
	case s.lanes.tried == 0:
		return nil
	//: every lane on io_uring.
	case s.lanes.on == s.lanes.tried:
		row.Status = statusOn
	//: no io_uring lane on this platform.
	case s.lanes.platform:
		row.Status, row.Reason = statusUnprobed, s.lanes.why
	//: a probe stage refused.
	default:
		row.Status, row.Reason = statusAbsent, s.lanes.why
	}
	return []corenet.CapabilityValue{row}
}
