// Package rpc is a call between two processes of one machine over the private
// socket (ADR 0218): a request and its reply, or a request and the items a
// server pushes, each framed, bounded and typed by a codec format both ends
// agreed on.
//
// # The wire
//
// A client opens with a hello naming the body format, the protocol version and
// the largest frame it accepts; the server answers with its own, or with
// FORMAT_REFUSED. Then every frame is
//
//	len:u32 | kind:u8 | id:u64 | mlen:u8 | method[mlen] | body
//
// big-endian, len counting what follows it. A call's body opens with its budget
// — the nanoseconds left on the caller's deadline, zero for none — before the
// encoded value. The kinds are hello, call, stream, reply, item, end, error,
// cancel, ping and pong.
//
// # What a peer cannot do
//
// A frame's length is checked against MaxFrameBytes before a byte of its body
// is read or allocated: a length of 4 GiB closes the connection with
// FRAME_TOO_LARGE. Once its first byte arrived, a frame must be whole within
// FrameTimeout, on the injected clock, or the connection is closed: a peer
// cannot hold one on half a frame. At most MaxInFlight calls run per
// connection; the next is answered OVERLOADED at once, never queued — a server
// connection runs at most MaxInFlight + 2 goroutines. A call's budget becomes a
// deadline on the server's clock, and a client that gives up sends a cancel
// frame: the handler's context ends, and its slot is freed when it returns.
// What a handler's error says reaches the caller only through its SDK code,
// reason and public text.
package rpc
