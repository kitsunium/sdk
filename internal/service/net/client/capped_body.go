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
func (c *cappedBody) Read(p []byte) (n int, err error) {
	//: the ceiling was already reached on a previous call.
	if c.read >= c.limit {
		//: refuse rather than hand back a truncated body.
		return 0, errs.Wrap(corenet.ResponseTooLarge, errs.WrapParams{},
			errs.Int64("limit", c.limit))
	}
	remaining := c.limit - c.read
	//: never ask for more than the ceiling allows, or a single oversized Read
	//: could cross it without ever being detected.
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err = c.inner.Read(p)
	c.read += int64(n)
	//: propagate the transport's own outcome, including io.EOF.
	return n, err
}

// Close implements io.Closer.
func (c *cappedBody) Close() error {
	//: closing the inner body is what releases the connection.
	return c.inner.Close()
}
