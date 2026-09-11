// Package crypto — the process-wide Deriver registry + Subkey dispatch.
package crypto

import (
	"fmt"

	"github.com/kitsunium/sdk/internal/kernel/plugin"
)

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
	//: a typed nil and a non-comparable plug-in both satisfy the port and
	//: neither can serve — refuse at import, where the offender is named.
	if why := plugin.Unusable(d); why != "" {
		//: panic so the offender is visible at boot.
		panic(fmt.Sprintf("crypto.RegisterDeriver [%s DUPLICATE_REGISTRATION]: %s", CodeDuplicateRegistration, why))
	}
	//: publish via the shared registry; a distinct duplicate Name is a hard conflict.
	if err := derivers.publish(d.Algorithm(), d); err != nil {
		//: surface the doc code for grep-friendly panic messages.
		panic(err.Error())
	}
	//: returning the deriver lets callers bind it to a typed singleton var.
	return d
}

// LookupDeriver returns the Deriver registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in Deriver instances behind the Deriver
// interface — concrete types are intentionally unexported per scheme.
func LookupDeriver(name Algorithm) (d Deriver, ok bool) {
	//: delegate to the shared registry's typed lookup.
	return derivers.lookup(name)
}

// AvailableDerivers returns the sorted list of registered KDF Algorithms.
func AvailableDerivers() []Algorithm {
	//: delegate to the shared registry's sorted key list.
	return derivers.available()
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
