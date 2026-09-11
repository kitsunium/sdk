// Package sse is the server side of Server-Sent Events (ADR 0029): a
// unidirectional server→client stream over an ordinary HTTP response, carrying
// text/event-stream frames until either end decides it is over.
//
// It is written against net/http's own interfaces, not against this SDK's
// listener engine, so it works on any http.Handler. Mounted on the SDK engine
// it additionally observes the server's drain signal, which is what stops an
// endless stream from holding a graceful shutdown open for its whole budget.
package sse

import (
	"context"
	"net/http"
	"sync"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/worker"
)

// initialFrameCapacity pre-sizes the per-stream encode buffer. One frame of a
// few hundred bytes is the overwhelmingly common shape, and the buffer is
// reused for the stream's whole life, so a steady-state send allocates nothing.
const initialFrameCapacity int = 512

// keepAliveComment is the text of the periodic keep-alive. It is a comment,
// which every client ignores by construction, so it can never be mistaken for
// an event by a consumer that forgot to filter it.
const keepAliveComment string = "keep-alive"

// Stream is one open event stream: an HTTP response held open, written one
// frame at a time and flushed after each.
//
// It is safe for concurrent use. That is not decoration — the keep-alive runs
// on its own goroutine and shares the response writer with the handler, and a
// handler that fans events in from several producers is the normal shape.
type Stream struct {
	// w is the response being streamed.
	w http.ResponseWriter
	// rc drives Flush and SetWriteDeadline through net/http's own controller,
	// so a wrapped ResponseWriter that forwards Unwrap keeps working.
	rc *http.ResponseController
	// lastEventID is the id the client says it last processed, taken from the
	// request header at construction.
	lastEventID string
	// writeTimeout bounds one frame's write, refreshed per frame.
	writeTimeout time.Duration
	// deadlines records whether the response supports write deadlines. A
	// recorder or a hand-rolled ResponseWriter does not, and that is not a
	// failure: the stream simply cannot bound its writes and says so here
	// rather than refusing to run.
	deadlines bool

	// mu serialises frame writes. Every write must be whole: two interleaved
	// frames are not two events, they are one corrupt one.
	mu sync.Mutex
	// frame is the reusable encode buffer, guarded by mu.
	frame []byte

	// done is closed exactly once, when the stream ends for any reason.
	done chan struct{}
	// endOnce guards close(done) against the several goroutines that can end a
	// stream: the handler, the keep-alive, and the watcher.
	endOnce sync.Once
	// pinger owns the keep-alive goroutine; nil when keep-alive is disabled.
	pinger *worker.LoopDaemon
	// watcher owns the goroutine translating the request context and the
	// server's drain signal into done.
	watcher *worker.LoopDaemon
}

// New starts an event stream on w and returns it.
//
// It writes the response headers and flushes them immediately, so the client's
// EventSource opens at once rather than at the first event — which on a stream
// whose first event may be minutes away is the difference between working and
// appearing hung.
//
// It fails when the response cannot be flushed. That is checked before anything
// is written, because a stream that cannot flush is not a slow stream: every
// frame sits in the transport buffer until the handler returns, and a handler
// that never returns delivers nothing at all.
func New(w http.ResponseWriter, r *http.Request, opts ...Option) (stream *Stream, err error) {
	cfg, cerr := resolve(opts)
	//: a refused option set never reaches the wire.
	if cerr != nil {
		//: the error already names the offending option.
		return nil, cerr
	}
	//: probed BEFORE a single header is set, so a response that cannot stream
	//: is left untouched for the handler to answer some other way. Calling
	//: Flush to find out would commit a 200 with no content type.
	if !canFlush(w) {
		//: refuse now; nothing has been written.
		return nil, errs.Wrap(corenet.SSEFlushUnsupported, errs.WrapParams{},
			errs.String("why", "the ResponseWriter implements neither FlushError nor http.Flusher"))
	}
	s := &Stream{
		w:            w,
		rc:           http.NewResponseController(w),
		lastEventID:  r.Header.Get(corenet.SSELastEventIDHeader),
		writeTimeout: cfg.writeTimeout,
		frame:        make([]byte, 0, initialFrameCapacity),
		done:         make(chan struct{}),
	}
	//: a response that cannot carry deadlines is served without them rather
	//: than refused: the streaming contract is Flush, not SetWriteDeadline.
	s.deadlines = s.rc.SetWriteDeadline(time.Time{}) == nil
	setStreamHeaders(w.Header())
	//: the headers go out now, so the client sees an open stream immediately.
	if ferr := s.rc.Flush(); ferr != nil {
		//: report the flush failure with the same code the probe would have.
		return nil, errs.Wrap(corenet.SSEFlushUnsupported, errs.WrapParams{},
			errs.String("cause", ferr.Error()))
	}
	s.watch(r.Context())
	//: the opening retry hint, when the caller set one.
	if cfg.retry != 0 {
		//: a failed opening frame means the peer is already gone.
		if serr := s.Send(corenet.SSEEventValue{Retry: corenet.DurationValue(cfg.retry)}); serr != nil {
			//: hand back the stream's own terminal error.
			return nil, serr
		}
	}
	s.startKeepAlive(cfg)
	//: open, flushed and watched.
	return s, nil
}

