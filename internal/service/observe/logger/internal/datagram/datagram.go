package datagram

import "net"

// MaxBatch is how many datagrams one sendmmsg(2) carries at most; a longer
// backlog takes several calls.
const MaxBatch int = 64

// BatchSender sends payloads as datagrams on one connected socket, reusing
// one scratch across calls. It is NOT safe for concurrent use: the sink that
// owns it serialises its sends, as every logger sink does.
type BatchSender struct {
	// conn is the connected socket, written once per datagram when batch is
	// nil.
	conn net.Conn
	// batch is the platform's batched send — sendmmsg(2) on Linux over a
	// descriptor —, nil where there is none: it reports how many datagrams,
	// from the first, left.
	batch func(payloads [][]byte) (sent int, err error)
}

// NewBatchSender returns a BatchSender over conn, a CONNECTED datagram
// socket — no datagram names a peer. On Linux a conn that exposes its
// descriptor (syscall.Conn: *net.UDPConn, *net.UnixConn) is written with
// sendmmsg(2), under the runtime's poller; any other conn, and every other
// platform, takes one Write per datagram. The scratch one call needs is
// allocated here, once.
func NewBatchSender(conn net.Conn) *BatchSender {
	//: the platform decides; nil is the portable loop.
	return &BatchSender{conn: conn, batch: newBatch(conn)}
}

// Send sends each payload as its own datagram, in order, and reports how
// many, counted from the first, were sent. When err is non-nil,
// payloads[sent:] were not sent; when it is nil, sent is len(payloads).
// Neither payloads nor their bytes are retained after Send returns.
func (s *BatchSender) Send(payloads [][]byte) (sent int, err error) {
	//: one system call per MaxBatch datagrams where the kernel has one.
	if s.batch != nil {
		//: the batched path.
		return s.batch(payloads)
	}
	//: one Write per datagram.
	return sendEach(s.conn, payloads)
}

// sendEach writes each payload with its own Write, which keeps it one
// datagram, and stops at the first refusal.
func sendEach(conn net.Conn, payloads [][]byte) (sent int, err error) {
	//: in order, each one datagram.
	for i, p := range payloads {
		//: the first refusal ends the batch; the ones before it left.
		if _, werr := conn.Write(p); werr != nil {
			return i, werr
		}
	}
	//: every one.
	return len(payloads), nil
}
