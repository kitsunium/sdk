// Package form declares the error codes and the sentinels of the
// application/x-www-form-urlencoded codec, internal/service/data/codec/form —
// range 0.3.40.* (ADR 0005 service/data/codec/form block).
//
// The codes live in the core at the path that mirrors the package emitting
// them (ADR 0160 §2) and keep the values they were allocated with: the LL
// byte 3 records the layer that allocated the range, not the directory that
// declares it today (ADR 0160 §3). Nothing here encodes or decodes — the
// mechanism, and the constructors that attach a failure's detail, stay in
// the service package.
//
// Package form — the sentinel *errs.Error values, one per code. Each
// Reason derives from its var name or from its Code constant (ADR 0020);
// the Public and Private texts are the ones the service package always
// emitted, so moving the declaration changed no rendering.
//
// There is deliberately no MarshalFailed sentinel: once the codec has
// accepted the argument shape, percent-escaping a map of strings is a total
// function — no byte sequence can make it fail. Defining an unreachable
// sentinel would put a code in the registry that no test can ever provoke.
package form
