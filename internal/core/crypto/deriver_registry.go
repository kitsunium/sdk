// Package crypto — range 0.2.4.* (ADR 0013 core/crypto block).
//
// Package crypto — the key-derivation port implemented by each KDF scheme.
//
// Package crypto — the process-wide Deriver registry + Subkey dispatch.
package crypto

// derivers maps each Algorithm to its Deriver. A distinct capability beside
// AEAD/Hasher/Signer; backed by the shared read-mostly schemeRegistry — register
// once at import, dispatch is lock-free.
var derivers = schemeRegistry[Deriver]{verb: "RegisterDeriver"}

// RegisterDeriver inserts d under d.Algorithm() and returns it so callers can
// bind the singleton to a typed package-level variable like
// `var Deriver = crypto.RegisterDeriver(hkdfSHA256{})`. Panics on a nil deriver
// or when a distinct deriver already claims the same Algorithm.
//
// IFACE-PLUGIN: the registry hands plug-in Deriver instances back to callers so
// each scheme keeps its concrete type unexported; the stable contract is the
// Deriver interface itself.
//
// "Nil" here means UNUSABLE, not only an untyped nil: a typed nil pointer and a
// plug-in whose type is not comparable both satisfy the port and neither can
// serve one call (see internal/kernel/plugin).
func RegisterDeriver(d Deriver) Deriver {
	//: refuse an unusable deriver, then publish it under its Algorithm; both
	//: refusals panic at boot with the dotted-quad code.
	return derivers.register(d)
}

// LookupDeriver returns the Deriver registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in Deriver instances behind the Deriver
// interface — concrete types are intentionally unexported per scheme.
func LookupDeriver(name Algorithm) (d Deriver, ok bool) {
	//: a lock-free snapshot read; a miss hands back nil AND false.
	return derivers.table.Lookup(name)
}

// AvailableDerivers returns the sorted list of registered KDF Algorithms.
func AvailableDerivers() []Algorithm {
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
