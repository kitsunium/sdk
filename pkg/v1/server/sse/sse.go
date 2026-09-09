//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/server/sse .

// Package sse is the server side of Server-Sent Events: a unidirectional
// server→client stream over an ordinary HTTP response.
//
// # The whole thing
//
//	func handler(w http.ResponseWriter, r *http.Request) {
//		stream, err := sse.New(w, r)
//		if err != nil {
//			http.Error(w, "streaming unavailable", http.StatusInternalServerError)
//			return
//		}
//		defer stream.Close()
//
//		for {
//			select {
//			case <-stream.Done():
//				return
//			case ev := <-events:
//				if err := stream.Send(sse.Event{ID: ev.Cursor, Name: "tick", Data: ev.JSON}); err != nil {
//					return
//				}
//			}
//		}
//	}
//
// It is written against net/http's own interfaces, so it works in any
// http.Handler. Mounted on this SDK's engine — Group.HandleHTTP — it
// additionally observes the server's drain signal, which is what stops an
// endless stream from holding a graceful shutdown open for its whole budget.
//
// # Why a newline is not an error
//
// The format has no escape mechanism. A line terminator inside a value is not
// quoted, it SPLITS the value across several "data:" lines, which the client
// rejoins with "\n" — so a multi-line payload is expressible and exact. The
// same absence of escaping makes a terminator inside [Event.ID] or
// [Event.Name] unrepresentable, and those are refused rather than truncated: a
// silently shortened id is a resume token pointing at the wrong place.
//
// # Reconnection, and what the SDK does not do
//
// A client stores the last non-empty id it saw and sends it back in the
// Last-Event-ID header when it reconnects. [Stream.LastEventID] hands that
// cursor to the handler. Nothing is replayed from it, on purpose.
//
// A replay buffer held by the stream would be empty at exactly the moment a
// resume needs it — a reconnect is a NEW connection and therefore a new stream.
// A buffer that outlived the stream would be an application store: it has to
// know how many events to keep, how long they stay valid, and whether replaying
// them is even safe, which are questions about what the events MEAN and not
// about the transport. So the cursor is handed over and the handler resumes
// from its own log. Minting ids is the handler's job for the same reason: an id
// the SDK invented would be a number that means nothing to the application the
// client sends it back to.
//
// # Keep-alive
//
// An idle stream is indistinguishable from a dead one to a proxy counting idle
// seconds. [Stream] therefore sends a periodic comment — ignored by every
// client, so it can never be mistaken for an event. The interval defaults to
// [DefaultKeepAlive]; a zero passed to [KeepAlive] is clamped to it rather than
// silently meaning "never", and "never" is spelled [WithoutKeepAlive].
//
// # Shutdown
//
// [Stream.Done] closes when the client disconnects, when the handler calls
// [Stream.Close], or when the server begins draining. [Stream.Send] refuses
// once it has, so a handler that only ever calls Send in a loop terminates too.
// Both halves matter: without the second, one handler shape could still hold a
// graceful shutdown open until its budget expired.
//
// # Clients
//
// This package is the server half only. The SDK's outbound client returns a
// fully-read body by design and cannot consume a stream that never ends; see
// the package CLAUDE.md for why that is a separate decision rather than an
// omission.
package sse

import (
	"context"
	"net/http"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	svcsse "github.com/kitsunium/sdk/internal/service/net/sse"
)

// ContentType is the media type an event stream is served under. A client that
// receives anything else does not open an EventSource at all.
const ContentType string = corenet.SSEContentType

// LastEventIDHeader is the request header a reconnecting client sends, carrying
// the id of the last event it processed.
const LastEventIDHeader string = corenet.SSELastEventIDHeader

// MinRetry is the shortest reconnection hint [Event.Retry] can carry. The wire
// field is an integer millisecond count, so anything shorter would round to
// zero — which does not mean "very soon", it means "reconnect immediately".
const MinRetry time.Duration = corenet.SSEMinRetry

// DefaultKeepAlive is the comment cadence a stream uses when the caller sets
// none. It sits under the idle timeout of every proxy worth naming.
const DefaultKeepAlive time.Duration = svcsse.DefaultKeepAlive

