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
package jsonpatch

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.90.0 - 0.3.90.255

// CodeNotJSON identifies a document to compare that is not exactly one JSON
// value, read strictly: a syntax error, a truncation, trailing data, a
// duplicated member name, invalid UTF-8, or nesting past the reader's bound.
const CodeNotJSON errs.Code = 0x00_03_5A_01 // 0.3.90.1
