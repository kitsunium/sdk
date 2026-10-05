package transform

import "github.com/kitsunium/sdk/internal/kernel/errs"

// DefaultMaxDecompressedBytes is the ceiling the two registered singletons are
// built with: 64 MiB.
//
// It is deliberately TIGHTER than the 256 MiB backstop the stdlib schemes use
// in internal/service/data/transform, and the reason is arithmetic rather than
// taste. DEFLATE's maximum expansion is roughly 1032:1, so reaching 256 MiB
// through gzip costs an attacker ~254 KiB of upload. Both schemes here expand
// far harder — measured in this package's own tests, 13 KiB of zstd and 28
// BYTES of s2 each declare 64 MiB — so the same ceiling would be four orders of
// magnitude cheaper to reach. The number that matters is what an attacker
// spends, not what the format is called.
//
// A caller who legitimately handles larger payloads passes its own ceiling to
// NewZstdCompressor / NewS2Compressor; the constant is only what the
// import-time singletons use.
const DefaultMaxDecompressedBytes int64 = 64 << 20

// checkLimit validates a caller-supplied decompression ceiling. It returns
// LimitMisconfigured for anything that is not a positive byte count.
//
// ADR 0031 asks each knob to pick a side: clamp where an SDK-chosen default
// needs no explanation, refuse where any value the SDK picked would be
// arbitrary. A ceiling is the second kind. Zero has two natural readings — "no
// limit" and "refuse everything" — which are opposites, and a caller who typed
// it meant one of them; defaulting silently grants what one reader wanted to
// forbid. Contrast zstdLevel below, which clamps: every level round-trips the
// caller's bytes, so a clamped level costs CPU or ratio and never correctness.
func checkLimit(maxDecompressedBytes int64) error {
	//: a ceiling that does not bound anything is not a ceiling.
	if maxDecompressedBytes <= 0 {
		//: refuse at construction so an unbounded compressor never exists.
		return errs.Wrap(LimitMisconfigured, errs.WrapParams{},
			errs.Int64("max_decompressed_bytes", maxDecompressedBytes))
	}
	//: a positive ceiling is accepted verbatim — the caller owns the number.
	return nil
}
