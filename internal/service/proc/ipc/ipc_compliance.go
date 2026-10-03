// Package ipc — the compile-time proof that the engines satisfy the core
// ports (ADR 0160): a method renamed or retyped on either engine fails the
// build here, not in a caller that holds the port.
package ipc

import coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"

// The socket listener is the engine behind the Listener port, the dialer the
// engine behind the Dialer port.
var (
	_ coreipc.Listener = (*Listener)(nil)
	_ coreipc.Dialer   = (*Dialer)(nil)
)
