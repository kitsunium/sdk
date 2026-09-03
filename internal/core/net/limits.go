// Package net — the per-group resource ceilings.
package net

// LimitsValue bounds what one listener group may consume. Every field is
// optional; zero means "use the domain default", never "unbounded" — an unset
// ceiling must not be the dangerous one.
type LimitsValue struct {
	// MaxConns caps concurrent connections in the group. Beyond it, a new
	// connection is accepted and immediately closed with ConnLimitReached, which
	// is kinder than letting the accept queue silently overflow.
	MaxConns int `json:"max_conns"`
	// ReadBufferSize sizes the per-connection scratch buffer returned by
	// Conn.Buffer.
	ReadBufferSize int `json:"read_buffer_size"`
	// MaxPacketSize is the largest datagram accepted. A larger one is DROPPED
	// and counted in StateValue.OversizedPackets, never delivered truncated:
	// the kernel discards the tail, so the prefix that survives reads exactly
	// like a complete message and the handler cannot tell the difference.
	MaxPacketSize int `json:"max_packet_size"`
	// Backlog is the listen backlog depth where the platform honours it.
	Backlog int `json:"backlog"`
	// Shards is the number of independent listeners to open on one address with
	// SO_REUSEPORT, each with its own accept loop, to remove accept contention.
	// Zero selects the platform default.
	Shards int `json:"shards"`
	// BatchSize is how many datagrams to read per syscall. Zero selects the
	// default; one disables batching.
	BatchSize int `json:"batch_size"`
}
