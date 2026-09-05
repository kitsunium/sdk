// Package server — the pooled stream connection.
package server

import (
	"errors"
	stdnet "net"
	"sync"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// fakeSocket is a net.Conn that records the deadlines installed on it.
//
// Recording them is the only way to tell a per-OPERATION bound from a
// per-connection one without waiting real time: the difference is whether a
// fresh deadline is installed before each call, not what value it holds.
type fakeSocket struct {
	// mu guards every field below, since a conn may be driven concurrently.
	mu sync.Mutex
	// readDeadlines and writeDeadlines record every deadline installed.
	readDeadlines  []time.Time
	writeDeadlines []time.Time
	// closes counts how many times the socket was closed.
	closes int
	// readErr and writeErr are what the socket reports.
	readErr  error
	writeErr error
	// deadlineErr is what setting a deadline reports.
	deadlineErr error
}

// Read implements net.Conn.
func (f *fakeSocket) Read(p []byte) (n int, err error) {
	//: a configured failure stands in for a peer that went away.
	if f.readErr != nil {
		//: nothing was read.
		return 0, f.readErr
	}
	//: one byte is enough to prove the call reached the socket.
	return len(p[:min(len(p), 1)]), nil
}

// Write implements net.Conn.
func (f *fakeSocket) Write(p []byte) (n int, err error) {
	//: a configured failure stands in for a broken pipe.
	if f.writeErr != nil {
		//: nothing was written.
		return 0, f.writeErr
	}
	//: the whole buffer reached the socket.
	return len(p), nil
}

// Close implements net.Conn.
func (f *fakeSocket) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
	//: closing a memory-backed socket cannot fail.
	return nil
}

// LocalAddr implements net.Conn.
func (f *fakeSocket) LocalAddr() stdnet.Addr {
	//: no address is needed by anything under test here.
	return nil
}

// RemoteAddr implements net.Conn.
func (f *fakeSocket) RemoteAddr() stdnet.Addr {
	//: no address is needed by anything under test here.
	return nil
}

// SetDeadline implements net.Conn.
func (f *fakeSocket) SetDeadline(t time.Time) error {
	//: both halves, as net.Conn documents it.
	if err := f.SetReadDeadline(t); err != nil {
		//: the read half refused.
		return err
	}
	//: the write half decides the outcome.
	return f.SetWriteDeadline(t)
}

// SetReadDeadline implements net.Conn.
func (f *fakeSocket) SetReadDeadline(t time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readDeadlines = append(f.readDeadlines, t)
	//: a configured failure stands in for a socket that is already gone.
	return f.deadlineErr
}

// SetWriteDeadline implements net.Conn.
func (f *fakeSocket) SetWriteDeadline(t time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeDeadlines = append(f.writeDeadlines, t)
	//: a configured failure stands in for a socket that is already gone.
	return f.deadlineErr
}

// reads returns how many read deadlines were installed.
func (f *fakeSocket) reads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.readDeadlines)
}

// writes returns how many write deadlines were installed.
func (f *fakeSocket) writes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writeDeadlines)
}

// timeouts builds a TimeoutsValue from plain durations.
func timeouts(read, write, idle time.Duration) corenet.TimeoutsValue {
	return corenet.TimeoutsValue{
		Read:  corenet.DurationValue(read),
		Write: corenet.DurationValue(write),
		Idle:  corenet.DurationValue(idle),
	}
}

