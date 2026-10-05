// Package baseenc declares the error codes and the sentinels of the base-N
// codec family (base64, base64url, base32, base16, hex, ascii85, base45,
// base58, base62), internal/service/data/codec/baseenc — range 0.3.24.* (ADR
// 0006 service/data/codec/baseenc block).
//
// The codes live in the core at the path that mirrors the package emitting
// them (ADR 0160 §2) and keep the values they were allocated with: the LL
// byte 3 records the layer that allocated the range, not the directory that
// declares it today (ADR 0160 §3). Nothing here encodes or decodes — the
// mechanism, and the constructors that attach a failure's detail, stay in
// the service package.
//
// Package baseenc — the sentinel *errs.Error values, one per code. Each
// Reason derives from its var name or from its Code constant (ADR 0020);
// the Public and Private texts are the ones the service package always
// emitted, so moving the declaration changed no rendering.
package baseenc
