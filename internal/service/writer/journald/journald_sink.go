// Package journald — journaldSink, the terminal datagram sink. It frames each
// record as a single "MESSAGE=<line>\n" journal entry into a reused buffer and
// sends it as one unix datagram, so the steady-state Write path allocates
// nothing (the buffer grows once, then is reused under the mutex). Non-blocking
// back-pressure is provided by the async middleware composed around it.
//
// Framing note: the native journald protocol length-prefixes multiline field
// values; this sink emits the simple "MESSAGE=<line>\n" form, which is correct
// because the SDK encoders strip CR/LF/NUL from the message — a record is always
// a single line at this layer.
package journald

import (
	"context"
	"net"
	"sync"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
)

// messagePrefix is the journal field name framing each record as the entry's
// MESSAGE. The trailing '=' separates the field name from the value.
const messagePrefix string = "MESSAGE="

// journaldSink is the unexported datagram sink behind core/logger.Sink. It owns
// the connected socket and a reused frame buffer, both guarded by mu so a Close
// never races an in-flight send.
type journaldSink struct {
	// conn is the connected unix-datagram socket to journald.
	conn net.Conn
	// mu serialises framing + send against Close.
	mu sync.Mutex
	// buf is the reused frame buffer; guarded by mu, it makes the steady-state
	// Write path allocation-free after its one-time growth.
	buf []byte
}

// newJournaldSink builds a datagram sink over an already-connected socket.
func newJournaldSink(conn net.Conn) *journaldSink {
	//: the sink owns the connection for its lifetime; Close releases it.
	return &journaldSink{conn: conn}
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
