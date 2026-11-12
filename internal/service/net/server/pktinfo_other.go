//go:build !linux

package server

import stdnet "net"

// enableDestination reports that a wildcard UDP socket cannot name each
// datagram's destination off Linux: the reader takes one datagram a call through
// the standard library, which reads no control message (#972).
func enableDestination(_ datagramSource, pc stdnet.PacketConn) (asked, on bool, why string) {
	udp, ok := pc.LocalAddr().(*stdnet.UDPAddr)
	//: only a socket bound to no address in particular is concerned.
	if !ok || udp.IP == nil || !udp.IP.IsUnspecified() {
		//: not asked.
		return false, false, ""
	}
	//: asked, and unavailable.
	return true, false, destinationUnavailable
}
