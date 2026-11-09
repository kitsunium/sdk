//go:build !windows

package notify

import "net"

// dialNotify connects a datagram socket to the supervisor's endpoint.
func dialNotify(addr *net.UnixAddr) (*net.UnixConn, error) {
	//: AF_UNIX SOCK_DGRAM, the protocol's transport.
	return net.DialUnix("unixgram", nil, addr)
}
