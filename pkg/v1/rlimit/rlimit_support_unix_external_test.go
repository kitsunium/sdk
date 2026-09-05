//go:build unix

// Package rlimit_test — build-tagged capability flag mirroring the one the
// service package carries. Every Unix target applies rlimits natively
// (setrlimit(2)), so the portable cases in rlimit_external_test.go expect
// success / UnknownResource here rather than the non-Unix UnsupportedPlatform
// degrade.
package rlimit_test

// nativeRlimit reports whether this platform applies resource limits natively.
// True on every Unix target; the //go:build !unix sibling sets it false.
const nativeRlimit = true
