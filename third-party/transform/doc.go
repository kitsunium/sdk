// Package transform — range 0.3.63.* (ADR 0066 third-party/transform block).
//
// Package transform — declares the sentinel *errs.Error values returned by the
// vendor compressors. Each sentinel var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form. The zstdWrap / s2Wrap WrapParams mirror their sentinels
// so a wrapped library cause carries the same Code/Reason/Public on the wire as
// the bare sentinel (origin wins, root CLAUDE.md rule 6).
//
// Package transform — the s2 scheme over github.com/klauspost/compress/s2, the
// same module the zstd scheme uses, so it costs no second dependency.
//
// Package transform wraps the vendor compressors zstd and s2 (both from
// github.com/klauspost/compress) as core/data/transform.Compressor implementations,
// beside the stdlib gzip/flate/zlib schemes in internal/service/data/transform.
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
// Importing this package registers "zstd" and "s2" with the core/data/transform
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
//
// Package transform — the zstd scheme (RFC 8878) over
// github.com/klauspost/compress/zstd.
package transform
