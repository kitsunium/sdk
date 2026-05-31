// Package transform — shared decompression bound for the stdlib gzip/flate
// schemes. Each Decompress reads through an io.LimitReader so a malformed or
// hostile stream cannot drive an unbounded allocation at this layer. The full
// self-describing decompression-bomb guard (max output size + max expansion
// ratio keyed to the frame header) lives in the pkg/v1/codec frame layer (a
// later commit, CodeCompressedFrameInvalid); this is the conservative
// layer-local backstop ADR 0014 D1 asks for.
package transform

import "io"

// maxDecompressedBytes caps how many plaintext bytes a single Decompress call
// at this layer will materialise. 256 MiB is generous for log/record payloads
// yet bounds a decompression bomb to a known ceiling; callers needing more route
// through the frame layer's ratio-aware guard. A stream exceeding the cap yields
// the scheme's failure sentinel rather than an OOM.
const maxDecompressedBytes int64 = 256 << 20 // 256 MiB

// readAllBounded drains r into a fresh buffer, refusing to materialise more than
// max plaintext bytes. It reads one byte past max so an over-cap stream is
// detected as overflow rather than silently truncated; the caller maps overflow
// to its failure sentinel. max is an explicit parameter (production passes
// maxDecompressedBytes) so the overflow path can be exercised against a small
// cap in tests without materialising the production ceiling and without mutating
// any shared state (keeping the parallel scheme tests race-free).
func readAllBounded(r io.Reader, max int64) (plain []byte, overflow bool, err error) {
	//: LimitReader stops the underlying read at max+1 so we can tell a
	//: legitimately-cap-sized payload from one that wanted to exceed the cap.
	limited := io.LimitReader(r, max+1)
	//: ReadAll over the bounded reader is the single allocation point.
	buf, readErr := io.ReadAll(limited)
	//: a read fault (corrupt stream) is surfaced verbatim for the caller to wrap.
	if readErr != nil {
		//: hand the raw error back; the scheme wraps it with its sentinel.
		return nil, false, readErr
	}
	//: more than the cap means the stream tried to exceed the bomb ceiling.
	if int64(len(buf)) > max {
		//: signal overflow so the caller returns its failure sentinel.
		return nil, true, nil
	}
	//: within bounds — hand back the decoded plaintext.
	return buf, false, nil
}
