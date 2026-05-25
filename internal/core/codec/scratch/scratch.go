// Package scratch provides shared, size-bounded recycling primitives for the
// service codecs. Every codec that encodes into a transient *bytes.Buffer
// previously declared its own sync.Pool, its own 256 KiB cap-discard constant,
// and its own release helper. Centralising them here gives a single source of
// truth for the cap-discard policy and a single shared pool whose reuse rate
// is higher than nine independent pools (sync.Pool is internally per-P
// sharded, so consolidation does not add contention).
//
// The pooling MECHANISM lives in internal/kernel/recycler (ADR 0010); scratch
// is a codec-domain consumer that keeps the 256 KiB threshold and the concrete
// *bytes.Buffer / *bytes.Reader types here.
//
// Lifetime contract: a value returned by an Acquire* call is owned by the
// caller until the matching Release* call. After Release the value — and any
// slice aliasing it (e.g. buf.Bytes()) — MUST NOT be used; clone the encoded
// bytes first if they need to outlive the release.
package scratch

import (
	"bytes"

	"github.com/kitsunium/sdk/internal/kernel/recycler"
)

// MaxRetainedBufBytes is the project-wide cap-discard threshold for pooled
// *bytes.Buffer instances. A buffer whose capacity exceeds this is dropped on
// release rather than re-pooled, so a one-off oversized payload cannot pin a
// large allocation for the lifetime of the pool's GC window.
const MaxRetainedBufBytes int = 256 << 10

// Shared recycling pools for the codecs, backed by the kernel recycler. One
// shared pool per pooled type — sync.Pool is internally per-P sharded, so
// consolidating across codecs raises the reuse rate without adding contention.
var (
	//: bufferPool recycles *bytes.Buffer with cap-discard at MaxRetainedBufBytes.
	//: reset-on-Put (b.Reset) keeps the next caller's buffer clean; the
	//: recycler discards before reset, so an over-cap buffer whose Bytes() the
	//: caller still holds is orphaned untouched (detach contract preserved).
	bufferPool = recycler.NewCappedPool[*bytes.Buffer](
		func() *bytes.Buffer { return new(bytes.Buffer) },
		func(b *bytes.Buffer) { b.Reset() },
		func(b *bytes.Buffer) int { return b.Cap() },
		MaxRetainedBufBytes,
	)
	//: readerPool recycles *bytes.Reader. A bytes.Reader is a fixed-size struct
	//: repositioned by AcquireReader's Reset(src), so it needs neither
	//: cap-discard nor reset-on-Put — a plain Pool is the right fit.
	readerPool = recycler.NewPool[*bytes.Reader](func() *bytes.Reader { return new(bytes.Reader) })
)

// AcquireBuffer returns a clean, zero-length *bytes.Buffer from the shared
// pool. The recycler reset the buffer on its previous Put, so callers append
// directly without re-resetting. The caller owns it until ReleaseBuffer.
func AcquireBuffer() *bytes.Buffer {
	//: already reset on its previous Put — hand the clean buffer to the caller.
	return bufferPool.Get()
}

// ReleaseBuffer returns buf to the shared pool unless its capacity exceeds
// MaxRetainedBufBytes, in which case the recycler orphans it for the GC.
// Callers MUST NOT use buf — or any slice aliasing buf.Bytes() — after this
// call; clone first if the encoded bytes must outlive the release.
func ReleaseBuffer(buf *bytes.Buffer) {
	//: nil-safe no-op — the generic CappedPool cannot nil-check *bytes.Buffer.
	if buf == nil {
		//: nothing to recycle.
		return
	}
	//: cap-discard + reset run inside the recycler (discard-before-reset).
	bufferPool.Put(buf)
}

// AcquireReader returns a *bytes.Reader from the shared pool, reset to read
// from src. The caller owns it until ReleaseReader; src must stay alive and
// unmodified for as long as the reader is used.
func AcquireReader(src []byte) *bytes.Reader {
	//: borrow a pooled reader from the recycler.
	r := readerPool.Get()
	//: Reset needs the runtime src, so the reposition stays consumer-side here.
	r.Reset(src)
	//: hand the positioned reader to the caller.
	return r
}

// ReleaseReader returns r to the shared pool. A bytes.Reader is fixed-size, so
// there is no cap-discard. Callers MUST NOT use r after this call.
func ReleaseReader(r *bytes.Reader) {
	//: nil-safe no-op so callers can defer ReleaseReader unconditionally.
	if r == nil {
		//: nothing to recycle.
		return
	}
	//: drop the caller's src before pooling so an idle reader cannot keep the
	//: backing array alive (Reset is re-applied with the real src on Acquire).
	r.Reset(nil)
	//: return the reader for reuse; the next AcquireReader repositions it.
	readerPool.Put(r)
}
