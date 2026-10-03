package transform

import "github.com/kitsunium/sdk/internal/kernel/plugin"

// ResetForTest clears the process-wide Compressor registry back to the empty
// state. Exported from this white-box test file so the external test package
// (transform_test) can isolate registry mutations: the registry is
// package-global and `go test -count=N` reuses the process (package state is
// NOT re-initialised between iterations), so a test that calls Register must
// reset first or a later iteration panics on a duplicate. Test-only, and only
// from a test that runs alone: it replaces the table rather than storing into
// it, which nothing may race.
func ResetForTest() {
	//: a fresh, empty table; Lookup then reports empty.
	registry = plugin.Registry[Algorithm, Compressor]{}
}
