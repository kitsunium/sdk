package loop

import (
	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// The loop is a LoopIO that is also a LoopPeer: a handler finds it by a type
// assertion.
var _ corenet.LoopPeer = (*loop)(nil)
