package client

import (
	"errors"
	"io"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// splitReader hands back its payload and reports the end separately: the final
// Read returns (n, nil) and only the NEXT one returns (0, io.EOF).
//
// io.Reader explicitly permits both shapes, and which one a response body uses
// is decided by the transport, not by this package. An HTTP/1.1 body with a
// Content-Length attaches EOF to the last bytes; an HTTP/2 body — which this
// client asks for, ForceAttemptHTTP2 is set — does not. Only the split shape
// asks the wrapper for one more byte after the ceiling is reached, so it is the
// only one that can observe an off-by-one there. A test written solely against
// httptest, which speaks HTTP/1.1 with a Content-Length, passes straight over
// the bug.
type splitReader struct {
	// data is what remains to be handed back.
	data []byte
	// eofWithData attaches io.EOF to the final bytes instead of to the call
	// after them, which is the other legal shape.
	eofWithData bool
	// failWith ends the reader with this error instead of io.EOF.
	failWith error
	// closeErr is what Close reports.
	closeErr error
	// closes counts how many times Close was called.
	closes int
}

// Read implements io.Reader.
func (r *splitReader) Read(p []byte) (n int, err error) {
	//: nothing left, so this is the call that reports the end.
	if len(r.data) == 0 {
		//: the reader is drained, one way or the other.
		return 0, r.end()
	}
	n = copy(p, r.data)
	r.data = r.data[n:]
	//: some transports attach the end to the last bytes; HTTP/2 does not.
	if len(r.data) == 0 && r.eofWithData {
		//: the end travels with the payload.
		return n, r.end()
	}
	//: more to come, or the end is reported by the next call.
	return n, nil
}

// end is how this reader finishes: io.EOF unless a failure was configured.
func (r *splitReader) end() error {
	//: a configured failure stands in for a connection that died mid-body.
	if r.failWith != nil {
		//: the body ended early.
		return r.failWith
	}
	//: the body was delivered in full.
	return io.EOF
}

// Close implements io.Closer.
func (r *splitReader) Close() error {
	r.closes++
	//: the inner body's own close result is what cappedBody must report.
	return r.closeErr
}

// Test_cappedBody_Read pins all three boundaries in both EOF shapes.
//
// The ceiling is a MAXIMUM: a body of exactly that size is admissible, and only
// a body past it is refused. Distinguishing the two requires reading one byte
// beyond the ceiling, because "the body ended here" and "there is one more byte
// to come" are otherwise the same observation — which is precisely the
// off-by-one this test exists to hold shut.
func Test_cappedBody_Read(t *testing.T) {
	t.Parallel()
	const limit int64 = 16
	type tc struct {
		// name describes the case.
		name string
		// size is the body length in bytes.
		size int
		// eofWithData selects which of the two legal EOF shapes the body uses.
		eofWithData bool
		// wantErr is whether the ceiling must refuse that body.
		wantErr bool
	}
	tests := []tc{
		{name: "under the ceiling, end with the bytes", size: 15, eofWithData: true},
		{name: "under the ceiling, end reported after", size: 15},
		{name: "an empty body", size: 0},
		{name: "exactly at the ceiling, end with the bytes", size: 16, eofWithData: true},
		{name: "exactly at the ceiling, end reported after", size: 16},
		{name: "one byte over, end with the bytes", size: 17, eofWithData: true, wantErr: true},
		{name: "one byte over, end reported after", size: 17, wantErr: true},
		{name: "far over the ceiling", size: 4096, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var reported int64 = -1
		inner := &splitReader{data: make([]byte, c.size), eofWithData: c.eofWithData}
		body := &cappedBody{
			inner: inner,
			limit: limit,
			done:  func(read int64, _ error) { reported = read },
		}

		got, err := io.ReadAll(body)

		//: only a body PAST the ceiling may be refused.
		if c.wantErr {
			if !errs.HasCode(err, corenet.CodeResponseTooLarge) {
				t.Fatalf("a %d-byte body under a %d-byte ceiling: expected RESPONSE_TOO_LARGE, got %v",
					c.size, limit, err)
			}
			//: the refusal is observed, so an audit trail never shows a clean read.
			if reported < 0 {
				t.Error("the refused body was never reported to the observer")
			}
			return
		}
		if err != nil {
			t.Fatalf("a %d-byte body under a %d-byte ceiling was refused: %v", c.size, limit, err)
		}
		//: an admitted body must arrive whole; the ceiling never truncates.
		if len(got) != c.size {
			t.Fatalf("read %d bytes of a %d-byte body — the ceiling truncated instead of admitting",
				len(got), c.size)
		}
		//: the observer learns the size, which is the whole reason it fires here.
		if reported != int64(c.size) {
			t.Errorf("the observer was told %d bytes, want %d", reported, c.size)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_cappedBody_finish pins that a body reports itself AT MOST ONCE.
//
// EOF, a read failure and Close all end a body, and a caller normally reaches
// two of them — reading to EOF and then closing. Reporting each would double
// every entry in the audit trail, which is how a call count stops meaning
// anything.
func Test_cappedBody_finish(t *testing.T) {
	t.Parallel()
	failure := errors.New("connection reset")

	type tc struct {
		// name describes the case.
		name string
		// noObserver leaves done nil, which a body must tolerate.
		noObserver bool
		// errs are the outcomes handed to finish, in order.
		errs []error
		// want is the outcome the observer must record, if any.
		want error
	}
	tests := []tc{
		{name: "a single clean end", errs: []error{nil}},
		{name: "a single failure", errs: []error{failure}, want: failure},
		//: the FIRST outcome is the one recorded; the rest are no-ops.
		{name: "an end then a close", errs: []error{nil, nil}},
		{name: "a failure then a close", errs: []error{failure, nil}, want: failure},
		{name: "a close then a spurious failure", errs: []error{nil, failure}},
		{name: "no observer at all", noObserver: true, errs: []error{failure}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var calls int
		var got error
		body := &cappedBody{inner: &splitReader{}, limit: 8, read: 3}
		if !c.noObserver {
			body.done = func(_ int64, err error) {
				calls++
				got = err
			}
		}

		for _, e := range c.errs {
			body.finish(e)
		}

		if c.noObserver {
			//: reaching here at all is the assertion: a nil hook must not panic.
			if calls != 0 {
				t.Fatalf("a body with no observer reported %d times", calls)
			}
			return
		}
		if calls != 1 {
			t.Fatalf("the body reported %d times for %d endings, want exactly 1",
				calls, len(c.errs))
		}
		if !errors.Is(got, c.want) {
			t.Errorf("the observer recorded %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_readOutcome pins that io.EOF is not a failure. It is how a COMPLETE body
// ends, so recording it would mark every successful call as an error and make
// the audit trail useless for finding the real ones.
func Test_readOutcome(t *testing.T) {
	t.Parallel()
	broken := errors.New("unexpected EOF")

	type tc struct {
		// name describes the case.
		name string
		// err is the terminal read result.
		err error
		// wantErr is whether the observer should record a failure.
		wantErr bool
	}
	tests := []tc{
		{name: "the body ended normally", err: io.EOF},
		{name: "a wrapped end", err: errs.Wrap(io.EOF, errs.WrapParams{})},
		{name: "the body was cut short", err: io.ErrUnexpectedEOF, wantErr: true},
		{name: "an invalid read", err: broken, wantErr: true},
		{name: "no error yet", err: nil},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := readOutcome(c.err)

		if c.wantErr {
			if !errors.Is(got, c.err) {
				t.Fatalf("readOutcome(%v) = %v, want the error itself", c.err, got)
			}
			return
		}
		if got != nil {
			t.Errorf("readOutcome(%v) = %v, want nil — a complete body is not a failure",
				c.err, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_cappedBody_tooLarge pins that the refusal names the CEILING and never the
// body. The limit is what an operator can act on; the payload is whatever the
// upstream sent, and putting that in an error is how a secret reaches a log.
func Test_cappedBody_tooLarge(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// limit is the configured ceiling.
		limit int64
	}
	tests := []tc{
		{name: "a small ceiling", limit: 128},
		{name: "a large ceiling", limit: 8 << 20},
		{name: "a ceiling of zero", limit: 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		body := &cappedBody{inner: &splitReader{}, limit: c.limit, read: c.limit + 1}

		err := body.tooLarge()

		if !errs.HasCode(err, corenet.CodeResponseTooLarge) {
			t.Fatalf("tooLarge() = %v, want RESPONSE_TOO_LARGE", err)
		}
		var named bool
		for _, f := range errs.FieldsOf(err) {
			if f.Key() == "limit" {
				named = true
			}
		}
		//: without the ceiling in the error, the operator cannot tell whether to
		//: raise it or to fix the upstream.
		if !named {
			t.Errorf("the refusal does not name the ceiling: %v", errs.FieldsOf(err))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_cappedBody_Close pins that closing releases the connection and is the
// LAST chance to record the call. A caller using the escape hatch may close a
// body it never read, and that call still happened.
func Test_cappedBody_Close(t *testing.T) {
	t.Parallel()
	failure := errors.New("close failed")

	type tc struct {
		// name describes the case.
		name string
		// closeErr is what the inner body reports on Close.
		closeErr error
		// readFirst drains the body before closing it.
		readFirst bool
		// wantObserved is the outcome the observer must record.
		wantObserved error
	}
	tests := []tc{
		{name: "a body closed without being read"},
		{name: "a body read then closed", readFirst: true},
		{name: "an invalid close", closeErr: failure, wantObserved: failure},
		//: the read already reported this body, so the close is a no-op and its
		//: own failure does not overwrite the recorded outcome.
		{name: "a body read then closed badly", readFirst: true, closeErr: failure},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var calls int
		var observed error
		inner := &splitReader{data: make([]byte, 4), closeErr: c.closeErr}
		body := &cappedBody{
			inner: inner,
			limit: 64,
			done: func(_ int64, err error) {
				calls++
				observed = err
			},
		}
		if c.readFirst {
			if _, err := io.ReadAll(body); err != nil {
				t.Fatalf("reading the body: %v", err)
			}
		}

		err := body.Close()

		//: the inner body's result is reported unchanged — that is what tells the
		//: caller the connection did not come back to the pool.
		if !errors.Is(err, c.closeErr) {
			t.Fatalf("Close() = %v, want %v", err, c.closeErr)
		}
		//: closing the inner body is what releases the connection.
		if inner.closes != 1 {
			t.Errorf("the inner body was closed %d times, want 1", inner.closes)
		}
		if calls != 1 {
			t.Fatalf("the body reported %d times, want exactly 1", calls)
		}
		if !errors.Is(observed, c.wantObserved) {
			t.Errorf("the observer recorded %v, want %v", observed, c.wantObserved)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