// DefaultWriteTimeout bounds ONE frame's write. A stream has no total write
// budget by nature; this is what stops a peer that has stopped reading from
// pinning a goroutine forever.
const DefaultWriteTimeout time.Duration = svcsse.DefaultWriteTimeout

// Sentinels returned by this package. Match with errors.Is or errs.HasCode.
var (
	// FieldInvalid reports an event the wire format cannot carry: a line
	// terminator in an id, an event name or a comment; a negative or
	// sub-millisecond retry; an event name with no data, which every client
	// discards; or a frame with no field at all.
	FieldInvalid = corenet.SSEFieldInvalid
	// FlushUnsupported reports a ResponseWriter that cannot be flushed, and so
	// cannot stream at all.
	FlushUnsupported = corenet.SSEFlushUnsupported
	// StreamClosed reports a send on a stream that has already ended — the
	// client disconnected, the server began draining, or the handler closed it.
	// It is the terminal outcome a streaming handler loops until.
	StreamClosed = corenet.SSEStreamClosed
	// StreamMisconfigured reports an option the domain refuses to interpret
	// rather than guess at, such as a negative keep-alive interval.
	StreamMisconfigured = corenet.SSEStreamMisconfigured
)

// Stream is one open event stream: an HTTP response held open, written one
// frame at a time and flushed after each. It is safe for concurrent use.
type Stream = svcsse.Stream

// Event is one Server-Sent Events frame.
type Event = corenet.SSEEventValue

// Option configures a Stream.
type Option = svcsse.Option

// New starts an event stream on w and returns it.
//
// It writes the response headers and flushes them immediately, so the client
// connects at once rather than at the first event. It fails when the response
// cannot be flushed — checked before anything is written, so the handler is
// free to answer some other way.
func New(w http.ResponseWriter, r *http.Request, opts ...Option) (stream *Stream, err error) {
	//: the service layer owns the wiring; this facade only forwards.
	return svcsse.New(w, r, opts...)
}

// KeepAlive sets the interval between keep-alive comments.
//
// Zero does not mean "never": a stream with keep-alive silently disabled works
// perfectly on a developer's loopback and dies at one minute behind a real
// proxy, which is the worst possible place to learn it. Zero is clamped to
// [DefaultKeepAlive]; a negative interval is refused. To disable it, say so
// with [WithoutKeepAlive].
func KeepAlive(d time.Duration) Option {
	//: forwarded unchanged.
	return svcsse.KeepAlive(d)
}

// WithoutKeepAlive disables the keep-alive comment entirely, so that "never" is
// something a caller writes on purpose rather than something a zero does to
// them.
func WithoutKeepAlive() Option {
	//: forwarded unchanged.
	return svcsse.WithoutKeepAlive()
}

// WriteTimeout bounds how long one frame's write may take.
//
// It is per-frame on purpose. A group's WriteTimeout is an absolute deadline
// for the whole response, which on an endless stream means the stream is cut at
// that instant; a stream replaces it with this bound, refreshed per frame. Zero
// is clamped to [DefaultWriteTimeout], negative is refused.
func WriteTimeout(d time.Duration) Option {
	//: forwarded unchanged.
	return svcsse.WriteTimeout(d)
}

// Retry sets the reconnection delay the stream advertises in its opening frame.
//
// It is the server's one chance to control how hard clients come back: a fleet
// that all reconnect on the browser default of about three seconds is a
// thundering herd aimed at a server that has just restarted. Zero sends no
// retry field.
func Retry(d time.Duration) Option {
	//: forwarded unchanged.
	return svcsse.Retry(d)
}

// DrainSignal returns the channel closed when the server serving this request
// begins draining, or nil when it publishes none.
//
// It is what [Stream] watches, exposed because an event stream is not the only
// handler that holds a connection open indefinitely — a long poll does too, and
// so will anything built on a protocol upgrade. A nil channel blocks forever,
// so a select that watches it needs no nil check.
func DrainSignal(ctx context.Context) <-chan struct{} {
	//: the core contract; this facade only forwards.
	return corenet.DrainSignal(ctx)
}
