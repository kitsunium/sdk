// Package client — the response body size ceiling.
package client

import (
	"io"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// cappedBody stops reading past the configured ceiling.
//
// It FAILS rather than truncating. A silent truncation does not stay silent: it
// surfaces several layers away as an incomprehensible decode error on a body
// that looks complete, and the operator has no way to connect the two. Failing
// at the boundary names the actual problem.
type cappedBody struct {
	// inner is the transport's own body.
	inner io.ReadCloser
	// limit is the ceiling in bytes.
	limit int64
	// read counts what has been handed to the caller so far.
	read int64
}

// Read implements io.Reader.
//
// The ceiling is a MAXIMUM, so a body of exactly that size is admitted and only
// one past it is refused. Telling those apart costs one byte: a wrapper that
// stops the moment it has handed back `limit` bytes cannot distinguish "the
// body ended exactly here" from "there is one more byte to come", and refuses
// both. Whether it ever gets to make that distinction is decided by the
// transport rather than by this package — an HTTP/1.1 body with a
// Content-Length reports the end together with its last bytes, so the question
// never arises, while an HTTP/2 body reports it on the following call, which is
// the shape this client asks for by setting ForceAttemptHTTP2.
func (c *cappedBody) Read(p []byte) (n int, err error) {
	//: the ceiling was already passed on a previous call.
	if c.read > c.limit {
		//: refuse rather than hand back a truncated body.
		return 0, c.tooLarge()
	}
	//: read at most one byte PAST the ceiling: that probe byte is the whole
	//: difference between a body that ended on the boundary and one that
	//: crossed it. Reading no further is what still stops a single oversized
	//: Read from crossing the ceiling undetected.
	remaining := c.limit - c.read + 1
	//: shrink the caller's buffer so the transport cannot overshoot the probe.
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err = c.inner.Read(p)
	c.read += int64(n)
	//: the probe byte arrived, so the body genuinely exceeds the ceiling.
	if c.read > c.limit {
		//: refuse rather than hand back a truncated body.
		return 0, c.tooLarge()
	}
	//: propagate the transport's own outcome, including io.EOF.
	return n, err
}

// tooLarge reports the refusal, naming the ceiling but never the body.
func (c *cappedBody) tooLarge() error {
	//: the limit is the actionable part; the payload never goes in an error.
	return errs.Wrap(corenet.ResponseTooLarge, errs.WrapParams{},
		errs.Int64("limit", c.limit))
}

// Close implements io.Closer.
func (c *cappedBody) Close() error {
	//: closing the inner body is what releases the connection.
	return c.inner.Close()
}
