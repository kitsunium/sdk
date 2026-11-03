// Package jcs declares the error codes and the sentinels of the RFC 8785
// canonicalization package, internal/service/data/codec/jcs — range
// 0.3.102.* (ADR 0102 amendment of 2026-11-03).
//
// The codes live in the core at the path that mirrors the package emitting
// them (ADR 0160 §2). Nothing here encodes or decodes.
//
// Package jcs — the sentinel *errs.Error values, one per code. No Public and
// no Private below carries a byte of the document: what is signed is
// routinely a record holding personal data.
package jcs
