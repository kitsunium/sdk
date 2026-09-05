// Package server — the pooled datagram.
package server

import (
	"errors"
	stdnet "net"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// packet implements the domain's datagram port; the assertion is what keeps a
// signature change from silently detaching it.
var _ corenet.Packet = (*packet)(nil)

// Test_packet_Data pins that the payload is the READ BUFFER's own subslice.
//
// That aliasing is what makes batched reading allocation-free, and it is exactly
// why the port documents Data as valid only until ServePacket returns: the same
// buffer is handed to the next datagram. A copy here would be safer for a
// careless handler and would undo the entire reason the batched path exists.
func Test_packet_Data(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// payload is what the read placed in the buffer.
		payload string
	}
	tests := []tc{
		{name: "a datagram", payload: "hello"},
		{name: "an empty datagram", payload: ""},
		{name: "a binary payload", payload: "\x00\x01\x02"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		buf := []byte(c.payload)
		p := &packet{data: buf}

		got := p.Data()

		if string(got) != c.payload {
			t.Fatalf("Data = %q, want %q", got, c.payload)
		}
		//: aliased rather than copied, which is what keeps the read loop
		//: allocation-free.
		if len(got) > 0 && &got[0] != &buf[0] {
			t.Error("Data copied the read buffer instead of aliasing it")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_packet_From pins the sender, which is the only thing a handler can reply
// to on a connectionless socket.
func Test_packet_From(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// from is the sender the read reported.
		from stdnet.Addr
	}
	tests := []tc{
		{name: "a UDP sender", from: &stdnet.UDPAddr{IP: stdnet.IPv4(127, 0, 0, 1), Port: 9999}},
		{name: "a named unix peer", from: &stdnet.UnixAddr{Name: "/run/peer.sock", Net: "unixgram"}},
		//: an unbound datagram client reports no address at all, and the handler
		//: has to be able to see that rather than meet a fabricated one.
		{name: "an unnamed peer"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		p := &packet{from: c.from}
		if got := p.From(); got != c.from {
			t.Fatalf("From = %v, want %v", got, c.from)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_packet_To pins the LOCAL address, which is what tells a handler on a
// multi-homed host which of its own sockets a datagram arrived on — the
// information it needs to answer from the same one.
func Test_packet_To(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// local is the address the socket reports as its own.
		local stdnet.Addr
	}
	tests := []tc{
		{name: "a bound UDP socket", local: &stdnet.UDPAddr{IP: stdnet.IPv4(10, 0, 0, 1), Port: 5060}},
		{name: "a unix datagram socket", local: &stdnet.UnixAddr{Name: "/run/svc.sock", Net: "unixgram"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		p := &packet{conn: &fakePacketConn{local: c.local}}
		if got := p.To(); got != c.local {
			t.Fatalf("To = %v, want %v", got, c.local)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_packet_ID pins the correlation identifier, which is what ties a handler's
// own log lines to the engine's on a socket that has no connection to name.
func Test_packet_ID(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// id is what the read loop assigned.
		id uint64
	}
	tests := []tc{
		{name: "the first datagram", id: 1},
		{name: "a later datagram", id: 1 << 40},
		{name: "a reset value", id: 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		p := &packet{id: c.id}
		if got := p.ID(); got != c.id {
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

// Test_packet_Group pins the group name, the metrics and log dimension every
// datagram carries.
func Test_packet_Group(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// group is the owning group's name.
		group string
	}
	tests := []tc{
		{name: "a named group", group: "sip"},
		{name: "a reset value", group: ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		p := &packet{group: c.group}
		if got := p.Group(); got != c.group {
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

// Test_packet_Reply pins that the answer goes back to the SENDER of this
// datagram, on the socket it arrived on.
//
// It exists so the overwhelmingly common case needs no reference to the
// underlying socket, which the handler has no other way to reach. Getting the
// address wrong here would send one peer's answer to another, on a protocol
// where nothing downstream would notice.
func Test_packet_Reply(t *testing.T) {
	t.Parallel()
	broken := errors.New("use of closed network connection")
	sender := &stdnet.UDPAddr{IP: stdnet.IPv4(192, 0, 2, 7), Port: 5060}

	type tc struct {
		// name describes the case.
		name string
		// payload is the answer the handler writes.
		payload string
		// writeErr is what the socket reports.
		writeErr error
	}
	tests := []tc{
		{name: "an answer", payload: "pong"},
		{name: "an empty answer", payload: ""},
		{name: "an invalid write", payload: "pong", writeErr: broken},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		socket := &fakePacketConn{writeErr: c.writeErr}
		p := &packet{conn: socket, from: sender}

		n, err := p.Reply([]byte(c.payload))

		//: the answer must be addressed to the sender of THIS datagram.
		if socket.writtenTo != sender {
			t.Fatalf("the reply went to %v, want %v", socket.writtenTo, sender)
		}
		if string(socket.written) != c.payload {
			t.Fatalf("the reply carried %q, want %q", socket.written, c.payload)
		}
		if !errors.Is(err, c.writeErr) {
			t.Fatalf("Reply = %v, want %v", err, c.writeErr)
		}
		//: a failed write reports nothing sent, so a handler counting bytes is
		//: not misled.
		if c.writeErr != nil {
			if n != 0 {
				t.Errorf("a failed reply reported %d bytes", n)
			}
			return
		}
		if n != len(c.payload) {
			t.Errorf("Reply reported %d bytes of %d", n, len(c.payload))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_packet_reset pins that recycling drops EVERY reference.
//
// The value is reused for the life of the read loop, so anything it keeps is
// pinned until the socket closes: a payload reference pins a read buffer, and a
// socket reference pins a descriptor the loop has already finished with.
func Test_packet_reset(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// filled populates every field before the reset.
		filled bool
	}
	tests := []tc{
		{name: "a served datagram", filled: true},
		//: resetting a value that was never filled must be harmless too.
		{name: "a value that was never used"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		p := &packet{}
		if c.filled {
			p.data = []byte("payload")
			p.from = &stdnet.UDPAddr{IP: stdnet.IPv4(127, 0, 0, 1), Port: 1}
			p.conn = &fakePacketConn{}
			p.id = 7
			p.group = "sip"
		}

		p.reset()

		//: a retained payload pins the read buffer it points into.
		if p.data != nil || p.from != nil {
			t.Errorf("the value still holds a payload or a sender: %v / %v", p.data, p.from)
		}
		//: a retained socket pins a descriptor the loop has finished with.
		if p.conn != nil {
			t.Error("the value still holds its socket")
		}
		if p.id != 0 || p.group != "" {
			t.Errorf("the value still identifies as %d/%q", p.id, p.group)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
