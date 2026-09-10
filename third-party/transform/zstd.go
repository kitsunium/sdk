// Package transform — the zstd scheme (RFC 8878) over
// github.com/klauspost/compress/zstd.
package transform

import (
	"errors"

	"github.com/klauspost/compress/zstd"

	coretransform "github.com/kitsunium/sdk/internal/core/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// ZstdAlgorithm is the canonical registry key for the zstd scheme. It matches
// the IANA content coding name, so a caller that already speaks HTTP spells it
// the same way here.
const ZstdAlgorithm coretransform.Algorithm = "zstd"

// ZstdLevel names a zstd compression level. The four values are the four the
// library actually implements; an unrecognised value CLAMPS to ZstdDefault,
// because every level round-trips the caller's bytes identically and so trades
// ratio against CPU rather than carrying the caller's intent (ADR 0031 §clamp,
// the same call internal/service/transform makes for the zlib level).
type ZstdLevel int

const (
	// ZstdFastest is the throughput end of the scale (library level 1).
	ZstdFastest ZstdLevel = 1
	// ZstdDefault is the balanced level (library level 3) and the clamp target.
	ZstdDefault ZstdLevel = 3
	// ZstdBetter trades roughly half the throughput for a few points of ratio
	// (library level 7).
	ZstdBetter ZstdLevel = 7
	// ZstdBest is the ratio end of the scale (library level 11). BENCH.md stops
	// at ZstdBetter, which already compresses SLOWER than stdlib gzip for a
	// sub-one-percent ratio gain; measure your own corpus before going past it.
	ZstdBest ZstdLevel = 11
)

// ZstdCompressor is the concrete core/transform.Compressor for the zstd wire
// format. It holds a shared encoder and decoder because building either per
// call would re-allocate the window buffers on every payload; both are
// documented safe for concurrent EncodeAll / DecodeAll, so one value serves the
// whole process.
//
// It is a POINTER type, unlike the stateless stdlib schemes. Registry
// re-registration therefore compares pointer identity, which keeps
// core/transform.Register's idempotent-republish path working for the singleton
// and turns a second, distinct compressor claiming "zstd" into the boot-time
// panic it is meant to be.
type ZstdCompressor struct {
	// enc is the shared encoder; EncodeAll appends to dst natively.
	enc *zstd.Encoder
	// dec is the shared decoder, built with the output ceiling baked in.
	dec *zstd.Decoder
	// maxDecompressedBytes is the ceiling, retained for the reported field.
	maxDecompressedBytes int64
}

// Zstd is the registered zstd singleton, built at DefaultMaxDecompressedBytes.
// Binding the registration result to a named var is the convention the sibling
// stdlib schemes use and keeps this package clear of init().
//
// Construction acquires no goroutine, no file descriptor and no socket: it is
// measured at a few kilobytes of heap, and the window buffers are allocated
// lazily on first use. That is what makes registering from an import legitimate
// here where ADR 0048 refuses it for an OTLP emitter — see ADR 0066 D3.
var Zstd = coretransform.Register(mustZstd())

// mustZstd builds the default zstd compressor for the package-level singleton.
// It panics on failure because the only reachable failure is a misconfigured
// constant in this file — a compile-time-shaped mistake that must not produce a
// silently absent registration.
func mustZstd() *ZstdCompressor {
	//: the singleton always uses the package default ceiling.
	c, err := NewZstdCompressor(ZstdFastest, DefaultMaxDecompressedBytes)
	//: an error here means this file's own constants are wrong.
	if err != nil {
		//: fail loudly at boot rather than register nothing.
		panic(err.Error())
	}
	//: hand the singleton to Register.
	return c
}

// NewZstdCompressor builds a zstd compressor at the given level, refusing to
// decompress more than maxDecompressedBytes of plaintext in one call.
//
// maxDecompressedBytes is positional and mandatory: see the package doc. A
// non-positive value returns LimitMisconfigured and no compressor. An
// unrecognised level clamps to ZstdDefault.
//
// The returned compressor owns a shared encoder and decoder. A caller that
// builds one per request must Close it; the package singleton is never closed
// because it lives as long as the process.
func NewZstdCompressor(level ZstdLevel, maxDecompressedBytes int64) (compressor *ZstdCompressor, err error) {
	//: refuse an unbounded or nonsensical ceiling before allocating anything.
	if lerr := checkLimit(maxDecompressedBytes); lerr != nil {
		//: no compressor exists that would not bound its output.
		return nil, lerr
	}
	//: the encoder writes to an explicit dst on every call, so nil is the sink.
	enc, eerr := zstd.NewWriter(nil, zstd.WithEncoderLevel(encoderLevel(level)))
	//: a construction failure here is a library-option fault; surface it typed.
	if eerr != nil {
		//: wrap under the zstd sentinel.
		return nil, errs.Wrap(eerr, zstdWrap)
	}
	//: the ceiling is baked into the decoder so it refuses before allocating.
	dec, derr := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(uint64(maxDecompressedBytes)))
	//: an option fault leaves the encoder dangling — release it before failing.
	if derr != nil {
		//: no compressor is returned, so nothing else will ever close enc; the
		//: release error joins the option fault rather than being swallowed.
		return nil, errs.Wrap(errors.Join(derr, enc.Close()), zstdWrap)
	}
	//: both halves built — hand back the compressor.
	return &ZstdCompressor{enc: enc, dec: dec, maxDecompressedBytes: maxDecompressedBytes}, nil
}

