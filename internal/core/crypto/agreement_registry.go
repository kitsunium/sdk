// Package crypto — the Agreement registry + GenerateAgreementKey / AgreementShared dispatch.
package crypto

import (
	"fmt"
	"maps"
	"slices"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/snapshot"
)

// agreements maps each Algorithm to its Agreement scheme. A registry beside the
// others: key agreement establishes the shared secret a Deriver later consumes.
// Same read-mostly snapshot.Value shape — register once at import, dispatch is
// lock-free.
var agreements snapshot.Value[map[Algorithm]Agreement]

// RegisterAgreement inserts a under a.Algorithm() and returns it so callers can
// bind the singleton to a typed package-level variable like
// `var Agreement = crypto.RegisterAgreement(x25519{})`. Panics on a nil scheme
// or when a distinct scheme already claims the same Algorithm.
//
// IFACE-PLUGIN: the registry hands plug-in Agreement instances back to callers so
// each scheme keeps its concrete type unexported; the stable contract is the
// Agreement interface itself.
func RegisterAgreement(a Agreement) Agreement {
	//: nil registration is always a programming error.
	if a == nil {
		//: panic so the offender is visible at boot.
		panic(fmt.Sprintf("crypto.RegisterAgreement [%s DUPLICATE_REGISTRATION]: nil Agreement", CodeDuplicateRegistration))
	}
	//: publish under the agreement lock; a distinct duplicate Name is a conflict.
	if err := publishAgreement(a.Algorithm(), a); err != nil {
		//: surface the doc code for grep-friendly panic messages.
		panic(err.Error())
	}
	//: returning the scheme lets callers bind it to a typed singleton var.
	return a
}

// publishAgreement inserts (name -> a) into the agreement snapshot under the
// writer lock. Returns a non-nil error when name is already registered to a
// different scheme; re-registering the same scheme is an idempotent no-op.
func publishAgreement(name Algorithm, a Agreement) error {
	//: dupErr escapes the Update closure to signal a conflicting registration.
	var dupErr error
	//: Update serialises writers on the snapshot mutex, so the duplicate check
	//: and the publish are atomic against any concurrent RegisterAgreement.
	agreements.Update(func(current *map[Algorithm]Agreement) *map[Algorithm]Agreement {
		//: duplicate detection runs on the current snapshot before any alloc.
		if current != nil {
			//: an existing entry under name decides idempotent vs conflict.
			if existing, dup := (*current)[name]; dup {
				//: re-registering the SAME scheme is a no-op republish.
				if existing == a {
					//: nothing changes; keep the current snapshot.
					return current
				}
				//: a DISTINCT scheme under a taken name is the hard conflict.
				dupErr = fmt.Errorf("crypto.RegisterAgreement [%s %w]: duplicate Algorithm %q", CodeDuplicateRegistration, errDuplicateRegistration, name)
				//: no-op publish — republish the current snapshot unchanged.
				return current
			}
		}
		//: clone the snapshot + insert the new entry, then publish atomically.
		return new(cloneAgreementMap(current, name, a))
	})
	//: surface any conflict to RegisterAgreement, which panics with the doc code.
	return dupErr
}

// cloneAgreementMap copies src and inserts (name -> a). RegisterAgreement runs
// once per scheme at package import, so this clone is init-time, one-shot work.
func cloneAgreementMap(src *map[Algorithm]Agreement, name Algorithm, a Agreement) map[Algorithm]Agreement {
	//: size hint = source size + 1 for the new entry; nil source -> 1.
	var size int
	//: nil source is the very-first-Register case; size stays zero.
	if src != nil {
		//: source has entries; pre-size for them plus one.
		size = len(*src)
	}
	//: allocate the new snapshot with the exact required capacity.
	next := make(map[Algorithm]Agreement, size+1)
	//: bulk-copy every existing entry (no-op on a nil source).
	if src != nil {
		maps.Copy(next, *src)
	}
	//: insert the new entry.
	next[name] = a
	//: caller publishes the snapshot via Value.Update.
	return next
}

// LookupAgreement returns the Agreement scheme registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in Agreement instances behind the
// Agreement interface — concrete types are intentionally unexported per scheme.
func LookupAgreement(name Algorithm) (a Agreement, ok bool) {
	//: load the current snapshot pointer; nil before first RegisterAgreement.
	current := agreements.Load()
	//: absence path — no scheme registered yet.
	if current == nil {
		//: clean miss.
		return nil, false
	}
	//: typed map read.
	agreement, found := (*current)[name]
	//: hand back the typed scheme + lookup outcome.
	return agreement, found
}

// AvailableAgreements returns the sorted list of registered agreement Algorithms.
func AvailableAgreements() []Algorithm {
	//: snapshot the registry pointer; nil before any RegisterAgreement.
	current := agreements.Load()
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

// GenerateAgreementKey draws a fresh keypair from the Agreement scheme
// registered as name. A name with no registered scheme returns
// UnknownAgreementAlgorithm; an entropy fault from the scheme propagates
// unchanged so callers see the original cause.
func GenerateAgreementKey(name Algorithm) (pub, priv []byte, err error) {
	//: resolve the scheme; a missing import surfaces the typed sentinel.
	agreement, ok := LookupAgreement(name)
	//: absence path — the scheme package was never blank-imported.
	if !ok {
		//: surface the documented sentinel naming the missing algorithm.
		return nil, nil, UnknownAgreementAlgorithm
	}
	//: delegate keypair generation; the scheme owns its entropy source.
	return agreement.GenerateKey()
}

// AgreementShared derives the raw shared secret with the Agreement scheme
// registered as name. The result MUST be passed through a KDF before use as a
// key. A name with no registered scheme returns UnknownAgreementAlgorithm; a
// scheme Shared fault is wrapped into AgreementFailed, leaking no key bytes.
func AgreementShared(name Algorithm, priv, peerPub []byte) (secret []byte, err error) {
	//: resolve the scheme; a missing import surfaces the typed sentinel.
	agreement, ok := LookupAgreement(name)
	//: absence path — the scheme package was never blank-imported.
	if !ok {
		//: surface the documented sentinel naming the missing algorithm.
		return nil, UnknownAgreementAlgorithm
	}
	//: delegate the derivation to the scheme (it owns the curve arithmetic).
	out, derr := agreement.Shared(priv, peerPub)
	//: a scheme fault (e.g. a low-order peer point) wraps into a typed sentinel.
	if derr != nil {
		//: wrap rather than relabel so the cause is preserved without key bytes.
		return nil, errs.Wrap(derr, errs.WrapParams{
			Code:    CodeAgreementFailed,
			Reason:  "AGREEMENT_FAILED",
			Public:  "Key agreement failed to derive a shared secret",
			Private: "core/crypto.AgreementShared: the scheme rejected the inputs (cause withheld of key bytes)",
		})
	}
	//: hand back the raw secret for the caller to KDF.
	return out, nil
}
