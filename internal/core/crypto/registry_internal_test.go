package crypto

// ResetForTest clears the process-wide AEAD registry back to the empty state.
// Exported from this white-box test file so the external test package
// (crypto_test) can isolate registry mutations: the registry is package-global
// and `go test -count=N` reuses the process (package state is NOT re-initialised
// between iterations), so a test that calls Register must reset first or a later
// iteration panics on a duplicate. Test-only.
func ResetForTest() {
	//: store nil snapshots; Lookup / lookupByID then report empty.
	aeads.store.Store(nil)
	idIndex.Store(nil)
	//: also clear the separate Hasher registry so hash tests isolate too.
	hashers.store.Store(nil)
	//: and the Signer registry, so signature tests isolate as well.
	signers.store.Store(nil)
	//: and the Deriver registry, so KDF tests isolate too.
	derivers.store.Store(nil)
	//: and the PasswordHasher registry, so password tests isolate as well.
	passwordHashers.store.Store(nil)
}
