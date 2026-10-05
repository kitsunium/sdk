package scratch

import (
	"bytes"
	"slices"

	"github.com/kitsunium/sdk/internal/kernel/concur/recycler"
)

// MaxRetainedBufBytes is the project-wide cap-discard threshold for pooled
// codec buffers. A buffer whose capacity exceeds this is dropped on release
// rather than re-pooled, so a one-off oversized payload cannot pin a large
// allocation for the lifetime of the pool's GC window. It bounds the shared
// *bytes.Buffer pool below, and it is also the ceiling a codec passes to a
// recycler.CappedPool of its own when it pools a buffer shape this package
// does not offer — so the codec domain keeps one threshold, not one per codec.
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

// DetachBuffer hands the caller the bytes encoded into buf and ends the
// caller's ownership of buf, by the cheaper of two releases for its size. A
// buffer within MaxRetainedBufBytes is cloned and repooled: the clone is small,
// and the next encode reuses the buffer. An over-cap buffer would pay a full
// copy of a large payload AND be dropped by the pool anyway, so it is orphaned
// instead: the returned slice IS its storage, now the caller's, and buf is left
// for the GC without a reset — a reset would not zero the bytes, but a later
// write through buf would overwrite them. Either way, buf must not be used
// after the call. A nil buf yields nil.
func DetachBuffer(buf *bytes.Buffer) []byte {
	//: nil-safe, like ReleaseBuffer: nothing encoded, nothing to hand over.
	if buf == nil {
		//: no bytes and no buffer to release.
		return nil
	}
	//: an over-cap buffer: hand its storage over rather than clone it.
	if buf.Cap() > MaxRetainedBufBytes {
		//: caller-owned now — never reset, never repooled.
		return buf.Bytes()
	}
	//: clone first, so the caller's slice does not alias a pooled buffer the
	//: next caller would overwrite.
	out := slices.Clone(buf.Bytes())
	//: reset + repool (the cap is under the threshold).
	ReleaseBuffer(buf)
	//: the caller's own copy.
	return out
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
