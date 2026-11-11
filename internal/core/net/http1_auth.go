package net

import "github.com/kitsunium/sdk/internal/core/observe/trace"

// The bytes an HTTP1RequestIDValue may hold: the visible ASCII range.
const (
	// visibleFirst is '!'.
	visibleFirst byte = 0x21
	// visibleLast is '~'.
	visibleLast byte = 0x7E
)

const (
	// HTTP1AccessAnswered is an answer written when the handler returned.
	HTTP1AccessAnswered HTTP1AccessStage = iota
	// HTTP1AccessDeferred is the first record of an answer deferred to the
	// batch's end; its Status is zero.
	HTTP1AccessDeferred
	// HTTP1AccessPending is an answer handed to the lane's worker: the record's
	// only one, its Status zero.
	HTTP1AccessPending
	// HTTP1AccessCompleted is a deferred answer's second record: Status and
	// BodyLen are the answer's, Elapsed the time since it was deferred.
	HTTP1AccessCompleted
)

// HTTP1AuthValue is what an authenticating link of the http1 chain
// established about a request: a subject of at most HTTP1MaxSubject bytes,
// held in an array, and 64 bits of claims the application gives meaning to.
// It is a value, so copying a request copies it, and it never aliases the
// bytes the credentials were read from. The zero value is no subject and no
// claim.
type HTTP1AuthValue struct {
	// subject holds the subject's bytes, the first n of them.
	subject [HTTP1MaxSubject]byte
	// n is the subject's length.
	n uint8
	// claims are the application's bits.
	claims uint64
}

// SetSubject copies b into the value and reports true, or leaves the value
// as it is and reports false when b is longer than HTTP1MaxSubject.
func (a *HTTP1AuthValue) SetSubject(b []byte) bool {
	//: a subject the array cannot hold is refused, never cut.
	if len(b) > HTTP1MaxSubject {
		//: left as it was.
		return false
	}
	a.n = uint8(copy(a.subject[:], b))
	clear(a.subject[a.n:])
	//: copied, nothing of an earlier subject left past it.
	return true
}

// Subject returns the subject, aliasing the value: valid while the value
// is, and empty when none was set.
func (a *HTTP1AuthValue) Subject() []byte {
	//: the bytes set, within the value.
	return a.subject[:a.n]
}

// SetClaims sets the claims.
func (a *HTTP1AuthValue) SetClaims(c uint64) {
	a.claims = c
}

// Claims returns the claims.
func (a *HTTP1AuthValue) Claims() uint64 {
	//: as set.
	return a.claims
}

// Reset clears the subject and the claims.
func (a *HTTP1AuthValue) Reset() {
	//: the length is the subject's extent: the stale bytes past it are never read.
	a.n, a.claims = 0, 0
}

// HTTP1RequestIDValue is a request's correlation identifier: at most
// HTTP1MaxRequestID bytes of visible ASCII (0x21 to 0x7E), held in an array. It
// is a value, so copying a request or a response copies it and it never
// aliases the bytes it was read from. The zero value is no identifier.
type HTTP1RequestIDValue struct {
	// id holds the identifier's bytes, the first n of them.
	id [HTTP1MaxRequestID]byte
	// n is the identifier's length.
	n uint8
}

// Set copies b into the value and reports true, or leaves the value as it is
// and reports false when b is empty, longer than HTTP1MaxRequestID or holds a
// byte outside 0x21 to 0x7E: the codec writes the value into a response head,
// so what the wire cannot carry never enters it.
func (v *HTTP1RequestIDValue) Set(b []byte) bool {
	//: empty is absent, and a longer one is refused, never cut.
	if len(b) == 0 || len(b) > HTTP1MaxRequestID {
		//: left as it was.
		return false
	}
	//: every byte must be one a field value carries without a question.
	for _, c := range b {
		//: a space, a control, DEL or a high byte.
		if c < visibleFirst || c > visibleLast {
			//: left as it was.
			return false
		}
	}
	v.n = uint8(copy(v.id[:], b))
	//: copied.
	return true
}

// Bytes returns the identifier, aliasing the value: valid while the value is,
// and empty when none was set.
func (v *HTTP1RequestIDValue) Bytes() []byte {
	//: the bytes set, within the value.
	return v.id[:v.n]
}

// Reset clears the identifier.
func (v *HTTP1RequestIDValue) Reset() {
	//: the length is the identifier's extent: the stale bytes past it are never read.
	v.n = 0
}

// HTTP1AccessStage says which moment of an answer's life an access record
// describes.
type HTTP1AccessStage uint8

// HTTP1AccessValue is one request's access-log record, as the http1 chain's
// logging link hands it to an HTTP1AccessLogger: what the request asked, how
// it was routed and answered, and the request fields the link allows, by
// name. It never holds the Authorization field, the query or the body: the
// link refuses to allow a credential field, and the record has no slot for
// the rest. Every slice aliases the request or the response.
type HTTP1AccessValue struct {
	// Lane is the request's lane.
	Lane int
	// Route is the route a router link matched, zero for none.
	Route int
	// Method is the request method.
	Method []byte
	// Path is the request's canonical path, without its query.
	Path []byte
	// Status is the answer's status code, 200 when the handler left it zero;
	// zero for an answer still deferred.
	Status int
	// BodyLen is the length of the answer's body.
	BodyLen int
	// Authenticated reports whether an authenticating link verified the request.
	Authenticated bool
	// TraceID is the trace the request joined, read from its traceparent by a
	// Trace link in the same chain; the zero value without one.
	TraceID trace.TraceID
	// Stage says what the record is: an answer written, one deferred or pending
	// and so with no status yet, or a deferred answer's completion.
	Stage HTTP1AccessStage
	// ID is the request's number (HTTP1RequestValue.ID): what a sampling decision
	// or a slow-request record is keyed on, the same on both records of a deferred
	// answer.
	ID uint64
	// Elapsed is the time from the batch's start to this record, the wait behind
	// the batch's earlier requests included; on a deferred answer's second record
	// the time since it was deferred.
	Elapsed DurationValue
	// RequestID is the request's correlation identifier, the one its answer
	// carries in X-Request-Id; empty without a RequestID link in the chain.
	RequestID HTTP1RequestIDValue
	// Proto is the request's HTTP major version, 1 or 2.
	Proto uint8
	// Stream is the HTTP/2 stream the request arrived on, zero on HTTP/1.1.
	Stream uint32
	// NFields is how many entries of Fields the record filled.
	NFields int
	// Fields holds the allowed request fields the request carried, in the order
	// the link was given their names.
	Fields [HTTP1MaxAccessFields]HTTP1FieldValue
}
