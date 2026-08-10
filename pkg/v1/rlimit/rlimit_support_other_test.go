//go:build !unix

// Package rlimit_test — build-tagged capability flag for non-Unix targets, where
// setrlimit(2) has no equivalent and every entry point degrades to
// UnsupportedPlatform. The //go:build unix sibling sets nativeRlimit true.
package rlimit_test

// nativeRlimit reports whether this platform applies resource limits natively.
// False on non-Unix targets (Windows, plan9, js/wasm).
const nativeRlimit = false
