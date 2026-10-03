package crypto

import "github.com/kitsunium/sdk/internal/kernel/plugin"

// ResetForTest clears the process-wide AEAD registry back to the empty state.
// Exported from this white-box test file so the external test package
// (crypto_test) can isolate registry mutations: the registry is package-global
// and `go test -count=N` reuses the process (package state is NOT re-initialised
// between iterations), so a test that calls Register must reset first or a later
// iteration panics on a duplicate. Test-only, and only from a test that runs
// alone: it replaces the tables rather than storing into them, which nothing
// may race.
func ResetForTest() {
	//: fresh, empty tables; Lookup / lookupByID then report empty.
	aeads.table = plugin.Registry[Algorithm, AEAD]{}
	idIndex = plugin.Registry[byte, AEAD]{}
	//: also clear the separate Hasher registry so hash tests isolate too.
	hashers.table = plugin.Registry[Algorithm, Hasher]{}
	//: and the Signer registry, so signature tests isolate as well.
	signers.table = plugin.Registry[Algorithm, Signer]{}
	//: and the Deriver registry, so KDF tests isolate too.
	derivers.table = plugin.Registry[Algorithm, Deriver]{}
	//: and the PasswordHasher registry, so password tests isolate as well.
	passwordHashers.table = plugin.Registry[Algorithm, PasswordHasher]{}
}
