// Package client — the response body size ceiling.
package client

import (
	"errors"
	"io"
	"sync"

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
	// done reports the finished body exactly once, with the byte count and
	// whatever ended it. It is how the observation hook learns a size at all:
	// a body's length is only known once it has been read, so a record emitted
	// when the headers arrive can never carry one.
	done func(read int64, err error)
	// once keeps that report to exactly one per body, whether the body ended at
	// EOF, at a read failure, or at Close.
	once sync.Once
}

// finish reports the completed body to the observer, at most once.
func (c *cappedBody) finish(err error) {
	//: a body with no observer costs one comparison.
	if c.done == nil {
		//: nothing observes this body.
		return
	}
	//: EOF, a read failure and Close all end the body; whichever happens first
	//: is the one that reports it, and the others are then no-ops.
	c.once.Do(func() {
		c.done(c.read, err)
	})
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
		refusal := c.tooLarge()
		//: the observer sees the refusal, not a clean 2xx with no bytes.
		c.finish(refusal)
		//: refuse rather than hand back a truncated body.
		return 0, refusal
	}
	//: the body is over, successfully or not; this is where its size is known.
	if err != nil {
		c.finish(readOutcome(err))
	}
	//: propagate the transport's own outcome, including io.EOF.
	return n, err
}

// readOutcome maps a terminal read result onto what the observer should record.
func readOutcome(err error) error {
	//: io.EOF is how a complete body ends, not a failure to report.
	if errors.Is(err, io.EOF) {
		//: the body was read in full.
		return nil
	}
	//: anything else ended the body early and belongs in the audit trail.
	return err
}

// tooLarge reports the refusal, naming the ceiling but never the body.
func (c *cappedBody) tooLarge() error {
	//: the limit is the actionable part; the payload never goes in an error.
	return errs.Wrap(corenet.ResponseTooLarge, errs.WrapParams{},
		errs.Int64("limit", c.limit))
}

// Close implements io.Closer.
func (c *cappedBody) Close() error {
	err := c.inner.Close()
	//: a body closed before it was read is still a completed call, and it is
	//: the last chance to record one — a caller using the escape hatch may
	//: never read to EOF at all.
	c.finish(err)
	//: closing the inner body is what releases the connection.
	return err
}
