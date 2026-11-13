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
	// SID is the peer's account on Windows — a client's read from the token
	// its pipe captured, a listener's from its process token, or from the
	// pipe's owner when that token cannot be read —; empty elsewhere.
	SID string
	// Verified is true when the kernel named the peer — UID and GID by
	// SO_PEERCRED on Linux, LOCAL_PEERCRED on FreeBSD and DragonFly,
	// LOCAL_PEEREID on NetBSD, SID from a token on Windows —, false
	// when only the directory's permissions admitted it (darwin, OpenBSD,
	// illumos, Solaris), or on Windows when a dialer named another
	// account's listener by its pipe's owner alone. On Linux, the three BSDs
	// and at a Windows listener an unnamed peer is refused, never admitted.
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
