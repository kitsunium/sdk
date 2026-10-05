//go:build !race

// The fixtures of scratch's performance contracts (design/sdk.yaml,
// budgets): each sets one call up and returns it, and the perf_gen_test.go
// kit gen writes beside this file counts its allocations against its
// budget — the total over 30 000 calls after as many warm-up calls.
//
// The shared pools exist so that a codec's transient buffer is recycled
// rather than allocated per encode: a warm Acquire/Release round trip, of a
// buffer or of a reader, allocates nothing. Detaching a buffer within the
// cap clones its bytes — the caller's own copy, exactly one allocation —
// and repools the buffer, so it allocates exactly once: never zero (the
// caller's slice would alias a pooled buffer the next encode overwrites)
// and never twice (the buffer itself is not reallocated).
package scratch_test

import (
	"bytes"
	"testing"

	"github.com/kitsunium/sdk/internal/core/data/codec/scratch"
)

// The sinks keep each call's result alive.
var (
	perfBuffer *bytes.Buffer
	perfReader *bytes.Reader
	perfBytes  []byte
)

// perfPayload is what a detached buffer holds: small, under the cap.
var perfPayload = []byte("a codec's encoded bytes")

// perfAcquireBuffer is AcquireBuffer's fixture: a borrow of the shared
// buffer and its release, the pool warmed first.
func perfAcquireBuffer(testing.TB) func() {
	scratch.ReleaseBuffer(scratch.AcquireBuffer())
	return func() {
		buf := scratch.AcquireBuffer()
		perfBuffer = buf
		scratch.ReleaseBuffer(buf)
	}
}

// perfAcquireReader is AcquireReader's fixture: a borrow of a reader
// positioned on a source, and its release.
func perfAcquireReader(testing.TB) func() {
	scratch.ReleaseReader(scratch.AcquireReader(perfPayload))
	return func() {
		r := scratch.AcquireReader(perfPayload)
		perfReader = r
		scratch.ReleaseReader(r)
	}
}

// perfDetachBuffer is DetachBuffer's fixture: a borrowed buffer written to,
// then detached — its bytes cloned for the caller, the buffer repooled.
func perfDetachBuffer(testing.TB) func() {
	scratch.ReleaseBuffer(scratch.AcquireBuffer())
	return func() {
		buf := scratch.AcquireBuffer()
		buf.Write(perfPayload)
		perfBytes = scratch.DetachBuffer(buf)
	}
}
