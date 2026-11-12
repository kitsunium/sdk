//go:build !linux

package server

import "github.com/kitsunium/sdk/internal/service/net/loop"

// startPacketLane has no io_uring lane to start off Linux: the leaf's refusal,
// named, and the batched reader serves the socket.
func startPacketLane(_ *Server, _ *boundPacketConn, group *PacketGroup) (loop.PacketLane, error) {
	_, err := loop.StartPacket(&loop.PacketConfig{Group: group.name, FD: -1})
	//: refused, always, by the platform.
	return nil, err
}
