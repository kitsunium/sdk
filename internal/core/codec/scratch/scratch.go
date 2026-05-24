// Package scratch provides shared, size-bounded recycling primitives for
// the service codecs. Every codec that encodes into a transient
// *bytes.Buffer previously declared its own sync.Pool, its own
// 256 KiB cap-discard constant, and its own release helper — nine
// byte-for-byte copies of the same logic. Centralising them here gives a
// single source of truth for the cap-discard policy and a single shared
// pool whose reuse rate is higher than nine independent pools (sync.Pool
// is internally per-P sharded, so consolidation does not add contention).
//
// Lifetime contract: a value returned by an Acquire* call is owned by the
// caller until the matching Release* call. After Release the value — and
// any slice aliasing it (e.g. buf.Bytes()) — MUST NOT be used; clone the
// encoded bytes first if they need to outlive the release.
package scratch

import (
	"bytes"
	"sync"
)

// MaxRetainedBufBytes is the project-wide cap-discard threshold for pooled
// *bytes.Buffer instances. A buffer whose capacity exceeds this is dropped
// on release rather than re-pooled, so a one-off oversized payload cannot
// pin a large allocation for the lifetime of the pool's GC window.
const MaxRetainedBufBytes int = 256 << 10

// Shared recycling pools for the codecs. One shared pool per pooled type —
// sync.Pool is internally per-P sharded, so consolidating across codecs
// raises the reuse rate without adding contention.
var (
	//: bufferPool recycles *bytes.Buffer across every codec that encodes
	//: into a transient buffer.
	bufferPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

	//: readerPool recycles *bytes.Reader for codecs that wrap caller input
	//: in a reader for streaming decode (csv, msgpack). A bytes.Reader is a
	//: fixed-size struct, so unlike bufferPool it needs no cap-discard.
	readerPool = sync.Pool{New: func() any { return new(bytes.Reader) }}
)

// AcquireBuffer returns a reset *bytes.Buffer from the shared pool. The
// caller owns it until ReleaseBuffer; the buffer is already Reset, so
// callers append directly without re-resetting.
func AcquireBuffer() *bytes.Buffer {
	//: pool guarantees a *bytes.Buffer via its New func.
	buf, ok := bufferPool.Get().(*bytes.Buffer)
	//: pool invariant guard — never expected to fail at runtime.
	if !ok {
		//: invariant broken — fail loud at the call site.
		panic("internal/core/codec/scratch: bufferPool yielded non-*bytes.Buffer")
	}
	//: start clean — pool may return a partially-filled buffer.
	buf.Reset()
	//: hand the clean buffer to the caller.
	return buf
}

// ReleaseBuffer returns buf to the shared pool unless its capacity exceeds
// MaxRetainedBufBytes, in which case it is orphaned for the GC to reclaim.
// Callers MUST NOT use buf — or any slice aliasing buf.Bytes() — after this
// call; clone first if the encoded bytes must outlive the release.
func ReleaseBuffer(buf *bytes.Buffer) {
	//: cap-discard: drop oversized buffers, the GC reclaims them.
	if buf.Cap() > MaxRetainedBufBytes {
		//: orphan the buffer.
		return
	}
	//: pool expects a clean buffer.
	buf.Reset()
	//: return for the next caller.
	bufferPool.Put(buf)
}

// AcquireReader returns a *bytes.Reader from the shared pool, reset to read
// from src. The caller owns it until ReleaseReader. src must stay alive and
// unmodified for as long as the reader is used.
func AcquireReader(src []byte) *bytes.Reader {
	//: pool guarantees a *bytes.Reader via its New func.
	r, ok := readerPool.Get().(*bytes.Reader)
	//: pool invariant guard — never expected to fail at runtime.
	if !ok {
		//: invariant broken — fail loud at the call site.
		panic("internal/core/codec/scratch: readerPool yielded non-*bytes.Reader")
	}
	//: point the reader at src and rewind its cursor.
	r.Reset(src)
	//: hand the positioned reader to the caller.
	return r
}

// ReleaseReader returns r to the shared pool. A bytes.Reader is fixed-size,
// so there is no cap-discard. Callers MUST NOT use r after this call.
func ReleaseReader(r *bytes.Reader) {
	//: no cap check needed — the struct never grows.
	readerPool.Put(r)
}
