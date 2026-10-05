package crypto

// derivers maps each Algorithm to its Deriver. A distinct capability beside
// AEAD/Hasher/Signer; backed by the shared read-mostly schemeRegistry — register
// once at import, dispatch is lock-free.
var derivers = schemeRegistry[Deriver]{verb: "RegisterDeriver"}

// registerDeriver is RegisterDeriver's body: decl_gen.go writes RegisterDeriver, from the
// design, as one call of it.
//
// IFACE-PLUGIN: the registry hands plug-in Deriver instances back to callers so
// each scheme keeps its concrete type unexported; the stable contract is the
// Deriver interface itself.
func registerDeriver(d Deriver) Deriver {
	//: refuse an unusable deriver, then publish it under its Algorithm; both
	//: refusals panic at boot with the dotted-quad code.
	return derivers.register(d)
}

// lookupDeriver is LookupDeriver's body: decl_gen.go writes LookupDeriver, from the
// design, as one call of it.
//
// IFACE-PLUGIN: the registry stores plug-in Deriver instances behind the Deriver
// interface — concrete types are intentionally unexported per scheme.
func lookupDeriver(name Algorithm) (d Deriver, ok bool) {
	//: a lock-free snapshot read; a miss hands back nil AND false.
	return derivers.table.Lookup(name)
}

// availableDerivers is AvailableDerivers's body: decl_gen.go writes AvailableDerivers, from the
// design, as one call of it.
func availableDerivers() []Algorithm {
	//: sorted ascending, the caller's own slice; nil before any registration.
	return derivers.table.Names()
}

// Subkey derives a length-byte subkey from secret (with optional salt and the
// context label info) using the Deriver registered as name. A name with no
// registered deriver returns UnknownKDFAlgorithm (blank-import the scheme's
// package to register it); an over-long length returns DerivationFailed.
func Subkey(name Algorithm, secret, salt []byte, info string, length int) (subkey []byte, err error) {
	//: resolve the deriver first so a missing import surfaces a clear sentinel.
	deriver, ok := LookupDeriver(name)
	//: absence path — the deriver package was never blank-imported.
	if !ok {
		//: surface the documented sentinel naming the missing algorithm.
		return nil, UnknownKDFAlgorithm
	}
	//: delegate derivation; the scheme guards its own maximum output length.
	return deriver.Derive(secret, salt, info, length)
}
