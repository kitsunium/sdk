// Package ipc is the private socket's contract (ADR 0148, ADR 0160): who is
// at the other end of a connection, the connection that carries that answer,
// the two ports a private socket is reached through — the Listener that
// accepts and the Dialer that connects — and the codes every refusal carries.
//
// The engine is internal/service/proc/ipc: a Unix socket in a 0700 directory
// whose whole path is audited, the kernel's word on the peer where it gives
// one, a named pipe with its own DACL on Windows. Nothing here opens a socket.
// The ports exist so a caller that holds a Listener or a Dialer can be handed
// a double in a test — connections from net.Pipe with the peer the test
// chooses — instead of a socket on disk.
package ipc

import (
	"net"
)

// PeerValue is who is at the other end of a connection, as the kernel says:
// the peer's user, group and process where the kernel names them, and whether
// it did (Verified) — false where only the directory's permissions admitted it.
type PeerValue struct {
	// UID and GID are the peer's effective user and group; -1 when the
	// kernel does not say (Verified is then false).
	UID, GID int
	// PID is the peer's process, 0 when the kernel does not say.
	PID int
	// SID is the peer's account on Windows, read from its process token;
	// empty elsewhere.
	SID string
	// Verified is true when the kernel named the peer — UID and GID by
	// SO_PEERCRED on Linux, SID from the process token on Windows —, false
	// when only the directory's permissions admitted it.
	Verified bool
}

// Conn is a connection with its peer's identity: what a Listener's Accept
// returns to a listener and a Dialer's Dial to a client, Peer naming the
// other end.
type Conn struct {
	net.Conn
	// Peer is who connected (for a listener) or who listens (for a dialer).
	Peer PeerValue
}
