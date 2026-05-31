// Package crypto — the process-wide MAC registry + MACTag / MACVerify dispatch.
package crypto

import (
	"fmt"
	"maps"
	"slices"

	"github.com/kitsunium/sdk/internal/kernel/snapshot"
)

// macs maps each Algorithm to its MAC. A registry beside AEAD/Hasher/Signer:
// keyed detached authentication is a distinct capability. Same read-mostly
// snapshot.Value shape — register once at import, dispatch is lock-free.
var macs snapshot.Value[map[Algorithm]MAC]

// RegisterMAC inserts m under m.Algorithm() and returns it so callers can bind
// the singleton to a typed package-level variable like
// `var MAC = crypto.RegisterMAC(hmacSHA256{})`. Panics on a nil MAC or when a
// distinct MAC already claims the same Algorithm.
//
// IFACE-PLUGIN: the registry hands plug-in MAC instances back to callers so each
// scheme keeps its concrete type unexported; the stable contract is the MAC
// interface itself.
func RegisterMAC(m MAC) MAC {
	//: nil registration is always a programming error.
	if m == nil {
		//: panic so the offender is visible at boot.
		panic(fmt.Sprintf("crypto.RegisterMAC [%s DUPLICATE_REGISTRATION]: nil MAC", CodeDuplicateRegistration))
	}
	//: publish under the MAC lock; a distinct duplicate Name is a hard conflict.
	if err := publishMAC(m.Algorithm(), m); err != nil {
		//: surface the doc code for grep-friendly panic messages.
		panic(err.Error())
	}
	//: returning the MAC lets callers bind it to a typed singleton var.
	return m
}

// publishMAC inserts (name -> m) into the MAC snapshot under the writer lock.
// Returns a non-nil error when name is already registered to a different MAC;
// re-registering the same MAC is an idempotent no-op.
func publishMAC(name Algorithm, m MAC) error {
	//: dupErr escapes the Update closure to signal a conflicting registration.
	var dupErr error
	//: Update serialises writers on the snapshot mutex, so the duplicate check
	//: and the publish are atomic against any concurrent RegisterMAC.
	macs.Update(func(current *map[Algorithm]MAC) *map[Algorithm]MAC {
		//: duplicate detection runs on the current snapshot before any alloc.
		if current != nil {
			//: an existing entry under name decides idempotent vs conflict.
			if existing, dup := (*current)[name]; dup {
				//: re-registering the SAME MAC is a no-op republish.
				if existing == m {
					//: nothing changes; keep the current snapshot.
					return current
				}
				//: a DISTINCT MAC under a taken name is the hard conflict.
				dupErr = fmt.Errorf("crypto.RegisterMAC [%s %w]: duplicate Algorithm %q", CodeDuplicateRegistration, errDuplicateRegistration, name)
				//: no-op publish — republish the current snapshot unchanged.
				return current
			}
		}
		//: clone the snapshot + insert the new entry, then publish atomically.
		return new(cloneMACMap(current, name, m))
	})
	//: surface any conflict to RegisterMAC, which panics with the doc code.
	return dupErr
}

// cloneMACMap copies src and inserts (name -> m). RegisterMAC runs once per
// scheme at package import, so this clone is init-time, one-shot work.
func cloneMACMap(src *map[Algorithm]MAC, name Algorithm, m MAC) map[Algorithm]MAC {
	//: size hint = source size + 1 for the new entry; nil source -> 1.
	var size int
	//: nil source is the very-first-Register case; size stays zero.
	if src != nil {
		//: source has entries; pre-size for them plus one.
		size = len(*src)
	}
	//: allocate the new snapshot with the exact required capacity.
	next := make(map[Algorithm]MAC, size+1)
	//: bulk-copy every existing entry (no-op on a nil source).
	if src != nil {
		maps.Copy(next, *src)
	}
	//: insert the new entry.
	next[name] = m
	//: caller publishes the snapshot via Value.Update.
	return next
}

// LookupMAC returns the MAC registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in MAC instances behind the MAC
// interface — concrete types are intentionally unexported per scheme.
func LookupMAC(name Algorithm) (m MAC, ok bool) {
	//: load the current snapshot pointer; nil before first RegisterMAC call.
	current := macs.Load()
	//: absence path — no MAC registered yet.
	if current == nil {
		//: clean miss.
		return nil, false
	}
	//: typed map read.
	mac, found := (*current)[name]
	//: hand back the typed MAC + lookup outcome.
	return mac, found
}

// AvailableMACs returns the sorted list of registered MAC Algorithms.
func AvailableMACs() []Algorithm {
	//: snapshot the registry pointer; nil before any RegisterMAC.
	current := macs.Load()
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

// MACTag returns the authentication tag over message under key for the MAC
// registered as name. It is named MACTag (not Tag) so the dispatch verb never
// collides with the signer/verifier surface. A name with no registered MAC
// returns UnknownMACAlgorithm (blank-import the scheme's package to register it).
func MACTag(name Algorithm, key Key, message []byte) (tag []byte, err error) {
	//: resolve the MAC; a missing import surfaces the typed sentinel.
	mac, ok := LookupMAC(name)
	//: absence path — the MAC package was never blank-imported.
	if !ok {
		//: surface the documented sentinel naming the missing algorithm.
		return nil, UnknownMACAlgorithm
	}
	//: delegate tagging; the redacting Key pins the length so Tag cannot fail.
	return mac.Tag(key, message), nil
}

// MACVerify reports whether tag authenticates message under key for the MAC
// registered as name, using the scheme's constant-time comparison. A name with
// no registered MAC returns (false, UnknownMACAlgorithm) so a miss is never
// mistaken for a bad-tag false.
func MACVerify(name Algorithm, key Key, message, tag []byte) (ok bool, err error) {
	//: resolve the MAC; a missing import is a configuration error, not a verdict.
	mac, found := LookupMAC(name)
	//: absence path — distinguish "scheme missing" from "bad tag".
	if !found {
		//: a miss returns false so callers never trust an unverified tag.
		return false, UnknownMACAlgorithm
	}
	//: a registered scheme reports validity as a plain bool, no error channel.
	return mac.Verify(key, message, tag), nil
}