// LastEventID returns the id the client says it last processed, or "" for a
// fresh connection.
//
// The SDK does NOT replay from it, and the shape of the problem is why. A
// replay buffer held by the stream would be useless by construction: a
// reconnect is a NEW connection and therefore a new stream, so its buffer is
// always empty at exactly the moment a resume needs it. A buffer that outlived
// the stream would be an application store — it has to know how many events to
// keep, how long they stay valid, and whether replaying them is even safe,
// which is a question about the events' meaning and not about the transport.
// The honest thing is to hand the cursor to the handler and let it resume from
// its own log.
func (s *Stream) LastEventID() string {
	//: captured at construction; the request header cannot change afterwards.
	return s.lastEventID
}

// Done returns the channel closed when the stream has ended: the client
// disconnected, the server began draining, or Close was called.
//
// A streaming handler selects on it. Send refuses once it is closed as well, so
// a handler that only ever calls Send in a loop still terminates — but a
// handler that watches this can stop between events instead of discovering it
// on the next one.
func (s *Stream) Done() <-chan struct{} {
	//: read-only, so nobody but the stream can end it.
	return s.done
}

// Send writes one event and flushes it.
//
// The frame is validated before anything is written, so an unrepresentable
// event cannot leave half of itself on a stream the peer is already parsing.
func (s *Stream) Send(event corenet.SSEEventValue) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: the terminal check comes first, so a handler looping on Send sees the
	//: same reason to stop whatever the event was.
	if err := s.ended(); err != nil {
		//: the stream is over.
		return err
	}
	frame, err := event.AppendTo(s.frame[:0])
	//: an invalid frame leaves the buffer untouched by contract.
	if err != nil {
		//: nothing was written; report what the format cannot carry.
		return err
	}
	s.frame = frame
	//: one whole frame, then a flush.
	return s.write(frame)
}

// Comment writes a comment frame — ignored by every client — and flushes it.
//
// It is what keep-alive is made of, and it is exported because a caller
// sometimes needs to prove liveness at a moment of its own choosing: right
// after the headers, or after a long computation it knows took too long.
func (s *Stream) Comment(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	//: the terminal check comes first, exactly as it does for an event.
	if err := s.ended(); err != nil {
		//: the stream is over.
		return err
	}
	frame, err := corenet.AppendSSEComment(s.frame[:0], text)
	//: an invalid comment leaves the buffer untouched by contract.
	if err != nil {
		//: nothing was written; report what the format cannot carry.
		return err
	}
	s.frame = frame
	//: one whole frame, then a flush.
	return s.write(frame)
}

// Close ends the stream and joins its goroutines.
//
// A handler should defer it. Returning from the handler also ends the stream —
// net/http cancels the request context — but Close makes the moment explicit
// and joins the keep-alive rather than leaving it to notice.
func (s *Stream) Close() error {
	s.end()
	//: joining is safe here because Close is the handler's call; the
	//: keep-alive and the watcher end the stream through end, never through
	//: Close, so neither can ever be joining itself.
	if s.pinger != nil {
		s.pinger.Stop()
	}
	//: the watcher is always started, so it is always joined.
	s.watcher.Stop()
	//: closing an event stream cannot fail; the signature matches io.Closer so
	//: `defer stream.Close()` reads like every other resource in Go.
	return nil
}

// write puts one already-encoded frame on the wire and flushes it. The caller
// holds s.mu.
func (s *Stream) write(frame []byte) error {
	//: refreshed per frame rather than set once: a stream has no total write
	//: budget by nature, but ONE write must still be bounded or a peer that
	//: stopped reading pins this goroutine and a socket buffer forever.
	if s.deadlines {
		//: a deadline that cannot be set now is not worth failing the frame
		//: over; the write below reports any real problem.
		if derr := s.rc.SetWriteDeadline(time.Now().Add(s.writeTimeout)); derr != nil {
			s.deadlines = false
		}
	}
	//: a failed write means the peer is gone; there is no recovering a stream
	//: whose reader has stopped listening.
	if _, err := s.w.Write(frame); err != nil {
		s.end()
		//: report the terminal state with the cause attached.
		return errs.Wrap(corenet.SSEStreamClosed, errs.WrapParams{},
			errs.String("cause", err.Error()))
	}
	//: without the flush the frame sits in the transport buffer until the
	//: handler returns — which on an endless stream is never.
	if err := s.rc.Flush(); err != nil {
		s.end()
		//: report the terminal state with the cause attached.
		return errs.Wrap(corenet.SSEStreamClosed, errs.WrapParams{},
			errs.String("cause", err.Error()))
	}
	//: delivered.
	return nil
}

// ended reports the stream's terminal error, or nil while it is open.
func (s *Stream) ended() error {
	select {
	//: the stream has ended for one of the three reasons watch translates.
	case <-s.done:
		//: refuse the write; the handler's loop reads this as "stop".
		return errs.Wrap(corenet.SSEStreamClosed, errs.WrapParams{})
	default:
	}
	//: still open.
	return nil
}

