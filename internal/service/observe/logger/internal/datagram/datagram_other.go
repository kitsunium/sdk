//go:build !linux

package datagram

import "net"

// newBatch has no batched send to offer off Linux: sendmmsg(2) is Linux's,
// and the BSDs' and darwin's sendmsg_x is not a stable interface. Every
// datagram takes its own Write.
func newBatch(net.Conn) func(payloads [][]byte) (sent int, err error) {
	//: the portable loop.
	return nil
}
