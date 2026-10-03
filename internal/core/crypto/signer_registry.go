// Package crypto — the process-wide Signer registry + GenerateKey / Sign / Verify dispatch.
package crypto

// signers maps each Algorithm to its Signer. Backed by the shared read-mostly
// schemeRegistry — register once at import, dispatch is lock-free.
var signers = schemeRegistry[Signer]{verb: "RegisterSigner"}

// RegisterSigner inserts s under s.Algorithm() and returns it so callers can
// bind the singleton to a typed package-level variable. Panics on a nil signer
// or when a distinct signer already claims the same Algorithm.
//
// IFACE-PLUGIN: the registry hands plug-in Signer instances back to callers so
// each scheme keeps its concrete type unexported; the contract is the Signer
// interface itself.
//
// "Nil" here means UNUSABLE, not only an untyped nil: a typed nil pointer and a
// plug-in whose type is not comparable both satisfy the port and neither can
// serve one call (see internal/kernel/plugin).
func RegisterSigner(s Signer) Signer {
	//: refuse an unusable signer, then publish it under its Algorithm; both
	//: refusals panic at boot with the dotted-quad code.
	return signers.register(s)
}

// LookupSigner returns the Signer registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in Signer instances behind the Signer
// interface — concrete types are intentionally unexported per scheme.
func LookupSigner(name Algorithm) (s Signer, ok bool) {
	//: a lock-free snapshot read; a miss hands back nil AND false.
	return signers.table.Lookup(name)
}

// AvailableSigners returns the sorted list of registered signature Algorithms.
func AvailableSigners() []Algorithm {
	//: sorted ascending, the caller's own slice; nil before any registration.
	return signers.table.Names()
}

// GenerateKey draws a fresh keypair for the Signer registered as name. A name
// with no registered signer returns UnknownSignatureAlgorithm (blank-import the
// scheme's package to register it).
func GenerateKey(name Algorithm) (pub, priv []byte, err error) {
	//: resolve the signer first so a missing import surfaces a clear sentinel.
	signer, ok := LookupSigner(name)
	//: absence path — the signer package was never blank-imported.
	if !ok {
		//: surface the documented sentinel naming the missing algorithm.
		return nil, nil, UnknownSignatureAlgorithm
	}
	//: delegate keypair generation to the scheme (it owns the entropy source).
	return signer.GenerateKey()
}

// Sign produces a detached signature over message using priv under the Signer
// registered as name. A name with no registered signer returns
// UnknownSignatureAlgorithm; a malformed priv returns SigningFailed.
func Sign(name Algorithm, priv, message []byte) (sig []byte, err error) {
	//: resolve the signer; a missing import surfaces the typed sentinel.
	signer, ok := LookupSigner(name)
	//: absence path — the signer package was never blank-imported.
	if !ok {
		//: surface the documented sentinel naming the missing algorithm.
		return nil, UnknownSignatureAlgorithm
	}
	//: delegate signing; the scheme guards its own key length.
	return signer.Sign(priv, message)
}

// Verify reports whether sig is a valid signature for message under pub for the
// Signer registered as name. An unregistered name returns
// (false, UnknownSignatureAlgorithm); an invalid signature is (false, nil) so
// Verify never becomes an oracle on WHY a check failed.
func Verify(name Algorithm, pub, message, sig []byte) (ok bool, err error) {
	//: resolve the signer; a missing import is a configuration error, not a
	//: verification outcome, so it surfaces a sentinel rather than a bare false.
	signer, found := LookupSigner(name)
	//: absence path — the signer package was never blank-imported.
	if !found {
		//: distinguish "scheme missing" from "bad signature" without an oracle.
		return false, UnknownSignatureAlgorithm
	}
	//: a registered scheme reports validity as a plain bool, no error channel.
	return signer.Verify(pub, message, sig), nil
}