// encoderLevel maps a ZstdLevel onto the library's EncoderLevel, clamping any
// unrecognised value to the default rather than refusing it.
func encoderLevel(level ZstdLevel) zstd.EncoderLevel {
	//: the four implemented levels map one-to-one.
	switch level {
	//: the throughput end of the scale.
	case ZstdFastest:
		//: library level 1.
		return zstd.SpeedFastest
	//: half the throughput for a few points of ratio.
	case ZstdBetter:
		//: library level 7.
		return zstd.SpeedBetterCompression
	//: the ratio end of the scale.
	case ZstdBest:
		//: library level 11.
		return zstd.SpeedBestCompression
	//: named explicitly so the clamp target is visible beside its own case.
	case ZstdDefault:
		//: library level 3.
		return zstd.SpeedDefault
	//: every unrecognised value, including the zero.
	default:
		//: anything else clamps — a level never carries the caller's intent.
		return zstd.SpeedDefault
	}
}

// Algorithm implements core/transform.Compressor and returns the registry key.
func (*ZstdCompressor) Algorithm() coretransform.Algorithm {
	//: canonical identifier, frozen per scheme.
	return ZstdAlgorithm
}

// Compress zstd-encodes src and appends the result to dst.
//
// It never fails: EncodeAll has no error return because a buffer-backed encode
// has nothing to fault on. The error result exists to satisfy the Compressor
// contract and is always nil.
func (z *ZstdCompressor) Compress(dst, src []byte) (encoded []byte, err error) {
	//: EncodeAll appends natively, which is exactly the port's dst convention.
	return z.enc.EncodeAll(src, dst), nil
}

// Decompress zstd-decodes src and appends the result to dst, refusing any
// payload whose plaintext would exceed the configured ceiling.
//
// The ceiling counts the DECODED payload only, never len(dst)+payload, so a
// caller reusing a large dst does not silently shrink its own decompression
// budget. That takes two paths: an empty dst is handed straight to DecodeAll
// (where the library's own budget already means exactly the payload), and a
// non-empty one decodes into a fresh buffer that is then appended. The split is
// an allocation, not a semantic — both paths bound the same thing. See
// BENCH.md §"Why decompression takes the empty-dst fast path".
//
// A refusal returns dst at its original length — never a partial payload.
func (z *ZstdCompressor) Decompress(dst, src []byte) (decoded []byte, err error) {
	//: an empty dst has no prefix to protect, so DecodeAll can append straight
	//: into it — and with len(dst)==0 the library's budget counts exactly the
	//: payload, which is the semantics this method promises. This is the buffer
	//: -reuse path the port's dst convention exists for, and it is measured at
	//: half the memory of the general path (BENCH.md).
	if len(dst) == 0 {
		//: decode directly into the caller's buffer.
		out, derr := z.dec.DecodeAll(src, dst)
		//: on failure hand back dst, never the partial output DecodeAll returns.
		if derr != nil {
			//: classify — a bomb and a corrupt frame are different events.
			return dst, z.classify(derr, len(src))
		}
		//: the caller's buffer now holds exactly the payload.
		return out, nil
	}
	//: a non-empty dst must not spend the caller's budget, so decode into a
	//: fresh buffer and append; the ceiling still means the payload alone.
	plain, derr := z.dec.DecodeAll(src, nil)
	//: classify before wrapping — a bomb and a corrupt frame are different events.
	if derr != nil {
		//: hand back dst untouched with the classified error.
		return dst, z.classify(derr, len(src))
	}
	//: append the bounded plaintext onto the caller's dst.
	return append(dst, plain...), nil
}

// classify turns a library decode error into the right sentinel: a tripped
// output bound becomes DecompressionLimitExceeded, anything else becomes
// ZstdFailed.
//
// Both of the library's refusals count as the bound. ErrDecoderSizeExceeded is
// the plain "this decodes past the cap" answer, and ErrWindowSizeExceeded is
// the same refusal taken one step earlier: WithDecoderMaxMemory also caps the
// window a frame may declare, so a frame that ANNOUNCES a window larger than
// the ceiling is rejected from its header, before a byte is decompressed. Both
// are the ceiling doing its job, and reporting the second as a corrupt stream
// would hide the loudest bomb signature behind the quietest label.
func (z *ZstdCompressor) classify(cause error, compressedLen int) error {
	//: both library refusals are the configured ceiling speaking.
	if isLimitError(cause) {
		//: report the bomb refusal with the numbers an operator needs.
		return errs.Wrap(DecompressionLimitExceeded, errs.WrapParams{},
			errs.String("algorithm", string(ZstdAlgorithm)),
			errs.Int64("max_decompressed_bytes", z.maxDecompressedBytes),
			errs.Int("compressed_bytes", compressedLen))
	}
	//: everything else is a malformed or truncated stream.
	return errs.Wrap(cause, zstdWrap)
}

// isLimitError reports whether cause is one of the two library errors that mean
// "the configured output ceiling refused this payload".
func isLimitError(cause error) bool {
	//: ErrDecoderSizeExceeded — the decoded size passed the cap.
	if errors.Is(cause, zstd.ErrDecoderSizeExceeded) {
		//: the ceiling refused it.
		return true
	}
	//: ErrWindowSizeExceeded — the frame header declared a window past the cap.
	return errors.Is(cause, zstd.ErrWindowSizeExceeded)
}

// Close releases the shared encoder and decoder. It is safe to call once; the
// compressor must not be used afterwards. The package singleton is never
// closed — it is owned by the process, not by any caller.
func (z *ZstdCompressor) Close() error {
	//: the decoder's Close has no error to report.
	z.dec.Close()
	//: the encoder's Close flushes nothing here (nil sink) but still releases.
	return z.enc.Close()
}
