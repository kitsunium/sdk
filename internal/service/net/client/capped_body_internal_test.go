package client

import (
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
}

// Read implements io.Reader.
func (r *splitReader) Read(p []byte) (n int, err error) {
	//: nothing left, so this is the call that reports the end.
	if len(r.data) == 0 {
		//: the reader is drained.
		return 0, io.EOF
	}
	n = copy(p, r.data)
	r.data = r.data[n:]
	//: some transports attach the end to the last bytes; HTTP/2 does not.
	if len(r.data) == 0 && r.eofWithData {
		//: the end travels with the payload.
		return n, io.EOF
	}
	//: more to come, or the end is reported by the next call.
	return n, nil
}

// Close implements io.Closer.
func (r *splitReader) Close() error {
	//: nothing to release; the reader is memory-backed.
	return nil
}

// cappedBodyCase is one body size, in one EOF shape, against the ceiling.
type cappedBodyCase struct {
	// name describes the case.
	name string
	// size is the body length in bytes.
	size int
	// eofWithData selects which of the two legal EOF shapes the body uses.
	eofWithData bool
	// wantErr is whether the ceiling must refuse that body.
	wantErr bool
}

// TestCappedBodyBoundaries pins all three boundaries in both EOF shapes.
//
// The ceiling is a MAXIMUM: a body of exactly that size is admissible, and only
// a body past it is refused. Distinguishing the two requires reading one byte
// beyond the ceiling, because "the body ended here" and "there is one more byte
// to come" are otherwise the same observation — which is precisely the
// off-by-one this test exists to hold shut.
func TestCappedBodyBoundaries(t *testing.T) {
	t.Parallel()
	const limit int64 = 16
	cases := []cappedBodyCase{
		{name: "under the ceiling, end with the bytes", size: 15, eofWithData: true},
		{name: "under the ceiling, end reported after", size: 15},
		{name: "exactly at the ceiling, end with the bytes", size: 16, eofWithData: true},
		{name: "exactly at the ceiling, end reported after", size: 16},
		{name: "one byte over, end with the bytes", size: 17, eofWithData: true, wantErr: true},
		{name: "one byte over, end reported after", size: 17, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCappedBodyCase(t, tc, limit)
		})
	}
}

// runCappedBodyCase reads one body through the ceiling and checks the verdict.
func runCappedBodyCase(t *testing.T, tc cappedBodyCase, limit int64) {
	t.Helper()
	inner := &splitReader{data: make([]byte, tc.size), eofWithData: tc.eofWithData}
	body := &cappedBody{inner: inner, limit: limit}
	got, err := io.ReadAll(body)
	//: only a body PAST the ceiling may be refused.
	if tc.wantErr {
		if !errs.HasCode(err, corenet.CodeResponseTooLarge) {
			t.Fatalf("a %d-byte body under a %d-byte ceiling: expected RESPONSE_TOO_LARGE, got %v",
				tc.size, limit, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("a %d-byte body under a %d-byte ceiling was refused: %v", tc.size, limit, err)
	}
	//: an admitted body must arrive whole; the ceiling never truncates.
	if len(got) != tc.size {
		t.Fatalf("read %d bytes of a %d-byte body — the ceiling truncated instead of admitting",
			len(got), tc.size)
	}
}
