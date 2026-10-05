package ipc

import coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"

// The socket listener is the engine behind the Listener port, the dialer the
// engine behind the Dialer port.
var (
	_ coreipc.Listener = (*Listener)(nil)
	_ coreipc.Dialer   = (*Dialer)(nil)
)
