// Package crypto — the process-wide Deriver registry + Subkey dispatch.
package crypto

import (
	"fmt"
	"maps"
	"slices"

	"github.com/kitsunium/sdk/internal/kernel/snapshot"
)

// derivers maps each Algorithm to its Deriver. A fourth registry beside AEAD,
// Hasher, and Signer: key derivation is a distinct capability. Same read-mostly
// snapshot.Value shape — register once at import, dispatch is lock-free.
var derivers snapshot.Value[map[Algorithm]Deriver]

// RegisterDeriver inserts d under d.Algorithm() and returns it so callers can
// bind the singleton to a typed package-level variable like
// `var Deriver = crypto.RegisterDeriver(hkdfSHA256{})`. Panics on a nil deriver
// or when a distinct deriver already claims the same Algorithm.
//
// IFACE-PLUGIN: the registry hands plug-in Deriver instances back to callers so
// each scheme keeps its concrete type unexported; the stable contract is the
// Deriver interface itself.
func RegisterDeriver(d Deriver) Deriver {
	//: nil registration is always a programming error.
	if d == nil {
		//: panic so the offender is visible at boot.
		panic(fmt.Sprintf("crypto.RegisterDeriver [%s DUPLICATE_REGISTRATION]: nil Deriver", CodeDuplicateRegistration))
	}
	//: publish under the deriver lock; a distinct duplicate Name is a hard conflict.
	if err := publishDeriver(d.Algorithm(), d); err != nil {
		//: surface the doc code for grep-friendly panic messages.
		panic(err.Error())
	}
	//: returning the deriver lets callers bind it to a typed singleton var.
	return d
}

// publishDeriver inserts (name -> d) into the deriver snapshot under the writer
// lock. Returns a non-nil error when name is already registered to a different
// deriver; re-registering the same deriver is an idempotent no-op.
func publishDeriver(name Algorithm, d Deriver) error {
	//: dupErr escapes the Update closure to signal a conflicting registration.
	var dupErr error
	//: Update serialises writers on the snapshot mutex, so the duplicate check
	//: and the publish are atomic against any concurrent RegisterDeriver.
	derivers.Update(func(current *map[Algorithm]Deriver) *map[Algorithm]Deriver {
		//: duplicate detection runs on the current snapshot before any alloc.
		if current != nil {
			//: an existing entry under name decides idempotent vs conflict.
			if existing, dup := (*current)[name]; dup {
				//: re-registering the SAME deriver is a no-op republish.
				if existing == d {
					//: nothing changes; keep the current snapshot.
					return current
				}
				//: a DISTINCT deriver under a taken name is the hard conflict.
				dupErr = fmt.Errorf("crypto.RegisterDeriver [%s %w]: duplicate Algorithm %q", CodeDuplicateRegistration, errDuplicateRegistration, name)
				//: no-op publish — republish the current snapshot unchanged.
				return current
			}
		}
		//: clone the snapshot + insert the new entry, then publish atomically.
		return new(cloneDeriverMap(current, name, d))
	})
	//: surface any conflict to RegisterDeriver, which panics with the doc code.
	return dupErr
}

// cloneDeriverMap copies src and inserts (name -> d). RegisterDeriver runs once
// per scheme at package import, so this clone is init-time, one-shot work.
func cloneDeriverMap(src *map[Algorithm]Deriver, name Algorithm, d Deriver) map[Algorithm]Deriver {
	//: size hint = source size + 1 for the new entry; nil source -> 1.
	var size int
	//: nil source is the very-first-Register case; size stays zero.
	if src != nil {
		//: source has entries; pre-size for them plus one.
		size = len(*src)
	}
	//: allocate the new snapshot with the exact required capacity.
	next := make(map[Algorithm]Deriver, size+1)
	//: bulk-copy every existing entry (no-op on a nil source).
	if src != nil {
		maps.Copy(next, *src)
	}
	//: insert the new entry.
	next[name] = d
	//: caller publishes the snapshot via Value.Update.
	return next
}

// LookupDeriver returns the Deriver registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in Deriver instances behind the Deriver
// interface — concrete types are intentionally unexported per scheme.
func LookupDeriver(name Algorithm) (d Deriver, ok bool) {
	//: load the current snapshot pointer; nil before first RegisterDeriver call.
	current := derivers.Load()
	//: absence path — no deriver registered yet.
	if current == nil {
		//: clean miss.
		return nil, false
	}
	//: typed map read.
	deriver, found := (*current)[name]
	//: hand back the typed deriver + lookup outcome.
	return deriver, found
}

// AvailableDerivers returns the sorted list of registered KDF Algorithms.
func AvailableDerivers() []Algorithm {
	//: snapshot the registry pointer; nil before any RegisterDeriver.
	current := derivers.Load()
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
