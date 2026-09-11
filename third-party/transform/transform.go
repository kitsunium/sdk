// Package transform wraps the vendor compressors zstd and s2 (both from
// github.com/klauspost/compress) as core/transform.Compressor implementations,
// beside the stdlib gzip/flate/zlib schemes in internal/service/transform.
//
// # Why it lives under third-party/
//
// Not because the library drags a dependency graph — measured, it drags none:
// github.com/klauspost/compress requires no other module, is pure Go, needs no
// cgo, and cross-compiles on the whole ADR 0018 matrix. The reason is the OTHER
// criterion ADR 0034 §Decision.3 names: it imposes a dependency on consumers who
// never use the integration. internal/service and pkg are dep-light on purpose,
// and a consumer who only wants a logger should not resolve, download, verify
// and link a compression library to get one. third-party/ lives in the ROOT
// module, which nothing requires — so the cost is paid by the blank-import and
// by nobody else.
//
// The downgrade narrative ADR 0022 once used is not cited here; ADR 0034
// retired it as a placement rule, and under minimal version selection it cannot
// happen (a dependency requiring a LOWER version never lowers a higher
// requirement that is still present).
//
// # Opt-in
//
// Importing this package registers "zstd" and "s2" with the core/transform
// registry. Nothing in internal/* or pkg/v1/* imports it, so a consumer who
// does not name it never compiles it. That is the same contract
// third-party/codec/hcl has for the "hcl" Format.
//
// # The output bound is a parameter, not an option
//
// Every constructor here takes maxDecompressedBytes as a REQUIRED positional
// argument. A functional option can be omitted; a positional parameter cannot,
// and a security bound that can be omitted is not mandatory. A non-positive
// value is refused at construction rather than defaulted — see
// LimitMisconfigured and ADR 0066 D4.
package transform

import "github.com/kitsunium/sdk/internal/kernel/errs"

// DefaultMaxDecompressedBytes is the ceiling the two registered singletons are
// built with: 64 MiB.
//
// It is deliberately TIGHTER than the 256 MiB backstop the stdlib schemes use
// in internal/service/transform, and the reason is arithmetic rather than
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
