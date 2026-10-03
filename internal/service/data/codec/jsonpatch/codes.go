// Package jsonpatch — range 0.3.90.* (ADR 0143 service/data/codec/jsonpatch block).
package jsonpatch

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.90.0 - 0.3.90.255

// CodeNotJSON identifies a document to compare that is not exactly one JSON
// value, read strictly: a syntax error, a truncation, trailing data, a
// duplicated member name, invalid UTF-8, or nesting past the reader's bound.
const CodeNotJSON errs.Code = 0x00_03_5A_01 // 0.3.90.1
