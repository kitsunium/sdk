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
	// Backlog is the depth the kernel queues established connections at, before the
	// accept loop takes them (#978). Zero keeps the standard library's, the host's
	// somaxconn; the kernel clamps a larger value to it, and State says so. A
	// stream group sets it with the Backlog option, on both engines; Windows
	// keeps the platform's own.
	Backlog int `json:"backlog"`
	// ReceiveBuffer is the receive buffer, in bytes, the kernel is asked for on
	// the group's sockets, before bind (SO_RCVBUF): a datagram group's, a stream
	// group's listeners and so the sockets they accept (#973). Zero keeps the
	// kernel's default. The kernel doubles the value and clamps it to rmem_max;
	// State reports what it holds, in ListenerStateValue.ReceiveBuffer, and a
	// clamp is no fault.
	ReceiveBuffer int `json:"receive_buffer"`
	// SendBuffer is the send buffer, in bytes, asked as ReceiveBuffer is
	// (SO_SNDBUF, clamped to wmem_max).
	SendBuffer int `json:"send_buffer"`
	// Shards is the number of independent listeners to open on one address with
	// SO_REUSEPORT, each with its own accept loop, to remove accept contention.
	// Zero selects the platform default.
	Shards int `json:"shards"`
	// BatchSize is how many datagrams to read per syscall. Zero selects the
	// default; one disables batching.
	BatchSize int `json:"batch_size"`
}
