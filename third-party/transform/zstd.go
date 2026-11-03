package transform

import (
	"bytes"
	"errors"

	"github.com/klauspost/compress/zstd"

	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	"github.com/kitsunium/sdk/internal/kernel/concur/recycler"
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
// the same call internal/service/data/transform makes for the zlib level).
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

// ZstdCompressor is the concrete core/data/transform.Compressor for the zstd wire
// format. It holds a shared encoder and decoder because building either per
// call would re-allocate the window buffers on every payload; both are
// documented safe for concurrent EncodeAll / DecodeAll, so one value serves the
// whole process.
//
// It is a POINTER type, unlike the stateless stdlib schemes. Registry
// re-registration therefore compares pointer identity, which keeps
// core/data/transform.Register's idempotent-republish path working for the singleton
// and turns a second, distinct compressor claiming "zstd" into the boot-time
// panic it is meant to be.
type ZstdCompressor struct {
	// enc is the shared encoder; EncodeAll appends to dst natively.
	enc *zstd.Encoder
	// dec is the shared decoder, built with the output ceiling baked in.
	dec *zstd.Decoder
	// maxDecompressedBytes is the ceiling, retained for the reported field.
	maxDecompressedBytes int64
	// level is the encoder level, kept for the encoders NewWriter builds.
	level zstd.EncoderLevel
	// dictionaryID is the ID of the dictionary enc writes with and dec reads,
	// 0 when the compressor has none; it is what a DictionaryMismatch reports.
	dictionaryID uint32
	// dict is the compressor's own copy of the dictionary, nil when it has
	// none: the library keeps slices of it, so it must not be the caller's.
	dict []byte
	// streamDecoders and streamEncoders recycle the stream codecs NewReader
	// and NewWriter hand out (ADR 0213), built with this compressor's options:
	// a stream decoder's history and an encoder's window are megabytes, built
	// per stream otherwise.
	streamDecoders *recycler.Pool[*zstd.Decoder]
	streamEncoders *recycler.Pool[*zstd.Encoder]
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
	//: no dictionary: the library's options alone.
	return newZstd(level, maxDecompressedBytes, nil, 0)
}

// NewZstdCompressorWithDictionary builds a zstd compressor at the given level
// that encodes with dict and decodes the frames that name it, refusing to
// decompress more than maxDecompressedBytes of plaintext in one call.
//
// dict is a zstd dictionary in the format "zstd --train" and zstd.BuildDict
// write — a magic number, a non-zero ID and the entropy tables trained on
// samples of the payloads it serves. It is copied, so the caller's slice may
// change afterwards. An empty or malformed dictionary returns
// DictionaryInvalid and no compressor; a non-positive ceiling
// LimitMisconfigured, as NewZstdCompressor does.
//
// Every frame the compressor writes names the dictionary's ID, and a frame
// naming an ID the reader does not hold is refused with DictionaryMismatch —
// by this compressor, by one built with another dictionary, and by the
// registered Zstd — never decoded into a corrupt payload. A frame without a
// dictionary still decodes. The ID is how a dictionary is versioned: train a
// new one under a new ID, and a reader that meets an old frame learns from
// the error which dictionary it needs.
//
// The result is not registered: a dictionary is the caller's artefact, trained
// on the caller's data, and the registry's "zstd" is the dictionary-less
// singleton. The decoder's window is bounded by maxDecompressedBytes, as
// NewZstdCompressor's is. Close it when done.
func NewZstdCompressorWithDictionary(level ZstdLevel, maxDecompressedBytes int64, dict []byte) (compressor *ZstdCompressor, err error) {
	//: the ceiling first, as the plain constructor checks it.
	if lerr := checkLimit(maxDecompressedBytes); lerr != nil {
		//: no compressor exists that would not bound its output.
		return nil, lerr
	}
	//: an empty dictionary is no dictionary, and claiming one would be a lie.
	if len(dict) == 0 {
		//: refused, never defaulted to the dictionary-less compressor.
		return nil, errs.Wrap(DictionaryInvalid, errs.WrapParams{}, errs.Int("dictionary_bytes", 0))
	}
	//: the compressor's own copy: the library keeps slices of what it loads.
	owned := bytes.Clone(dict)
	//: parse it once here, so a malformed one is named as such and not as a
	//: library option fault.
	parsed, derr := zstd.InspectDictionary(owned)
	//: no magic, an ID of zero, truncated tables.
	if derr != nil {
		//: the library's reason kept beneath the sentinel.
		return nil, errs.Wrap(derr, dictionaryInvalidWrap, errs.Int("dictionary_bytes", len(dict)))
	}
	//: encoder and decoder share the one copy.
	return newZstd(level, maxDecompressedBytes, owned, parsed.ID())
}

// newZstd builds the compressor's encoder and decoder, with dict when it is
// not nil. The ceiling has been checked by the caller.
func newZstd(level ZstdLevel, maxDecompressedBytes int64, dict []byte, dictID uint32) (compressor *ZstdCompressor, err error) {
	encOpts := []zstd.EOption{zstd.WithEncoderLevel(encoderLevel(level))}
	//: WithDecoderMaxMemory also caps the window a frame may declare, so the
	//: ceiling bounds the decoder's memory as well as its output.
	decOpts := []zstd.DOption{zstd.WithDecoderMaxMemory(uint64(maxDecompressedBytes))}
	//: a dictionary rides on both halves, or on neither.
	if dict != nil {
		encOpts = append(encOpts, zstd.WithEncoderDict(dict))
		decOpts = append(decOpts, zstd.WithDecoderDicts(dict))
	}
	//: the encoder writes to an explicit dst on every call, so nil is the sink.
	enc, eerr := zstd.NewWriter(nil, encOpts...)
	//: a construction failure here is a library-option fault; surface it typed.
	if eerr != nil {
		//: wrap under the zstd sentinel.
		return nil, errs.Wrap(eerr, zstdWrap)
	}
	//: the ceiling is baked into the decoder so it refuses before allocating.
	dec, derr := zstd.NewReader(nil, decOpts...)
	//: an option fault leaves the encoder dangling — release it before failing.
	if derr != nil {
		//: no compressor is returned, so nothing else will ever close enc; the
		//: release error joins the option fault rather than being swallowed.
		return nil, errs.Wrap(errors.Join(derr, enc.Close()), zstdWrap)
	}
	//: both halves built — hand back the compressor.
	return &ZstdCompressor{
		enc: enc, dec: dec, maxDecompressedBytes: maxDecompressedBytes,
		level: encoderLevel(level), dictionaryID: dictID, dict: dict,
		//: empty pools: a stream codec is built on its first miss only.
		streamDecoders: recycler.NewPool(func() *zstd.Decoder { return nil }),
		streamEncoders: recycler.NewPool(func() *zstd.Encoder { return nil }),
	}, nil
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

// Algorithm implements core/data/transform.Compressor and returns the registry key.
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
// budget: the library counts len(dst) into its budget, so the decode is handed
// dst's TAIL — a zero-length slice over its spare capacity — where the budget
// means exactly the payload. When the payload fits that capacity the decode
// lands in it, with no allocation and no copy; when it does not, the library
// grows its own buffer and that is appended onto dst once. See BENCH.md
// §"Decompression decodes into the tail".
//
// A refusal returns dst at its original length — never a partial payload.
func (z *ZstdCompressor) Decompress(dst, src []byte) (decoded []byte, err error) {
	start := len(dst)
	//: the tail: no length, so the library's budget counts the payload alone,
	//: and dst's spare capacity, so the decode can land in it.
	tail := dst[start:]
	out, derr := z.dec.DecodeAll(src, tail)
	//: on failure hand back dst, never the partial output DecodeAll returns.
	if derr != nil {
		//: classify — a bomb and a corrupt frame are different events.
		return dst, z.classify(derr, src)
	}
	return joinTail(dst, tail, out), nil
}

// joinTail returns dst followed by out, where out is what a decode appended to
// tail, dst's zero-length tail. When out is still in dst's array — the payload
// fitted the spare capacity — dst is extended over it, with no copy; when the
// decoder outgrew the capacity, out is a buffer of its own and is appended
// onto dst once. An empty dst has no prefix to keep, so out is dst then, as
// append would make it.
func joinTail(dst, tail, out []byte) []byte {
	//: nothing decoded: dst as it came.
	if len(out) == 0 {
		return dst
	}
	//: no prefix to keep: the decoder's buffer is the result either way.
	if len(dst) == 0 {
		return out
	}
	//: the decode stayed in dst's array: the prefix is already in front of it.
	if cap(tail) > 0 && &out[0] == &tail[:1][0] {
		return dst[:len(dst)+len(out)]
	}
	//: the decoder reallocated past dst's capacity: one copy onto dst.
	return append(dst, out...)
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
func (z *ZstdCompressor) classify(cause error, src []byte) error {
	compressedLen := len(src)
	//: a frame naming a dictionary this compressor does not hold.
	if errors.Is(cause, zstd.ErrUnknownDictionary) {
		//: refused, never decoded into a corrupt payload; the IDs say which.
		return errs.Wrap(DictionaryMismatch, errs.WrapParams{},
			errs.String("algorithm", string(ZstdAlgorithm)),
			errs.Int64("frame_dictionary_id", int64(frameDictionaryID(src))),
			errs.Int64("dictionary_id", int64(z.dictionaryID)),
			errs.Int("compressed_bytes", compressedLen))
	}
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

// frameDictionaryID reads the dictionary ID src's first frame header names, 0
// when it names none or the header does not parse. It is read for the error's
// fields only, after the library has refused the frame.
func frameDictionaryID(src []byte) uint32 {
	var header zstd.Header
	//: a header that does not parse names no dictionary.
	if header.Decode(src) != nil {
		return 0
	}
	//: the ID the frame was written with.
	return header.DictionaryID
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

// DictionaryID returns the ID of the dictionary the compressor encodes with,
// and the frames it writes name — 0 when it has none. Store it beside the
// payloads to know which dictionary reads them back.
func (z *ZstdCompressor) DictionaryID() uint32 {
	//: fixed at construction.
	return z.dictionaryID
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
