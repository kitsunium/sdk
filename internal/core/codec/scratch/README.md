# scratch

Shared, size-bounded buffer recycling for the SDK's service codecs.

`scratch` centralises the `*bytes.Buffer` pooling that every codec
previously reimplemented: one shared `sync.Pool`, one cap-discard
threshold, one acquire/release pair.

## API

```go
const MaxRetainedBufBytes = 256 << 10

func AcquireBuffer() *bytes.Buffer    // returns an already-Reset buffer
func ReleaseBuffer(buf *bytes.Buffer) // repools, or drops if Cap > MaxRetainedBufBytes

func AcquireReader(src []byte) *bytes.Reader // returns a reader positioned at src
func ReleaseReader(r *bytes.Reader)          // repools (no cap-discard — fixed-size)
```

## Usage

```go
buf := scratch.AcquireBuffer()
// ... encode into buf ...
out := slices.Clone(buf.Bytes()) // clone before release if bytes escape
scratch.ReleaseBuffer(buf)
return out
```

## Lifetime

A buffer is caller-owned from `AcquireBuffer` until `ReleaseBuffer`. After
release, neither the buffer nor any slice aliasing `buf.Bytes()` may be
used. Clone the encoded bytes first if they must outlive the release.

A single shared pool is used deliberately: `sync.Pool` is per-P sharded,
so consolidation raises the reuse rate without adding contention.

This package is internal to the SDK; consumers reach codecs through
`pkg/v1/codec`.
