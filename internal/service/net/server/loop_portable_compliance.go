package server

import (
	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// A portable lane is a LoopIO that is also a LoopPeer: a handler finds it by a
// type assertion.
var _ corenet.LoopPeer = (*portableLoop)(nil)