// Test_tighterBudget pins that ZERO means unbounded rather than "expire now".
//
// Read and Idle are independent bounds that both apply to the same read, and a
// deadline is an instant rather than a nested budget — so installing one after
// the other simply overwrites the first. Combining them here is what makes both
// promises true at once, and treating an unset one as zero would turn "no bound
// configured" into "every read times out immediately".
func Test_tighterBudget(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// a and b are the two budgets.
		a time.Duration
		b time.Duration
		// want is the effective bound.
		want time.Duration
	}
	tests := []tc{
		{name: "neither is set", a: 0, b: 0, want: 0},
		{name: "only the first is set", a: time.Second, b: 0, want: time.Second},
		{name: "only the second is set", a: 0, b: time.Second, want: time.Second},
		{name: "the first is tighter", a: time.Second, b: 5 * time.Second, want: time.Second},
		{name: "the second is tighter", a: 5 * time.Second, b: time.Second, want: time.Second},
		{name: "they are equal", a: time.Second, b: time.Second, want: time.Second},
		//: a negative budget is as unset as zero; it must not win by being small.
		{name: "an invalid first budget", a: -time.Second, b: time.Second, want: time.Second},
		{name: "an invalid second budget", a: time.Second, b: -time.Second, want: time.Second},
		{name: "both invalid", a: -time.Second, b: -2 * time.Second, want: -2 * time.Second},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := tighterBudget(c.a, c.b); got != c.want {
			t.Fatalf("tighterBudget(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_conn_boundRead pins that a read is bounded by BOTH budgets, freshly, and
// that an unconfigured group touches the socket not at all.
//
// The deadlines are documented as per-OPERATION bounds. Installed once at accept
// time they would be a budget for the connection's whole life, which is a
// different and much weaker promise — a peer that sends one byte per second
// stays inside a lifetime budget for as long as it lasts, and outside a per-read
// one immediately.
func Test_conn_boundRead(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// read and idle are the group's budgets.
		read time.Duration
		idle time.Duration
		// wantInstalled is whether a deadline must reach the socket.
		wantInstalled bool
		// wantBudget is the bound the deadline must express.
		wantBudget time.Duration
	}
	tests := []tc{
		//: no bound configured, so the socket is left alone and the hot path
		//: keeps a poller update per read.
		{name: "nothing configured"},
		{name: "only a read budget", read: time.Second, wantInstalled: true, wantBudget: time.Second},
		{name: "only an idle budget", idle: 2 * time.Second, wantInstalled: true, wantBudget: 2 * time.Second},
		{
			//: the tighter of the two is the honest bound; installing them one
			//: after the other would silently keep only the second.
			name: "the read budget is tighter",
			read: time.Second, idle: 10 * time.Second,
			wantInstalled: true, wantBudget: time.Second,
		},
		{
			name: "the idle budget is tighter",
			read: 10 * time.Second, idle: time.Second,
			wantInstalled: true, wantBudget: time.Second,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		socket := &fakeSocket{}
		wrapper := &conn{Conn: socket, timeouts: timeouts(c.read, 0, c.idle)}
		before := time.Now()

		wrapper.boundRead()

		if !c.wantInstalled {
			if socket.reads() != 0 {
				t.Fatalf("an unbounded group installed %d read deadlines", socket.reads())
			}
			return
		}
		if socket.reads() != 1 {
			t.Fatalf("boundRead installed %d deadlines, want 1", socket.reads())
		}
		//: the deadline is measured from NOW — which is exactly when the previous
		//: activity ended — so it lands at the budget plus however long the call
		//: itself took, and never before the budget.
		got := socket.readDeadlines[0].Sub(before)
		if got < c.wantBudget || got > c.wantBudget+time.Second {
			t.Fatalf("the deadline is %v away, want about %v", got, c.wantBudget)
		}
		//: a second read gets its own budget rather than inheriting the first's.
		wrapper.boundRead()
		if socket.reads() != 2 {
			t.Fatalf("the second read installed %d deadlines in total, want 2 — "+
				"the bound is per connection rather than per read", socket.reads())
		}
		if !socket.readDeadlines[1].After(socket.readDeadlines[0]) {
			t.Error("the second read reused the first read's deadline")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_conn_boundWrite pins the write half. It deliberately does NOT combine
// with the idle budget: idleness is about a peer that has stopped sending, and a
// write that is slow because the peer's window is full is a different condition
// with a different remedy.
func Test_conn_boundWrite(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// write and idle are the group's budgets.
		write time.Duration
		idle  time.Duration
		// wantInstalled is whether a deadline must reach the socket.
		wantInstalled bool
	}
	tests := []tc{
		{name: "nothing configured"},
		//: the idle budget belongs to reads; it must not bound a write.
		{name: "only an idle budget", idle: time.Second},
		{name: "a write budget", write: time.Second, wantInstalled: true},
		{name: "both budgets", write: time.Second, idle: 10 * time.Second, wantInstalled: true},
		{name: "an invalid write budget", write: -time.Second},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		socket := &fakeSocket{}
		wrapper := &conn{Conn: socket, timeouts: timeouts(0, c.write, c.idle)}
		before := time.Now()

		wrapper.boundWrite()

		if !c.wantInstalled {
			if socket.writes() != 0 {
				t.Fatalf("an unbounded write installed %d deadlines", socket.writes())
			}
			return
		}
		if socket.writes() != 1 {
			t.Fatalf("boundWrite installed %d deadlines, want 1", socket.writes())
		}
		got := socket.writeDeadlines[0].Sub(before)
		if got < c.write || got > c.write+time.Second {
			t.Fatalf("the deadline is %v away, want about %v", got, c.write)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_conn_Read pins that every read passes through the bound and still reports
// the socket's own outcome. A wrapper that swallowed the failure would leave the
// handler looping on a dead connection.
func Test_conn_Read(t *testing.T) {
	t.Parallel()
	broken := errors.New("connection reset by peer")

	type tc struct {
		// name describes the case.
		name string
		// budget is the group's read bound.
		budget time.Duration
		// readErr is what the socket reports.
		readErr error
		// wantDeadlines is how many bounds must reach the socket.
		wantDeadlines int
	}
	tests := []tc{
		{name: "an unbounded read"},
		{name: "a bounded read", budget: time.Second, wantDeadlines: 1},
		{name: "an invalid read", budget: time.Second, readErr: broken, wantDeadlines: 1},
		{name: "an invalid unbounded read", readErr: broken},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		socket := &fakeSocket{readErr: c.readErr}
		wrapper := &conn{Conn: socket, timeouts: timeouts(c.budget, 0, 0)}

		n, err := wrapper.Read(make([]byte, 4))

		//: the bound is installed BEFORE the read starts, not after it returns.
		if socket.reads() != c.wantDeadlines {
			t.Fatalf("the read installed %d deadlines, want %d", socket.reads(), c.wantDeadlines)
		}
		if !errors.Is(err, c.readErr) {
			t.Fatalf("Read = %v, want %v", err, c.readErr)
		}
		if c.readErr == nil && n == 0 {
			t.Error("a successful read reported no bytes")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_conn_Write pins the same for the write half.
func Test_conn_Write(t *testing.T) {
	t.Parallel()
	broken := errors.New("broken pipe")

	type tc struct {
		// name describes the case.
		name string
		// budget is the group's write bound.
		budget time.Duration
		// writeErr is what the socket reports.
		writeErr error
		// wantDeadlines is how many bounds must reach the socket.
		wantDeadlines int
	}
	tests := []tc{
		{name: "an unbounded write"},
		{name: "a bounded write", budget: time.Second, wantDeadlines: 1},
		{name: "an invalid write", budget: time.Second, writeErr: broken, wantDeadlines: 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		socket := &fakeSocket{writeErr: c.writeErr}
		wrapper := &conn{Conn: socket, timeouts: timeouts(0, c.budget, 0)}
		payload := []byte("hello")

		n, err := wrapper.Write(payload)

		if socket.writes() != c.wantDeadlines {
			t.Fatalf("the write installed %d deadlines, want %d", socket.writes(), c.wantDeadlines)
		}
		if !errors.Is(err, c.writeErr) {
			t.Fatalf("Write = %v, want %v", err, c.writeErr)
		}
		if c.writeErr == nil && n != len(payload) {
			t.Errorf("Write reported %d bytes of %d", n, len(payload))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_conn_ID pins the correlation identifier a handler reads to tie its own
// log lines to the engine's.
func Test_conn_ID(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// id is what the accept loop assigned.
		id uint64
	}
	tests := []tc{
		{name: "the first connection", id: 1},
		{name: "a later connection", id: 4096},
		//: a recycled wrapper the pool has reset carries no identifier.
		{name: "a reset wrapper", id: 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		wrapper := &conn{id: c.id}
		if got := wrapper.ID(); got != c.id {
			t.Fatalf("ID = %d, want %d", got, c.id)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_conn_Group pins the group name, which is the metrics and log dimension
// every handler is expected to carry.
func Test_conn_Group(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// group is the owning group's name.
		group string
	}
	tests := []tc{
		{name: "a named group", group: "api"},
		{name: "a group with a path-like name", group: "internal/admin"},
		{name: "a reset wrapper", group: ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		wrapper := &conn{group: c.group}
		if got := wrapper.Group(); got != c.group {
			t.Fatalf("Group = %q, want %q", got, c.group)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_conn_Buffer pins that the scratch buffer arrives at FULL LENGTH, ready to
// read into. A zero-length slice with capacity would force every caller to
// reslice it, which is exactly the papercut Buffer exists to remove — and a
// group that asked for no buffer gets nothing rather than an allocation it never
// wanted.
func Test_conn_Buffer(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// size is the buffer the group asked for; zero means none.
		size int
		// spare is extra capacity beyond the requested size, as the pool's
		// recycled buffers routinely carry.
		spare int
	}
	tests := []tc{
		{name: "no buffer requested"},
		{name: "a small buffer", size: 64},
		{name: "a buffer with pooled spare capacity", size: 64, spare: 4032},
		{name: "a large buffer", size: 1 << 16},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		wrapper := &conn{}
		if c.size > 0 {
			wrapper.scratch = new(make([]byte, c.size, c.size+c.spare))
		}

		got := wrapper.Buffer()

		if c.size == 0 {
			//: no buffer was requested, so hand back nothing rather than allocate.
			if got != nil {
				t.Fatalf("Buffer = a %d-byte slice for a group that asked for none", len(got))
			}
			return
		}
		//: full length, not a zero-length slice the caller has to reslice.
		if len(got) != c.size {
			t.Fatalf("Buffer has length %d, want %d — the caller would have to reslice it",
				len(got), c.size)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_conn_Close pins that a LATE close is harmless.
//
// net/http can close a connection after the engine has reclaimed its wrapper —
// on a drain, where the adapter releases the waiter without waiting for net/http
// to finish. Embedding net.Conn would make that a nil dereference on a serving
// goroutine, so the guard is what turns a race into a no-op.
func Test_conn_Close(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// reclaimed drops the socket first, as the pool's reset does.
		reclaimed bool
		// wantCloses is how many closes must reach the socket.
		wantCloses int
	}
	tests := []tc{
		{name: "a live connection", wantCloses: 1},
		{name: "a wrapper the pool already reset", reclaimed: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		socket := &fakeSocket{}
		wrapper := &conn{Conn: socket}
		if c.reclaimed {
			wrapper.reset()
		}

		err := wrapper.Close()
		if err != nil {
			t.Fatalf("Close = %v, want nil", err)
		}
		if socket.closes != c.wantCloses {
			t.Fatalf("the socket was closed %d times, want %d", socket.closes, c.wantCloses)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_conn_reset pins that recycling drops EVERY reference.
//
// A pooled entry that kept its socket would pin a closed descriptor for as long
// as the pool lives, and a wrapper handed back out still carrying the previous
// connection's group name or deadlines would serve the next peer under the wrong
// identity and the wrong bounds.
func Test_conn_reset(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// filled populates every field before the reset.
		filled bool
	}
	tests := []tc{
		{name: "a fully used connection", filled: true},
		//: resetting a wrapper that was never filled must be harmless too, since
		//: the pool cannot tell the two apart.
		{name: "a wrapper that was never used"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		wrapper := &conn{}
		if c.filled {
			wrapper.Conn = &fakeSocket{}
			wrapper.id = 42
			wrapper.group = "api"
			wrapper.scratch = new(make([]byte, 8))
			wrapper.timeouts = timeouts(time.Second, time.Second, time.Second)
		}

		wrapper.reset()

		//: a pooled entry holding a socket would pin a closed descriptor.
		if wrapper.Conn != nil {
			t.Error("the wrapper still holds its socket")
		}
		if wrapper.id != 0 || wrapper.group != "" {
			t.Errorf("the wrapper still identifies as %d/%q", wrapper.id, wrapper.group)
		}
		if wrapper.scratch != nil {
			t.Error("the wrapper still holds a scratch buffer the pool has taken back")
		}
		if wrapper.timeouts != (corenet.TimeoutsValue{}) {
			t.Errorf("the wrapper still carries %+v — the next peer would be served "+
				"under the previous group's bounds", wrapper.timeouts)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
