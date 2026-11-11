//go:build !linux

package server

import (
	stdnet "net"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// portablePeer says nothing here: no peer credential this backend can verify
// with the standard library alone, so LoopPeer answers false (#974).
func portablePeer(_ stdnet.Conn) corenet.LoopPeerValue {
	//: nobody.
	return corenet.LoopPeerValue{}
}
