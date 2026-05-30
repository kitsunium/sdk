// Package crypto — the process-wide Signer registry + Sign / Verify dispatch.
package crypto

import (
	"fmt"
	"maps"
	"slices"

	"github.com/kitsunium/sdk/internal/kernel/snapshot"
)

// signers maps each Algorithm to its Signer. A third registry beside AEAD and
// Hasher: signing is a distinct, keypair-based capability. Same read-mostly
// snapshot.Value shape — register once at import, dispatch is lock-free.
var signers snapshot.Value[map[Algorithm]Signer]

// RegisterSigner inserts s under s.Algorithm() and returns it so callers can
// bind the singleton to a typed package-level variable like
// `var Signer = crypto.RegisterSigner(ed25519Signer{})`. Panics on a nil signer
// or when a distinct signer already claims the same Algorithm.
//
// IFACE-PLUGIN: the registry hands plug-in Signer instances back to callers so
// each scheme keeps its concrete type unexported; the stable contract is the
// Signer interface itself.
func RegisterSigner(s Signer) Signer {
	//: nil registration is always a programming error.
	if s == nil {
		//: panic so the offender is visible at boot.
		panic(fmt.Sprintf("crypto.RegisterSigner [%s DUPLICATE_REGISTRATION]: nil Signer", CodeDuplicateRegistration))
	}
	//: publish under the signer lock; a distinct duplicate Name is a hard conflict.
	if err := publishSigner(s.Algorithm(), s); err != nil {
		//: surface the doc code for grep-friendly panic messages.
		panic(err.Error())
	}
	//: returning the signer lets callers bind it to a typed singleton var.
	return s
}

// publishSigner inserts (name -> s) into the signer snapshot under the writer
// lock. Returns a non-nil error when name is already registered to a different
// signer; re-registering the same signer is an idempotent no-op.
func publishSigner(name Algorithm, s Signer) error {
	//: dupErr escapes the Update closure to signal a conflicting registration.
	var dupErr error
	//: Update serialises writers on the snapshot mutex, so the duplicate check
	//: and the publish are atomic against any concurrent RegisterSigner.
	signers.Update(func(current *map[Algorithm]Signer) *map[Algorithm]Signer {
		//: duplicate detection runs on the current snapshot before any alloc.
		if current != nil {
			//: an existing entry under name decides idempotent vs conflict.
			if existing, dup := (*current)[name]; dup {
				//: re-registering the SAME signer is a no-op republish.
				if existing == s {
					//: nothing changes; keep the current snapshot.
					return current
				}
				//: a DISTINCT signer under a taken name is the hard conflict.
				dupErr = fmt.Errorf("crypto.RegisterSigner [%s %w]: duplicate Algorithm %q", CodeDuplicateRegistration, errDuplicateRegistration, name)
				//: no-op publish — republish the current snapshot unchanged.
				return current
			}
		}
		//: clone the snapshot + insert the new entry, then publish atomically.
		return new(cloneSignerMap(current, name, s))
	})
	//: surface any conflict to RegisterSigner, which panics with the doc code.
	return dupErr
}

// cloneSignerMap copies src and inserts (name -> s). RegisterSigner runs once
// per scheme at package import, so this clone is init-time, one-shot work.
func cloneSignerMap(src *map[Algorithm]Signer, name Algorithm, s Signer) map[Algorithm]Signer {
	//: size hint = source size + 1 for the new entry; nil source -> 1.
	var size int
	//: nil source is the very-first-Register case; size stays zero.
	if src != nil {
		//: source has entries; pre-size for them plus one.
		size = len(*src)
	}
	//: allocate the new snapshot with the exact required capacity.
	next := make(map[Algorithm]Signer, size+1)
	//: bulk-copy every existing entry (no-op on a nil source).
	if src != nil {
		maps.Copy(next, *src)
	}
	//: insert the new entry.
	next[name] = s
	//: caller publishes the snapshot via Value.Update.
	return next
}

// LookupSigner returns the Signer registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in Signer instances behind the Signer
// interface — concrete types are intentionally unexported per scheme.
func LookupSigner(name Algorithm) (s Signer, ok bool) {
	//: load the current snapshot pointer; nil before first RegisterSigner call.
	current := signers.Load()
	//: absence path — no signer registered yet.
	if current == nil {
		//: clean miss.
		return nil, false
	}
	//: typed map read.
	signer, found := (*current)[name]
	//: hand back the typed signer + lookup outcome.
	return signer, found
}

// AvailableSigners returns the sorted list of registered signature Algorithms.
func AvailableSigners() []Algorithm {
	//: snapshot the registry pointer; nil before any RegisterSigner.
	current := signers.Load()
	//: empty result when nothing registered yet.
	if current == nil {
		//: nil slice is the documented zero value.
		return nil
	}
	//: collect the keys, then sort for a stable, reflection-free order.
	names := slices.Collect(maps.Keys(*current))
	//: ascending order is deterministic for callers.
	slices.Sort(names)
	//: hand back the freshly ordered slice.
	return names
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
