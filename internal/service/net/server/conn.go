// Package server — the pooled stream connection.
package server

import (
	stdnet "net"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// conn is the domain's view of one accepted connection.
//
// It embeds stdnet.Conn so every call the domain does not bound reaches the
// socket directly. Read and Write are overridden because the group's deadlines
// are documented as per-OPERATION bounds: installed once at accept time they
// would be a budget for the connection's whole life, which is a different and
// much weaker promise — a peer that sends one byte per second stays inside a
// lifetime budget for as long as the budget lasts, and outside a per-read one
// immediately.
type conn struct {
	stdnet.Conn
	// id is the monotonic identifier assigned at accept time.
	id uint64
	// group names the listener group that accepted this connection.
	group string
	// scratch is the pooled read buffer handed out by Buffer.
	scratch *[]byte
	// timeouts are the group's per-phase bounds, refreshed per operation.
	timeouts corenet.TimeoutsValue
}

// Read implements net.Conn, bounding this read before it starts.
func (c *conn) Read(p []byte) (n int, err error) {
	c.boundRead()
	//: the socket's own outcome, now under a deadline that belongs to THIS read.
	return c.Conn.Read(p)
}

// Write implements net.Conn, bounding this write before it starts.
func (c *conn) Write(p []byte) (n int, err error) {
	c.boundWrite()
	//: the socket's own outcome, now under a deadline that belongs to THIS write.
	return c.Conn.Write(p)
}

// boundRead installs the deadline for the read about to start.
func (c *conn) boundRead() {
	//: a read must respect BOTH budgets: it may not run longer than the read
	//: timeout, and it may not leave the connection silent past the idle one.
	//: Both are measured from now — now is exactly when the previous activity
	//: ended — so the tighter of the two is the honest bound. Installing them
	//: one after another, as the accept-time version did, simply overwrote the
	//: first with the second: a deadline is an instant, not a nested budget.
	budget := tighterBudget(c.timeouts.Read.Duration(), c.timeouts.Idle.Duration())
	//: no bound configured, so leave the socket alone and keep the hot path
	//: free of a poller update per read.
	if budget <= 0 {
		//: unbounded by configuration.
		return
	}
	swallowErr(c.SetReadDeadline(time.Now().Add(budget)))
}

// boundWrite installs the deadline for the write about to start.
func (c *conn) boundWrite() {
	budget := c.timeouts.Write.Duration()
	//: no bound configured, so leave the socket alone.
	if budget <= 0 {
		//: unbounded by configuration.
		return
	}
	swallowErr(c.SetWriteDeadline(time.Now().Add(budget)))
}

// tighterBudget returns the smaller of two budgets, treating zero as unbounded.
func tighterBudget(a, b time.Duration) time.Duration {
	//: an unset budget never constrains the other.
	if a <= 0 {
		//: only b can bound this operation.
		return b
	}
	//: an unset budget never constrains the other.
	if b <= 0 {
		//: only a can bound this operation.
		return a
	}
	//: both apply, so the tighter one is the effective bound.
	return min(a, b)
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

// Close closes the underlying socket, tolerating a wrapper the pool has already
// reset.
//
// net/http can close a connection after the engine has reclaimed its wrapper —
// on a drain, where the adapter releases the waiter without waiting for
// net/http to finish. Embedding net.Conn would make that a nil dereference, so
// the guard is what keeps a late close from panicking a serving goroutine.
func (c *conn) Close() error {
	//: already reset by the pool; there is nothing left to close.
	if c.Conn == nil {
		//: already reset by the pool; there is nothing left to close.
		return nil
	}
	//: the socket's own close result, which the caller decides what to do with.
	return c.Conn.Close()
}

// reset clears the connection so the pool can hand it out again. It is the
// recycler's reset hook, and it must drop every reference or a pooled entry
// would pin a closed socket for as long as the pool lives.
func (c *conn) reset() {
	c.Conn = nil
	c.id = 0
	c.group = ""
	c.scratch = nil
	c.timeouts = corenet.TimeoutsValue{}
}
