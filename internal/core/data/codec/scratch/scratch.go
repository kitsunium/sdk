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

// MaxRetainedSizedBytes is the capacity past which the size-classed tier
// drops a buffer: 4 MiB. Between MaxRetainedBufBytes and it, a released buffer
// joins the class its capacity falls in — up to 1 MiB, up to 4 MiB — so a
// workload of documents past 256 KiB reuses its buffers instead of growing
// one on every call; past it, one oversized payload still cannot pin its
// allocation.
const MaxRetainedSizedBytes int = 4 << 20

// midClassBytes is the ceiling of the smaller size class, and the size its
// new buffers are allocated at (#906).
const midClassBytes int = 1 << 20

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
	//: midClassPool recycles buffers past MaxRetainedBufBytes up to 1 MiB; a
	//: new one is allocated at 1 MiB, so it serves every hint of its class.
	midClassPool = recycler.NewCappedPool[*bytes.Buffer](
		func() *bytes.Buffer { return bytes.NewBuffer(make([]byte, 0, midClassBytes)) },
		func(b *bytes.Buffer) { b.Reset() },
		func(b *bytes.Buffer) int { return b.Cap() },
		midClassBytes,
	)
	//: topClassPool recycles buffers past 1 MiB up to MaxRetainedSizedBytes,
	//: a new one allocated at that size.
	topClassPool = recycler.NewCappedPool[*bytes.Buffer](
		func() *bytes.Buffer { return bytes.NewBuffer(make([]byte, 0, MaxRetainedSizedBytes)) },
		func(b *bytes.Buffer) { b.Reset() },
		func(b *bytes.Buffer) int { return b.Cap() },
		MaxRetainedSizedBytes,
	)
)

// AcquireBuffer returns a clean, zero-length *bytes.Buffer from the shared
// pool. The recycler reset the buffer on its previous Put, so callers append
// directly without re-resetting. The caller owns it until ReleaseBuffer.
func AcquireBuffer() *bytes.Buffer {
	//: already reset on its previous Put — hand the clean buffer to the caller.
	return bufferPool.Get()
}

// AcquireBufferSized returns a clean, zero-length *bytes.Buffer with room for
// at least hint bytes: from the shared pool when hint is within
// MaxRetainedBufBytes, else from the size class hint falls in — up to 1 MiB,
// up to 4 MiB — whose new buffers are allocated at the class's full size, so
// one serves every hint of its class. A codec that knows, or has seen, the
// size of what it encodes passes it, and a document past 256 KiB is encoded
// into a recycled buffer instead of a growth chain. The caller owns it until
// ReleaseBufferSized, which returns it to its class.
func AcquireBufferSized(hint int) *bytes.Buffer {
	var buf *bytes.Buffer
	//: the pool whose buffers can hold hint.
	switch {
	//: the shared pool.
	case hint <= MaxRetainedBufBytes:
		buf = bufferPool.Get()
	//: the 1 MiB class.
	case hint <= midClassBytes:
		buf = midClassPool.Get()
	//: the 4 MiB class; past it, the grow below is the allocation.
	default:
		buf = topClassPool.Get()
	}
	//: a pooled buffer released smaller than its class's ceiling grows once.
	buf.Grow(hint)
	//: clean and wide enough.
	return buf
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

// ReleaseBufferSized returns buf to the pool its capacity belongs to: the
// shared pool within MaxRetainedBufBytes, the size class past it, and none
// past MaxRetainedSizedBytes, where the recycler orphans it for the GC. It is
// the release of a buffer AcquireBufferSized handed out — or of any buffer a
// codec grew past the shared pool's cap and means to reuse. Callers MUST NOT
// use buf, or any slice aliasing buf.Bytes(), after this call.
func ReleaseBufferSized(buf *bytes.Buffer) {
	//: nil-safe no-op, like ReleaseBuffer.
	if buf == nil {
		//: nothing to recycle.
		return
	}
	//: cap-discard + reset run inside the recycler (discard-before-reset).
	switch c := buf.Cap(); {
	//: the shared pool.
	case c <= MaxRetainedBufBytes:
		bufferPool.Put(buf)
	//: the 1 MiB class.
	case c <= midClassBytes:
		midClassPool.Put(buf)
	//: the 4 MiB class, which drops what is past it.
	default:
		topClassPool.Put(buf)
	}
}

// DetachBuffer hands the caller the bytes encoded into buf and ends the
// caller's ownership of buf, by the cheaper of two releases for its size. A
// buffer within MaxRetainedBufBytes is cloned and repooled: the clone is small,
// and the next encode reuses the buffer. An over-cap buffer would pay a full
// copy of a large payload, so it is orphaned instead — the size-classed tier
// is for codecs that ask AcquireBufferSized for their buffer and release it
// with ReleaseBufferSized: the returned slice IS its storage, now the caller's, and buf is
// left for the GC without a reset — a reset would not zero the bytes, but a
// later write through buf would overwrite them. Either way, buf must not be
// used after the call. A nil buf yields nil.
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
