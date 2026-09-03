// Package server — the pooled stream connection.
package server

import stdnet "net"

// conn is the domain's view of one accepted connection.
//
// It embeds stdnet.Conn so every read, write and deadline call reaches the
// socket directly, with no wrapper on the hot path. The added fields are set
// once at accept time and read by the handler; nothing here is contended.
type conn struct {
	stdnet.Conn
	// id is the monotonic identifier assigned at accept time.
	id uint64
	// group names the listener group that accepted this connection.
	group string
	// scratch is the pooled read buffer handed out by Buffer.
	scratch *[]byte
}

// ID implements corenet.Conn.
func (c *conn) ID() uint64 {
	//: assigned once at accept time; never mutated afterwards.
	return c.id
}

// Group implements corenet.Conn.
func (c *conn) Group() string {
	//: the owning group's name, fixed for the connection's lifetime.
	return c.group
}

// Buffer implements corenet.Conn.
func (c *conn) Buffer() []byte {
	//: no buffer was requested for this group's size, so hand back nothing
	//: rather than allocating one the handler may not want.
	if c.scratch == nil {
		//: no buffer was requested, so hand back nothing rather than allocate.
		return nil
	}
	//: full length, ready to read into — a zero-length slice would force every
	//: caller to reslice it, which is exactly the papercut Buffer removes.
	return *c.scratch
}

// reset clears the connection so the pool can hand it out again. It is the
// recycler's reset hook, and it must drop every reference or a pooled entry
// would pin a closed socket for as long as the pool lives.
func (c *conn) reset() {
	c.Conn = nil
	c.id = 0
	c.group = ""
	c.scratch = nil
}
