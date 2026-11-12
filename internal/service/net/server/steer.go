package server

import (
	stdnet "net"
	"sync"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// classCode is class A-code: opt-in, what a loop group's rows call it.
const classCode string = "a-code"

// unixgramShardsRefusal refuses an explicit Shards above one on a Unix datagram
// socket: SO_REUSEPORT is an IP-socket option, and a count the socket cannot
// honour is refused rather than quietly one.
func unixgramShardsRefusal(shim *StreamGroup) error {
	//: no shard count above one was asked for.
	if !shim.kinds.has(corenet.OptionShards) || shim.limits.Shards <= 1 {
		//: nothing to refuse.
		return nil
	}
	//: every address of the group.
	for _, addr := range shim.addrs {
		//: a family with no SO_REUSEPORT.
		if !shardable(addr.Network) {
			//: refused by name.
			return errs.Wrap(corenet.PacketOptionUnsupported, errs.WrapParams{},
				errs.String("group", shim.name), errs.String("option", corenet.OptionShards.String()))
		}
	}
	//: every address can share its port.
	return nil
}

// steerProbe is what the datagram groups said of their steering program: the
// first reason one could not attach, and how many did.
type steerProbe struct {
	// mu guards the fields.
	mu sync.Mutex
	// asked and on count the groups asked and those attached.
	asked, on int
	// why is the first failure's reason.
	why string
}

// probeSteer attaches the group's steering program to its sockets, unless the
// operator switched reuseport-cbpf off, and records how that went for its row.
// A group without Steer asks for nothing; one socket has nothing to steer.
func (s *Server) probeSteer(steer corenet.SteerValue, sockets []stdnet.PacketConn) {
	//: the kernel's hash, nothing to spread, or switched off by name.
	if steer != corenet.SteerCPU || s.operator.kernelDisabled(corenet.CapabilityReuseportCBPF) {
		return
	}
	s.steer.mu.Lock()
	defer s.steer.mu.Unlock()
	s.steer.asked++
	err := attachSteer(sockets)
	//: taken.
	if err == nil {
		s.steer.on++
		return
	}
	//: the first refusal explains the row.
	if s.steer.why == "" {
		s.steer.why = err.Error()
	}
}

// steerCapability is the reuseport-cbpf row, or none while no group asked.
func (s *Server) steerCapability() []corenet.CapabilityValue {
	row := corenet.CapabilityValue{Name: corenet.CapabilityReuseportCBPF, Class: classCode}
	//: switched off: the row says so whatever groups exist.
	if s.operator.kernelDisabled(corenet.CapabilityReuseportCBPF) {
		_, row.Reason = s.operator.kernelSwitches()
		row.Status = statusDisabled
		//: disabled.
		return []corenet.CapabilityValue{row}
	}
	s.steer.mu.Lock()
	defer s.steer.mu.Unlock()
	switch {
	//: no group asked.
	case s.steer.asked == 0:
		//: no row.
		return nil
	//: attached.
	case s.steer.on > 0:
		row.Status = statusOn
	//: every group refused: the kernel's hash still spreads them.
	default:
		row.Status, row.Reason = statusAbsent, s.steer.why
	}
	//: the row.
	return []corenet.CapabilityValue{row}
}
