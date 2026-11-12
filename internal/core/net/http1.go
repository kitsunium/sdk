package net

import (
	"io"
	"time"

	"github.com/kitsunium/sdk/internal/core/observe/trace"
)

// HTTP1MaxFields is how many field lines one request of the restricted
// HTTP/1.1 profile may carry. It sizes HTTP1RequestValue.Fields, so a request is
// framed into a fixed array and never into a slice that grows: one more line is
// refused (431), never stored elsewhere.
const HTTP1MaxFields int = 64

// HTTP1MaxSubject is the longest subject an HTTP1AuthValue holds, in bytes. It
// sizes the value's array, so a subject is copied into the request and never
// aliases the bytes it was read from; a longer one is refused.
const HTTP1MaxSubject int = 128

// HTTP1MaxAccessFields is how many request fields an access-log link may
// allow: it sizes HTTP1AccessValue.Fields, so a record is a value and never a
// slice that grows.
const HTTP1MaxAccessFields int = 8

// HTTP1TraceLineLen sizes HTTP1ResponseValue.TraceLine: the field name, a
// version-00 traceparent and the line's end. Its array keeps a deferred
// answer's trace field, so it never aliases the request.
const HTTP1TraceLineLen int = 70

// HTTP1MaxRequestID is the longest request identifier an HTTP1RequestIDValue
// holds, in bytes. It sizes the value's array, so an identifier is copied by
// value and never aliases the bytes it was read from; a longer one is treated
// as absent by the chain's RequestID link.
const HTTP1MaxRequestID int = 64

// HTTP1DefaultHead is the head bound a zero HTTP1LimitsValue.Head selects: the
// request line, every field line and the blank line that ends them, in bytes.
const HTTP1DefaultHead int = 8192

// HTTP1MaxHead is the largest head bound HTTP1LimitsValue.Head may ask for.
const HTTP1MaxHead int = 65536

// HTTP1DefaultTarget is the request-target bound a zero HTTP1LimitsValue.Target
// selects, in bytes.
const HTTP1DefaultTarget int = 2048

// HTTP1DefaultBody is the Content-Length bound a zero HTTP1LimitsValue.Body
// selects: one mebibyte.
const HTTP1DefaultBody int64 = 1 << 20

// HTTP1MaxBody is the largest Content-Length bound HTTP1LimitsValue.Body may
// ask for: one gibibyte, which ten decimal digits always hold.
const HTTP1MaxBody int64 = 1 << 30

// HTTP1MaxContentLengthDigits is how many digits a Content-Length may carry,
// leading zeros included. Ten digits hold HTTP1MaxBody and stay far from the
// int64 limit, so the value is accumulated with no overflow to check: a longer
// field is refused before it is read.
const HTTP1MaxContentLengthDigits int = 10

// HTTP1LimitsValue bounds what one request of the restricted HTTP/1.1 profile
// may carry. Zero means the profile's default, never unbounded; a negative
// field, or one past its maximum, is refused when the codec is built.
type HTTP1LimitsValue struct {
	// Head bounds the request line, the field lines and the blank line that
	// ends them, in bytes: zero selects HTTP1DefaultHead, at most HTTP1MaxHead.
	Head int `json:"head"`
	// Target bounds the request-target, in bytes: zero selects
	// HTTP1DefaultTarget, and it may not exceed Head.
	Target int `json:"target"`
	// Body bounds the Content-Length a request may declare: zero selects
	// HTTP1DefaultBody, at most HTTP1MaxBody.
	Body int64 `json:"body"`
}

// HTTP1FieldValue is one field line of a request, as it arrived: its name,
// in the case the client wrote it, and its value with the surrounding
// whitespace removed. Both slices alias the bytes the request was framed from.
type HTTP1FieldValue struct {
	// Name is the field name, validated as an RFC 9110 token.
	Name []byte
	// Value is the field value, without its leading and trailing spaces and
	// tabs; it holds no NUL, CR, LF or other control byte but the tab.
	Value []byte
}

