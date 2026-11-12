package net

// LoopPeerValue is who the kernel says is on the other side of a Unix connection:
// the user, group and process that connected, as SO_PEERCRED recorded them when
// the connection was made. Verified is true when the kernel named them; a value
// the loop could not read is never returned, so Verified is true wherever a
// LoopPeer answers.
type LoopPeerValue struct {
	// UID is the peer's user id.
	UID uint32
	// GID is the peer's group id.
	GID uint32
	// PID is the peer's process id.
	PID uint32
	// Verified is true when the kernel named the peer.
	Verified bool
}
