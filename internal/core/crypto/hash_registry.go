// Package crypto — the process-wide Hasher registry + Sum / SumHex dispatch.
package crypto

import (
	"encoding/hex"
	"fmt"
	"hash"
)

// hashers maps each Algorithm to its Hasher. Separate from the AEAD registry:
// fingerprint hashing is a distinct, non-authenticated capability. Backed by the
// shared read-mostly schemeRegistry — register once at import, Sum is lock-free.
var hashers = schemeRegistry[Hasher]{verb: "RegisterHasher"}

// RegisterHasher inserts h under h.Algorithm() and returns it so callers can
// bind the singleton to a typed package-level variable like
// `var Hasher = crypto.RegisterHasher(sha256Hasher{})`. Panics on a nil hasher
// or when a distinct hasher already claims the same Algorithm.
//
// IFACE-PLUGIN: the registry hands plug-in Hasher instances back to callers so
// each scheme keeps its concrete type unexported; the stable contract is the
// Hasher interface itself.
func RegisterHasher(h Hasher) Hasher {
	//: nil registration is always a programming error.
	if h == nil {
		//: panic so the offender is visible at boot.
		panic(fmt.Sprintf("crypto.RegisterHasher [%s DUPLICATE_REGISTRATION]: nil Hasher", CodeDuplicateRegistration))
	}
	//: publish via the shared registry; a distinct duplicate Name is a hard conflict.
	if err := hashers.publish(h.Algorithm(), h); err != nil {
		//: surface the doc code for grep-friendly panic messages.
		panic(err.Error())
	}
	//: returning the hasher lets callers bind it to a typed singleton var.
	return h
}

// LookupHasher returns the Hasher registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in Hasher instances behind the Hasher
// interface — concrete types are intentionally unexported per scheme.
func LookupHasher(name Algorithm) (h Hasher, ok bool) {
	//: delegate to the shared registry's typed lookup.
	return hashers.lookup(name)
}

// AvailableHashers returns the sorted list of registered hash Algorithms.
func AvailableHashers() []Algorithm {
	//: delegate to the shared registry's sorted key list.
	return hashers.available()
}

// Sum returns the digest of data under the Hasher registered as name. A name
// with no registered hasher returns UnknownHashAlgorithm (blank-import the
// scheme's package to register it).
func Sum(name Algorithm, data []byte) (digest []byte, err error) {
	//: resolve the hasher first so a missing import surfaces a clear sentinel.
	hasher, ok := LookupHasher(name)
	//: absence path — the hasher package was never blank-imported.
	if !ok {
		//: surface the documented sentinel naming the missing algorithm.
		return nil, UnknownHashAlgorithm
	}
	//: one-shot: a fresh hash, write the data, read the digest.
	hsh := hasher.New()
	//: hash.Hash.Write is contractually error-free, but capture + propagate so a
	//: non-conforming custom Hasher can never silently drop a fault.
	if _, werr := hsh.Write(data); werr != nil {
		//: unreachable for every stdlib hasher; surfaced, never swallowed.
		return nil, werr
	}
	//: Sum(nil) appends the digest to a fresh slice.
	return hsh.Sum(nil), nil
}

// NewHash returns a fresh streaming hash.Hash for the Hasher registered as
// name (for io.Copy over large inputs). A name with no registered hasher
// returns UnknownHashAlgorithm.
func NewHash(name Algorithm) (h hash.Hash, err error) {
	//: resolve the hasher; a missing import surfaces the typed sentinel.
	hasher, ok := LookupHasher(name)
	//: absence path — the hasher package was never blank-imported.
	if !ok {
		//: surface the documented sentinel naming the missing algorithm.
		return nil, UnknownHashAlgorithm
	}
	//: hand back a fresh streaming hash for the caller to Write/Sum.
	return hasher.New(), nil
}

// SumHex is Sum rendered as canonical lowercase hex — the frozen string form
// for content IDs and cache keys.
func SumHex(name Algorithm, data []byte) (digest string, err error) {
	//: delegate to Sum, then hex-encode on success.
	raw, sErr := Sum(name, data)
	//: propagate an unknown-algorithm error unchanged.
	if sErr != nil {
		//: no digest to encode.
		return "", sErr
	}
	//: lowercase hex is the canonical, frozen rendering.
	return hex.EncodeToString(raw), nil
}