// HTTP1RequestValue is one request of the restricted HTTP/1.1 profile, framed
// by the codec (internal/service/net/http1).
//
// It is not named RequestValue: that name is the outbound request the Policy
// port judges, and the two never meet. The profile's prefix is the domain's
// way of naming a protocol's value, as SSEEventValue and WSMessageValue do.
//
// Every slice aliases the bytes the codec read: the value is valid while those
// bytes are, which on the loop engine is until OnBatchEnd returns. A handler
// that keeps a byte past that copies it. Fields is an array, so a value framed
// into a reused HTTP1RequestValue allocates nothing. Auth is a value, not a
// slice: what a link established survives the bytes it was read from.
//
// A link of a chain may read every field and set Route, Auth and
// Authenticated; writing Consumed, HeadLen or Body changes nothing, since the
// adapter frames with what Parse returned, never with what a link left.
type HTTP1RequestValue struct {
	// Method is the request method, a token.
	Method []byte
	// Target is the request-target exactly as sent, still percent-encoded.
	Target []byte
	// Host is the Host field's value; HasHost says whether it was present.
	Host []byte
	// Authorization is the Authorization field's value; HasAuthorization says
	// whether it was present.
	Authorization []byte
	// Body is the request content, Content-Length bytes after the head. It is
	// empty until the whole body has arrived.
	Body []byte
	// ContentLength is the declared body length, zero when the field is absent.
	ContentLength int64
	// HeadLen is the length of the head: the request line, the field lines and
	// the blank line. It is zero until the head is complete.
	HeadLen int
	// Consumed is HeadLen plus ContentLength: the bytes this request takes from
	// the stream, whatever its method or route. The next pipelined request
	// starts there. It is set as soon as the head is complete, before the body
	// arrives, so an early answer knows how much it has to drain.
	Consumed int
	// NFields is how many entries of Fields the request filled.
	NFields int
	// Fields holds the request's field lines in the order they arrived,
	// Host, Authorization and Content-Length included.
	Fields [HTTP1MaxFields]HTTP1FieldValue
	// HasHost reports whether the request carried a Host field.
	HasHost bool
	// HasAuthorization reports whether the request carried an Authorization
	// field.
	HasAuthorization bool
	// Close reports whether the client asked to close the connection after
	// this request: a Connection field holding the close token.
	Close bool
	// Lane is the index of the lane that framed the request, from 0 to the
	// group's lane count less one — its LoopIO.Loop — set by the loop adapter
	// before the handler runs. One lane is one thread or one goroutine, so a
	// handler finds its per-lane state in a slice sized at LoopStarter.OnStart,
	// with no lock. Parse leaves it as it is; outside the loop adapter it is
	// what the caller set.
	Lane int
	// Path is the request's canonical path, the one target every link of a
	// chain routes and judges on, computed once by the codec: the target's
	// path up to its query, an absolute-form target reduced to its path once
	// its authority matched Host. It is still percent-encoded, and holds no
	// dot-segment, no empty segment, no backslash and no encoded slash, dot,
	// backslash or NUL: the codec refuses a target that would (400). An
	// asterisk-form target, OPTIONS only, is "*".
	Path []byte
	// Query is the target's query, after its "?" and without it; empty when
	// the target has none.
	Query []byte
	// Route is the identifier of the route a router link matched, zero for
	// none. Parse resets it to zero; only a link sets it.
	Route int
	// Auth is what an authenticating link established: the subject and the
	// claims, copied by value into the request, so nothing of them aliases
	// bytes the connection reuses. Parse resets it; Authenticated says
	// whether a link set it.
	Auth HTTP1AuthValue
	// Authenticated reports whether an authenticating link verified the
	// request's credentials and filled Auth. Parse resets it to false.
	Authenticated bool
	// Trace is the request's W3C trace context, as the http1 chain's Trace link
	// read it from the traceparent field: invalid (IsValid false) when no such
	// link ran, when the field is absent, or when it is malformed, a request
	// being served either way (§4.3). tracestate is not read: a request carries
	// the 25 bytes of the context and no list. Parse resets it.
	Trace trace.SpanContextValue
	// Scheme is the :scheme pseudo-field of an HTTP/2 request, empty on HTTP/1.1.
	// An HTTP/2 codec maps a request before Canonical (ADR 0221): Method from
	//:method; Target from :path, origin-form or "*" (an absolute-form :path is
	// RefusalTargetForm); Host from :authority, else the Host field, both present
	// and different RefusalTargetHost, HasHost set; Fields the regular fields in
	// arrival order with HPACK's lowercase names; Authorization, ContentLength and
	// Body as on HTTP/1.1; Close, HeadLen and Consumed zero.
	Scheme []byte
	// Proto is the request's HTTP major version, 1 or 2.
	Proto uint8
	// Stream is the HTTP/2 stream the request arrived on, zero on HTTP/1.1.
	Stream uint32
	// ID identifies the request on its lane — the lane in the top 16 bits, a
	// counter from 1 in the rest — set by the loop adapter before the handler
	// runs, so two lanes never hold the same one. It is a number the client
	// never sees, what a sampling decision or a slow-request record is keyed on.
	// Parse leaves it as it is; outside the loop adapter it is what the caller
	// set.
	ID uint64
	// Conn names the connection the request arrived on, set by the loop adapter
	// before the handler runs: what a handler keeps to find, at
	// HTTP1Streamer.OnStreamOpen, the stream this request opened. Parse leaves it
	// as it is.
	Conn LoopConnRef
	// RoutePrefix is how many leading bytes of Path the Prefix route a router link
	// matched covers, zero for an exact route or none: what a file tree strips to
	// find the name under its route. Parse resets it; only a link sets it.
	RoutePrefix int
	// RequestID is the request's correlation identifier, as the http1 chain's
	// RequestID link accepted it from X-Request-Id or minted it: copied by
	// value, so a handler that needs it past the batch copies this value into
	// its own job table. Parse resets it; it is empty when no such link ran.
	RequestID HTTP1RequestIDValue
}