// end closes done exactly once, whichever goroutine gets there first.
func (s *Stream) end() {
	//: several goroutines can end a stream; only one may close the channel.
	s.endOnce.Do(func() {
		//: every observer — Send, Comment, Done, the keep-alive — sees this.
		close(s.done)
	})
}

// watch starts the goroutine that turns the request's end and the server's
// drain into this stream's end.
//
// Goroutine lifecycle: exactly one per stream, owned by the Stream, joined by
// Close. It returns as soon as the stream ends, whatever ended it.
func (s *Stream) watch(ctx context.Context) {
	//: nil when the server publishes no drain signal — receiving from a nil
	//: channel blocks forever, which is exactly what an absent signal should
	//: do, so no branch is needed here or below.
	draining := corenet.DrainSignal(ctx)
	s.watcher = worker.Start(func(stop <-chan struct{}) {
		select {
		//: Close is joining us.
		case <-stop:
		//: the stream already ended some other way.
		case <-s.done:
		//: the client went away, or net/http finished with the request.
		case <-ctx.Done():
			s.end()
		//: the server began draining. This is the whole reason the signal
		//: exists: an endless stream that ignored it would hold a graceful
		//: shutdown open until the drain budget expired and the socket was
		//: severed under it — every time, on every deploy.
		case <-draining:
			s.end()
		}
	})
}

// startKeepAlive spawns the periodic comment writer, unless it is disabled.
//
// Goroutine lifecycle: at most one per stream, owned by the Stream and joined
// by Close. It exits on the stream's own end, so it cannot outlive the response
// it writes to.
func (s *Stream) startKeepAlive(cfg config) {
	//: an explicitly disabled keep-alive starts no goroutine at all, so it
	//: costs nothing rather than costing a parked ticker.
	if cfg.noKeepAlive {
		//: nothing to start.
		return
	}
	s.pinger = worker.Start(func(stop <-chan struct{}) {
		//: the loop owns the ticker so it stops exactly when the loop returns.
		ticker := time.NewTicker(cfg.keepAlive)
		//: release the ticker once the loop returns.
		defer ticker.Stop()
		//: comment until the stream ends or Close joins us.
		for {
			select {
			//: Close is joining us.
			case <-stop:
				//: nothing more to send.
				return
			//: the stream ended; the response is no longer ours to write to.
			case <-s.done:
				//: nothing more to send.
				return
			case <-ticker.C:
				//: a failed keep-alive means the peer is gone — Comment has
				//: already ended the stream, so there is nothing left to do
				//: but exit. end is called rather than Close: Close joins this
				//: very goroutine.
				if err := s.Comment(keepAliveComment); err != nil {
					s.end()
					//: the stream is over.
					return
				}
			}
		}
	})
}

// setStreamHeaders puts the response in event-stream mode.
func setStreamHeaders(h http.Header) {
	//: the media type IS the protocol handshake: a client that receives
	//: anything else refuses to open an EventSource at all, so it is set
	//: unconditionally rather than left to the caller.
	h.Set("Content-Type", corenet.SSEContentType)
	//: the two below are advisory, so a caller who set them on purpose keeps
	//: what it chose. A cached event stream is a stream that never updates.
	if h.Get("Cache-Control") == "" {
		h.Set("Cache-Control", "no-cache")
	}
	//: nginx buffers proxied responses by default, which turns a real-time
	//: stream into a batch delivered at close. This header is the documented
	//: opt-out and is ignored by everything else, so it is safe to send always.
	if h.Get("X-Accel-Buffering") == "" {
		h.Set("X-Accel-Buffering", "no")
	}
	//: Connection is deliberately NOT set. It is hop-by-hop, net/http manages
	//: it for HTTP/1.1, and it is forbidden outright over HTTP/2 — a stream
	//: that set it would be broken on the transport most likely to carry it.
}

// canFlush reports whether the response can be flushed, walking the same
// unwrap chain http.ResponseController does.
//
// It exists because ResponseController answers the question only by ANSWERING
// it — calling Flush on a supported writer commits the response — and the whole
// point is to refuse before anything is written. The recognised forms mirror
// net/http's own: FlushError, and the older http.Flusher.
func canFlush(w http.ResponseWriter) bool {
	//: walk the wrapper chain the same way the controller does.
	for {
		//: the type decides whether this link in the chain can flush, or
		//: whether there is another link underneath it to look at.
		switch unwrapped := w.(type) {
		//: either form is a flusher; net/http accepts both, so we must too.
		case interface{ FlushError() error }, http.Flusher:
			//: this link streams, so the chain does.
			return true
		//: a wrapper that exposes what it wraps.
		case interface{ Unwrap() http.ResponseWriter }:
			//: keep looking underneath — the flusher may be further down.
			w = unwrapped.Unwrap()
		//: an opaque writer with no flush and nothing beneath it.
		default:
			//: nothing in the chain can flush.
			return false
		}
	}
}
