package net

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
	//: copied.
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
	// Deferred reports whether the answer was deferred to the batch's end.
	Deferred bool
	// NFields is how many entries of Fields the record filled.
	NFields int
	// Fields holds the allowed request fields the request carried, in the order
	// the link was given their names.
	Fields [HTTP1MaxAccessFields]HTTP1FieldValue
}
