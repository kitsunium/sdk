package server

import (
	stdnet "net"
	"slices"
	"sync"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// destinationUnavailable is the udp-pktinfo row's reason where the socket cannot
// name a datagram's destination (ADR 0029 D5: stated, never silent).
const destinationUnavailable string = "destination address unavailable"

// rxqProbe is what the datagram sockets said of SO_RXQ_OVFL (#973): how many
// asked for it, how many took it, and the first reason one did not.
type rxqProbe struct {
	// mu guards the fields.
	mu sync.Mutex
	// tried and on count the sockets asked and the sockets that took it.
	tried, on int
	// why is the first refusal's reason.
	why string
}

// probeKernelDrops asks a bound datagram socket's source for the kernel's drop
// count, unless the operator switched udp-rxq-ovfl off: then the option is not
// set, the count stays zero and the row reads disabled.
func (s *Server) probeKernelDrops(source datagramSource) {
	//: switched off by name or by an unknown token: never asked.
	if s.operator.kernelDisabled(corenet.CapabilityUDPRxqOvfl) {
		return
	}
	on, why := enableKernelDrops(source, &s.datagrams.kernelDrops)
	s.rxq.mu.Lock()
	defer s.rxq.mu.Unlock()
	s.rxq.tried++
	//: taken.
	if on {
		s.rxq.on++
		return
	}
	//: the first refusal explains the row.
	if s.rxq.why == "" {
		s.rxq.why = why
	}
}

// datagramCapabilities is the datagram sockets' capability rows: udp-rxq-ovfl
// and udp-pktinfo, each empty while no socket concerns it and the operator left
// it alone.
func (s *Server) datagramCapabilities() []corenet.CapabilityValue {
	return slices.Concat(s.rxqCapability(), s.pktinfoCapability(), s.steerCapability(), s.laneCapability())
}

// rxqCapability is the udp-rxq-ovfl row, or none.
func (s *Server) rxqCapability() []corenet.CapabilityValue {
	row := corenet.CapabilityValue{Name: corenet.CapabilityUDPRxqOvfl, Class: classKernel}
	//: switched off: never asked, whatever sockets exist.
	if s.operator.kernelDisabled(corenet.CapabilityUDPRxqOvfl) {
		_, row.Reason = s.operator.kernelSwitches()
		row.Status = statusDisabled
		return []corenet.CapabilityValue{row}
	}
	s.rxq.mu.Lock()
	defer s.rxq.mu.Unlock()
	switch {
	//: no datagram socket was bound: nothing to report.
	case s.rxq.tried == 0:
		return nil
	//: at least one batched socket counts the kernel's drops.
	case s.rxq.on > 0:
		row.Status = statusOn
	//: every socket was refused.
	default:
		row.Status, row.Reason = statusAbsent, s.rxq.why
	}
	return []corenet.CapabilityValue{row}
}

// destinationProbe is what the wildcard datagram sockets said of IP_PKTINFO
// (#972): the first reason one did not take it, and how many took it.
type destinationProbe struct {
	// mu guards the fields.
	mu sync.Mutex
	// asked and on count the sockets asked and those that took it.
	asked, on int
	// why is the first failure's reason.
	why string
}

// probeDestination asks a bound datagram socket to name each datagram's
// destination when it is bound to a wildcard address, unless the operator
// switched udp-pktinfo off, and reports whether the reply can pin its source.
func (s *Server) probeDestination(source datagramSource, pc stdnet.PacketConn) bool {
	//: switched off by name or by an unknown token: never asked.
	if s.operator.kernelDisabled(corenet.CapabilityUDPPktinfo) {
		//: To is the bound address, the kernel chooses the source.
		return false
	}
	asked, on, why := enableDestination(source, pc)
	//: a socket bound to one address is that address.
	if !asked {
		//: nothing to pin.
		return false
	}
	s.dst.mu.Lock()
	defer s.dst.mu.Unlock()
	s.dst.asked++
	//: taken.
	if on {
		s.dst.on++
		//: the reply names its source.
		return true
	}
	//: the first refusal explains the row.
	if s.dst.why == "" {
		s.dst.why = why
	}
	//: the socket reads no destination.
	return false
}

// pktinfoCapability is the udp-pktinfo row, or none while no wildcard socket
// concerns it.
func (s *Server) pktinfoCapability() []corenet.CapabilityValue {
	row := corenet.CapabilityValue{Name: corenet.CapabilityUDPPktinfo, Class: classKernel}
	//: switched off: never asked, whatever sockets exist.
	if s.operator.kernelDisabled(corenet.CapabilityUDPPktinfo) {
		_, row.Reason = s.operator.kernelSwitches()
		row.Status = statusDisabled
		//: disabled.
		return []corenet.CapabilityValue{row}
	}
	s.dst.mu.Lock()
	defer s.dst.mu.Unlock()
	switch {
	//: no wildcard datagram socket: nothing to report.
	case s.dst.asked == 0:
		//: no row.
		return nil
	//: at least one wildcard socket reads its destinations.
	case s.dst.on > 0:
		row.Status = statusOn
	//: read one datagram a call, or off Linux: stated.
	case s.dst.why == destinationUnavailable:
		row.Status, row.Reason = statusUnprobed, s.dst.why
	//: every socket was refused.
	default:
		row.Status, row.Reason = statusAbsent, s.dst.why
	}
	//: the row.
	return []corenet.CapabilityValue{row}
}
