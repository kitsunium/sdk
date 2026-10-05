// Package strictjson declares the error codes and the sentinels of the strict
// JSON decoder (not a codec), internal/service/data/codec/strictjson — range
// 0.3.72.* (ADR 0102 service/data/codec/strictjson block).
//
// The codes live in the core at the path that mirrors the package emitting
// them (ADR 0160 §2) and keep the values they were allocated with: the LL
// byte 3 records the layer that allocated the range, not the directory that
// declares it today (ADR 0160 §3). Nothing here encodes or decodes — the
// mechanism, and the constructors that attach a failure's detail, stay in
// the service package.
//
// Package strictjson — the sentinel *errs.Error values, one per code. Each
// Reason derives from its var name or from its Code constant (ADR 0020);
// the Public and Private texts are the ones the service package always
// emitted, so moving the declaration changed no rendering.
//
// Every Public and every Private below is a fixed sentence. None of them can
// carry a byte of the document, because the document is what a caller least
// controls and an error message is where input classically leaks back out —
// into a response, into a log someone else reads.
package strictjson
