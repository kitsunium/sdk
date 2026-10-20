package server

import (
	"io"
	"net/http"
	"time"
)

// perReadBody gives every request body a deadline per read: the group's
// ReadTimeout, refreshed before each read, as the stream path's own conn
// refreshes it.
//
// http.Server.ReadTimeout — which the adapter used to set from the same
// option — is a budget for the WHOLE request, headers and body together: a
// peer sending a 20-byte body two bytes every 100 ms, every read well inside
// a 500 ms bound, was cut off after the eighth byte (#454), against the
// TimeoutsValue contract ("every field is a PER-OPERATION bound, refreshed
// each time"). The header phase keeps its own bound, http.Server's
// ReadHeaderTimeout, which falls back to the read budget; the body is bounded
// here, read by read; and the time a handler spends between two reads is its
// own, as it is on a stream connection.
//
// A zero read budget leaves the body unbounded, as before, and installs
// nothing.
func perReadBody(next http.Handler, read time.Duration) http.Handler {
	//: no budget, nothing to refresh.
	if read <= 0 {
		//: the handler runs as the caller wrote it.
		return next
	}
	//: every request is wrapped on its way to the handler.
	return readDeadlineHandler{next: next, read: read}
}

// readDeadlineHandler installs a deadlineBody on each request with a body.
type readDeadlineHandler struct {
	// next is the application's handler.
	next http.Handler
	// read is the bound of one read of the body.
	read time.Duration
}

// ServeHTTP implements http.Handler.
func (h readDeadlineHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	//: a request with no body has no read to bound; net/http's NoBody is the
	//: common case and costs nothing.
	if r.Body != nil && r.Body != http.NoBody {
		r.Body = &deadlineBody{inner: r.Body, rc: http.NewResponseController(w), read: h.read}
	}
	h.next.ServeHTTP(w, r)
}

// deadlineBody bounds each read of a request body on its own.
type deadlineBody struct {
	// inner is net/http's body.
	inner io.ReadCloser
	// rc sets the connection's (or, on HTTP/2, the stream's) read deadline.
	rc *http.ResponseController
	// read is the bound of one read.
	read time.Duration
	// ended records that the body returned its last read.
	ended bool
}

// Read implements io.Reader.
func (b *deadlineBody) Read(p []byte) (n int, err error) {
	//: once the body has ended, nothing more is read from the peer through it.
	if b.ended {
		//: the inner body repeats its own end.
		return b.inner.Read(p)
	}
	//: each read gets the budget afresh; the error, a hijacked or closed
	//: connection, is reported by the read itself far more usefully.
	swallowErr(b.rc.SetReadDeadline(time.Now().Add(b.read)))
	n, err = b.inner.Read(p)
	//: the body is over. At its EOF net/http starts a background read that
	//: watches for the peer going away, and a deadline meant for a body read
	//: would expire under it and cancel the request's context while the
	//: handler is still working — so the deadline is cleared here.
	if err != nil {
		b.ended = true
		swallowErr(b.rc.SetReadDeadline(time.Time{}))
	}
	//: the body's own outcome, a deadline's timeout included.
	return n, err
}

// Close implements io.Closer.
func (b *deadlineBody) Close() error {
	//: closing is net/http's, on the body it handed out.
	return b.inner.Close()
}
