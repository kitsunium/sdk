// Package jsonpatch declares the error codes and the sentinels of the
// JSON-difference package (RFC 6902 operations, not a codec),
// internal/service/data/codec/jsonpatch — range 0.3.90.* (ADR 0143
// service/data/codec/jsonpatch block).
//
// The codes live in the core at the path that mirrors the package emitting
// them (ADR 0160 §2) and keep the values they were allocated with: the LL
// byte 3 records the layer that allocated the range, not the directory that
// declares it today (ADR 0160 §3). Nothing here encodes or decodes — the
// mechanism, and the constructors that attach a failure's detail, stay in
// the service package.
//
// Package jsonpatch — the sentinel *errs.Error values, one per code. Each
// Reason derives from its var name or from its Code constant (ADR 0020);
// the Public and Private texts are the ones the service package always
// emitted, so moving the declaration changed no rendering.
//
// No Public and no Private below carries a byte of either document: a
// document compared is routinely a record holding personal data, and an error
// is where input classically leaks back out.
package jsonpatch

// exitDataErr matches sysexits EX_DATAERR (65): the input was wrong.
const exitDataErr int = 65

// httpBadRequest is RFC 9110 400 Bad Request: a document that is not JSON is
// the caller's input, not a server fault. A literal, because the core does
// not import net/http for a number (ADR 0160).
const httpBadRequest int = 400
