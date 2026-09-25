// Package server — the two group options only an HTTP group reads: the header
// phase's own deadline and the header size cap.
package server

import (
	"time"

	svcserver "github.com/kitsunium/sdk/internal/service/net/server"
)

// ReadHeaderTimeout bounds the HEADER phase of an HTTP request on its own: the
// request line and every header field must arrive within d, whatever the read
// timeout allows a body.
//
// The header phase is the one a slowloris stalls — a byte every few seconds,
// never finishing a line — and the one an honest client finishes in
// milliseconds, so it deserves a bound far shorter than a read timeout sized
// for an upload. A client that stalls there is disconnected at d. Unset (or
// non-positive) keeps the earlier behaviour: the header phase is bounded by
// [ReadTimeout]. No effect on a group that does not serve HTTP.
func ReadHeaderTimeout(d time.Duration) GroupOption {
	//: forwarded unchanged.
	return svcserver.ReadHeaderTimeout(d)
}

// MaxHeaderBytes caps the bytes net/http reads for a request's line and header
// fields; a request past the cap is answered 431 Request Header Fields Too
// Large and never reaches the handler.
//
// Unset (or non-positive) keeps net/http's default of 1 MiB, which applied
// silently before this option existed. net/http reads a small fixed allowance
// past the cap before refusing. No effect on a group that does not serve HTTP.
func MaxHeaderBytes(n int) GroupOption {
	//: forwarded unchanged.
	return svcserver.MaxHeaderBytes(n)
}
