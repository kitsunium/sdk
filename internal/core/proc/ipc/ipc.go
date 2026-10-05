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