// Field returns the value of the first field named name and true, or nil and
// false when the request carries none. Names compare case-insensitively on
// A–Z only, so no locale or Unicode folding can make two names equal.
func (r *HTTP1RequestValue) Field(name string) ([]byte, bool) {
	//: a linear walk: a request carries a few fields, and an index would cost more to build.
	for i := range r.NFields {
		//: the first match wins, the order the client sent.
		if foldEqual(r.Fields[i].Name, name) {
			//: found.
			return r.Fields[i].Value, true
		}
	}
	//: absent.
	return nil, false
}

// foldEqual reports whether b and s are equal once A–Z are folded to a–z.
func foldEqual(b []byte, s string) bool {
	//: a length mismatch settles it before any byte is read.
	if len(b) != len(s) {
		//: different lengths are never equal.
		return false
	}
	//: byte by byte; only A–Z fold.
	for i := range len(b) {
		//: both sides folded, so the literal may be spelled in any case.
		if foldByte(b[i]) != foldByte(s[i]) {
			//: one differing byte.
			return false
		}
	}
	//: every byte matched.
	return true
}

// foldByte maps A–Z onto a–z and every other byte onto itself.
func foldByte(c byte) byte {
	//: only the 26 ASCII capitals move.
	if c >= 'A' && c <= 'Z' {
		//: the lowercase letter is 0x20 above.
		return c + 'a' - 'A'
	}
	//: anything else, the Kelvin sign's bytes included, stays.
	return c
}

// HTTP1ResponseValue is the answer an HTTP1Handler gives to one request.
//
// The handler sets the status, appends any field lines to Header with the
// codec's writer and the content to Body. The codec writes the status line,
// the Content-Length computed from Body and Connection: close when it is
// needed: the handler never spells the response's framing, so it cannot get it
// wrong.
type HTTP1ResponseValue struct {
	// Status is the status code, 200 to 999; zero means 200. The profile
	// sends no interim response.
	Status int
	// Header holds the response's extra field lines, each written as
	// "Name: value\r\n" by the codec's writer. It never holds Content-Length or
	// Connection, which the codec writes.
	Header []byte
	// Body is the response content.
	Body []byte
	// Close asks the codec to close the connection after this response.
	Close bool
	// Defer, set by the handler, holds the answer back until the end of the
	// loop's batch, for work it batches — one database transaction for every
	// request of the batch, say. The response value then stays valid, at the
	// same address, until HTTP1Batcher.EndBatch returns, and the handler
	// completes it there: it fills it and clears Defer. An answer still
	// deferred when EndBatch returns is answered 500 and its connection
	// closed. Only the loop adapter reads it; AppendResponse does not.
	Defer bool
	// Pending, set by the handler, hands the answer to the lane's worker
	// (HTTP1Worker, R3): the loop adapter sends Job to it and writes the
	// answer the worker fills, from the loop's thread, once it comes back.
	// Answers after it on the same connection wait for it, in order. A queue
	// to the worker that is full answers 503 with Retry-After at once, the
	// connection kept; a connection that closes first drops the answer. Pending
	// with Defer, or with no worker, is answered 500 and the connection closed.
	// Only the loop adapter reads it.
	Pending bool
	// Job is the integer a pending answer's worker is given: the handler's own
	// index of what the work needs, which it copied out of the request, since
	// the request's bytes do not outlive the batch.
	Job uint64
	// TraceLine holds the traceparent field line the http1 chain's Trace link
	// writes on this answer, Header pointing at it; a value, so a deferred
	// answer keeps it past the request. Only the link sets it.
	TraceLine [HTTP1TraceLineLen]byte
	// RequestID is the identifier the codec writes as an X-Request-Id field after
	// Header, when set: a value, so a deferred or a pending answer keeps it past
	// the request, and a field written per request needs no slice to outlive the
	// batch. The chain's RequestID link sets it.
	RequestID HTTP1RequestIDValue
	// Stream answers with a response delimited by the connection's close (RFC 9112
	// §6.3 item 8): the codec writes the status line and the fields with
	// Connection: close and no Content-Length, refusing HEAD, 204 and 304 with
	// HTTP1ResponseInvalid, and the handler keeps writing the body through
	// HTTP1Streamer. Requests pipelined behind it are dropped with the connection.
	// A Stream with Defer or Pending, behind a held answer, or with no
	// HTTP1Streamer in the handler is answered 500 and the connection closed. It
	// does not make a chunked answer: ADR 0186 §D6 still refuses every coding.
	Stream bool
	// Upgrade answers 101 Switching Protocols: the codec writes the status line,
	// Connection: Upgrade and Header — which holds the protocol's own fields —
	// and no length, then every later byte of the connection goes to
	// HTTP1Upgrader. It carries no body; with one, or with no HTTP1Upgrader in the
	// handler, the answer is 500 and the connection closed.
	Upgrade bool
	// RetryAfter, when above zero, is written by the codec as a Retry-After field
	// in seconds after Header: a value, so a refusal needs no slice of its own.
	// The chain's RateLimit link sets it.
	RetryAfter int
	// CORS holds the field lines the chain's CORS link gives this answer, written
	// by the codec after Header: its own slot, so the link costs nothing beside a
	// Headers link, and its bytes are the policy's, shared and read only.
	CORS []byte
	// File, set by a pending answer's worker, makes the answer's body a file: the
	// codec writes the head with Content-Length File.Len and no body bytes, and
	// sends the file behind it. A HEAD answer closes File.R unsent. With Body, on a
	// 204 or a 304, or outside a worker's answer it is answered 500 and the
	// connection closed.
	File HTTP1FileValue
}

