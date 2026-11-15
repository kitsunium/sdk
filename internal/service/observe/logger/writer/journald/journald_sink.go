package journald

import (
	"context"
	"net"
	"sync"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/service/observe/logger/internal/datagram"
)

// messagePrefix is the journal field name framing each record as the entry's
// MESSAGE. The trailing '=' separates the field name from the value.
const messagePrefix string = "MESSAGE="

// journaldSink is the unexported datagram sink behind core/observe/logger.Sink. It owns
// the connected socket and a reused frame buffer, both guarded by mu so a Close
// never races an in-flight send.
type journaldSink struct {
	// conn is the connected unix-datagram socket to journald.
	conn net.Conn
	// mu serialises framing + send against Close.
	mu sync.Mutex
	// buf is the reused frame buffer; guarded by mu, it makes the steady-state
	// Write path allocation-free after its one-time growth. WriteBatch frames
	// a whole backlog into it, one entry after the other.
	buf []byte
	// ends marks where each framed entry of a batch ends in buf, and frames
	// slices buf at those ends — both reused under mu.
	ends   []int
	frames [][]byte
	// batch sends a backlog's entries as datagrams in one sendmmsg(2) on
	// Linux (#807); guarded by mu like the socket it writes.
	batch *datagram.BatchSender
}

// newJournaldSink builds a datagram sink over an already-connected socket.
func newJournaldSink(conn net.Conn) *journaldSink {
	//: the sink owns the connection for its lifetime; Close releases it.
	return &journaldSink{conn: conn, batch: datagram.NewBatchSender(conn)}
}

// WriteBatch frames each payload as Write frames one — "MESSAGE=<line>\n" —
// into the reused buffer and sends the backlog as one datagram per entry, in
// one sendmmsg(2) on Linux (#807). It reports how many entries, from the
// first, were sent; the first refusal ends the batch. Steady state allocates
// nothing once the buffer has grown to the largest batch.
func (s *journaldSink) WriteBatch(ctx context.Context, _ []corelogger.RecordEvent, payloads [][]byte) (written int, err error) {
	//: a cancelled context sends nothing.
	if ctx != nil && ctx.Err() != nil {
		return 0, wrapWrite(ctx.Err(), 0)
	}
	s.mu.Lock()
	s.frameAll(payloads)
	sent, serr := s.batch.Send(s.frames)
	s.mu.Unlock()
	//: the entries from the refused one on were not sent.
	if serr != nil {
		undelivered := 0
		//: the bytes the caller handed for them, as Write reports a refusal.
		for _, p := range payloads[min(sent, len(payloads)):] {
			undelivered += len(p)
		}
		return sent, wrapWrite(serr, undelivered)
	}
	//: every entry.
	return len(payloads), nil
}

// frameAll frames every payload into s.buf and slices s.frames over it. The
// ends are recorded first and sliced last, because appending may move buf.
// The caller holds s.mu.
func (s *journaldSink) frameAll(payloads [][]byte) {
	s.buf = s.buf[:0]
	s.ends = s.ends[:0]
	//: each record one entry: the field name, the line without its newline,
	//: the entry's terminator.
	for _, p := range payloads {
		//: the encoder's own newline is the entry's terminator, not part of it.
		if len(p) > 0 && p[len(p)-1] == '\n' {
			p = p[:len(p)-1]
		}
		s.buf = append(s.buf, messagePrefix...)
		s.buf = append(s.buf, p...)
		s.buf = append(s.buf, '\n')
		s.ends = append(s.ends, len(s.buf))
	}
	s.frames = s.frames[:0]
	start := 0
	//: the entries, over the buffer as it finally stands.
	for _, end := range s.ends {
		s.frames = append(s.frames, s.buf[start:end])
		start = end
	}
}

// Write frames p as a single "MESSAGE=<line>\n" journal entry and sends it as
// one datagram. The frame is assembled into the reused buffer under the mutex,
// so steady-state Write allocates nothing.
func (s *journaldSink) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (n int, err error) {
	//: the accepted count reports the whole record handed in, independent of
	//: the cosmetic newline strip below, so callers see a stable byte count.
	accepted := len(p)
	//: honour cancellation so a doomed record does not waste a datagram.
	if ctx != nil && ctx.Err() != nil {
		//: surface the cancellation under the write sentinel (typed-errors rule).
		return 0, wrapWrite(ctx.Err(), accepted)
	}
	//: the text encoder already terminates the line; strip one trailing
	//: newline so framing does not double it (single-newline MESSAGE frame).
	if len(p) > 0 && p[len(p)-1] == '\n' {
		//: drop exactly one terminator; the frame appends its own below.
		p = p[:len(p)-1]
	}
	//: serialise framing + send against Close via the mutex.
	s.mu.Lock()
	//: assemble MESSAGE=<payload>\n into the reused buffer (one datagram).
	s.buf = append(s.buf[:0], messagePrefix...)
	s.buf = append(s.buf, p...)
	s.buf = append(s.buf, '\n')
	_, werr := s.conn.Write(s.buf)
	s.mu.Unlock()
	//: wrap any socket error with the write sentinel semantics.
	if werr != nil {
		//: propagate through errs.Wrap so errors.Is still catches the cause.
		return 0, wrapWrite(werr, accepted)
	}
	//: happy path — the whole payload was framed and sent.
	return accepted, nil
}

// WantsAttrs answers false to corelogger.AttrsRequester: it sends the encoder's bytes as MESSAGE and reads nothing else of the record, so the
// handler spares it the merged attributes (#1016).
func (s *journaldSink) WantsAttrs() bool {
	//: the bytes are the whole record to this sink.
	return false
}

// Flush is a no-op: each record is sent synchronously as its own datagram, so
// nothing is buffered on this side. It still honours cancellation.
func (s *journaldSink) Flush(ctx context.Context) error {
	//: honour cancellation even though there is nothing buffered to flush.
	if ctx != nil && ctx.Err() != nil {
		//: surface the cancellation under the write sentinel.
		return wrapWrite(ctx.Err(), 0)
	}
	//: datagram sends settle synchronously — nothing buffered here.
	return nil
}

// Close releases the socket under the mutex. Subsequent sends fail with the
// socket's own closed error, wrapped as JournaldWriteFailed.
func (s *journaldSink) Close() error {
	//: serialise the close against an in-flight send via the same mutex.
	s.mu.Lock()
	cerr := s.conn.Close()
	s.mu.Unlock()
	//: wrap any close error under the write sentinel semantics.
	if cerr != nil {
		//: propagate through errs.Wrap so errors.Is still catches the cause.
		return wrapWrite(cerr, 0)
	}
	//: happy path — nothing to report.
	return nil
}
