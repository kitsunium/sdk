package crypto

// ResetForTest clears the process-wide AEAD registry back to the empty state.
// Exported from this white-box test file so the external test package
// (crypto_test) can isolate registry mutations: the registry is package-global
// and `go test -count=N` reuses the process (package state is NOT re-initialised
// between iterations), so a test that calls Register must reset first or a later
// iteration panics on a duplicate. Test-only.
func ResetForTest() {
	//: store nil snapshots; Lookup / lookupByID then report empty.
	registry.Store(nil)
	idIndex.Store(nil)
	//: also clear the separate Hasher registry so hash tests isolate too.
	hashers.Store(nil)
}