// HTTP1FileValue is the body of an HTTP1ResponseValue when it is a file: the
// reader the file comes from and the byte range to send. Only a pending answer's
// worker fills it (HTTP1Worker); the loop adapter sends it behind the head, with
// no copy when R is a file the connection's backend can take (a descriptor and
// LoopFileWriter), and otherwise through a bounded copy, read on the lane's worker
// and never on its thread. The codec owns R from the moment the answer comes back
// and closes it, when it is an io.Closer, on every path, the client's early close
// and the drain included. R nil means no file.
type HTTP1FileValue struct {
	// R reads the file: *os.File where a descriptor is to be sent, or any other
	// io.ReaderAt (an embed.FS file) through the copy path. Nil means no file.
	R io.ReaderAt
	// Off is the offset of the first byte to send.
	Off int64
	// Len is how many bytes to send; what Content-Length says.
	Len int64
}

// LoopFileSpanValue is the file a LoopFileWriter sends: a descriptor and the byte
// range, what a kernel call names.
type LoopFileSpanValue struct {
	// FD is the open descriptor, read and never closed by the engine.
	FD int
	// Off is the offset of the first byte to send.
	Off int64
	// Len is how many bytes to send.
	Len int64
}

// HTTP1StreamValue is a stream response opened on a connection, handed to
// HTTP1Streamer's callbacks. It is a value a handler keeps for as long as the
// connection lives, and every method runs on the lane's thread: a callback, or a
// LoopIO.Post.
type HTTP1StreamValue struct {
	// IO is the loop the connection lives on.
	IO LoopIO
	// Ref is the connection.
	Ref LoopConnRef
}

// Send writes p as the next bytes of the body, copied before it returns. It
// is refused as LoopIO.Write is: LoopOutputFull when the connection's output
// is past its ceiling, LoopStaleRef when it closed.
func (s HTTP1StreamValue) Send(p []byte) error {
	//: the loop's own bounded, copying write.
	return s.IO.Write(s.Ref, p)
}

// End closes the connection once the output already written has left: the
// only way a response delimited by close ends.
func (s HTTP1StreamValue) End() error {
	//: closed after the output.
	return s.IO.Close(s.Ref)
}

// Deadline arms the connection's timer, HTTP1Streamer.OnStreamTimeout firing after d.
func (s HTTP1StreamValue) Deadline(d time.Duration) error {
	//: the loop's timer wheel.
	return s.IO.SetDeadline(s.Ref, d)
}

// HTTP1HandlerFunc adapts a plain function to HTTP1Handler.
type HTTP1HandlerFunc func(req *HTTP1RequestValue, res *HTTP1ResponseValue)

// ServeHTTP1 implements HTTP1Handler by calling f.
func (f HTTP1HandlerFunc) ServeHTTP1(req *HTTP1RequestValue, res *HTTP1ResponseValue) {
	//: the function IS the handler — nothing else to consult.
	f(req, res)
}
