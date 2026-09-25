// Package server — the bounds only an HTTP group has.
package server

import "time"

// httpBounds are the options that mean something only to the net/http
// adapter: the header phase's own deadline and the header size cap. Their zero
// value is "not set", and each keeps the behaviour the group had before the
// option existed.
type httpBounds struct {
	// readHeaderTimeout bounds the request line and the header fields; zero or
	// negative falls back to the group's read timeout.
	readHeaderTimeout time.Duration
	// maxHeaderBytes caps the request line and the header fields; zero or
	// negative leaves net/http's default.
	maxHeaderBytes int
}

// headerTimeout resolves the header phase's deadline: the option when it was
// set, the read timeout otherwise — which is what the adapter applied before
// the option existed.
func (b httpBounds) headerTimeout(read time.Duration) time.Duration {
	//: the caller bounded the header phase on its own.
	if b.readHeaderTimeout > 0 {
		//: the option wins.
		return b.readHeaderTimeout
	}
	//: unset: the read budget, as before.
	return read
}

// headerBytes resolves the header cap handed to http.Server, where zero means
// net/http's own default.
func (b httpBounds) headerBytes() int {
	//: a positive cap is the caller's.
	if b.maxHeaderBytes > 0 {
		//: the option.
		return b.maxHeaderBytes
	}
	//: unset or non-positive: net/http's default applies.
	return 0
}
