// Package crypto — the generic read-mostly capability registry shared by every
// scheme registry (Hasher, Signer, MAC, Deriver, Agreement, PasswordHasher,
// StreamSealer). Each capability is a distinct snapshot-backed map; this type
// collapses the previously-duplicated publish / clone / lookup / available
// logic into one generic, leaving each registry file as thin Register* /
// Lookup* / Available* delegations (the public contract is unchanged).
package crypto

import (
	"fmt"
	"maps"
	"slices"

	"github.com/kitsunium/sdk/internal/kernel/snapshot"
)

// schemeRegistry is the shared registry backing one crypto capability. V is the
// capability interface (e.g. Hasher); it must be comparable so idempotent
// re-registration of the SAME value is a no-op while a DISTINCT value under a
// taken Algorithm is a hard conflict. The zero value is ready to use.
type schemeRegistry[V comparable] struct {
	// store is the copy-on-write snapshot; nil before the first publish.
	store snapshot.Value[map[Algorithm]V]
	// verb names the public registrar in panic/error text, e.g. "RegisterHasher".
	verb string
}

// publish inserts (name -> v) under the writer lock. Returns a non-nil error
// when name is already held by a DISTINCT value; re-publishing the SAME value is
// an idempotent no-op. The duplicate check and the publish are atomic.
func (r *schemeRegistry[V]) publish(name Algorithm, v V) error {
	//: dupErr escapes the Update closure to signal a conflicting registration.
	var dupErr error
	//: Update serialises writers so the check + publish are atomic.
	r.store.Update(func(current *map[Algorithm]V) *map[Algorithm]V {
		//: duplicate detection runs on the current snapshot before any alloc.
		if current != nil {
			//: an existing entry under name decides idempotent vs conflict.
			if existing, dup := (*current)[name]; dup {
				//: re-registering the SAME value is a no-op republish.
				if existing == v {
					//: nothing changes; keep the current snapshot.
					return current
				}
				//: a DISTINCT value under a taken name is the hard conflict.
				dupErr = fmt.Errorf("crypto.%s [%s %w]: duplicate Algorithm %q", r.verb, CodeDuplicateRegistration, errDuplicateRegistration, name)
				//: no-op publish — republish the current snapshot unchanged.
				return current
			}
		}
		//: clone the snapshot + insert the new entry, then publish atomically.
		return new(cloneSchemeMap(current, name, v))
	})
	//: surface any conflict to the caller, which panics with the doc code.
	return dupErr
}

// lookup returns the value registered under name (and whether it was found).
func (r *schemeRegistry[V]) lookup(name Algorithm) (value V, ok bool) {
	//: load the current snapshot pointer; nil before the first publish.
	current := r.store.Load()
	//: absence path — nothing registered yet.
	if current == nil {
		//: zero value + clean miss.
		var zero V
		//: report the miss.
		return zero, false
	}
	//: typed map read.
	found, hit := (*current)[name]
	//: hand back the value + lookup outcome.
	return found, hit
}

// available returns the sorted list of registered Algorithms.
func (r *schemeRegistry[V]) available() []Algorithm {
	//: snapshot the registry pointer; nil before the first publish.
	current := r.store.Load()
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

// cloneSchemeMap copies src and inserts (name -> v). A registry publish runs once
// per scheme at package import, so this clone is init-time, one-shot work.
func cloneSchemeMap[V comparable](src *map[Algorithm]V, name Algorithm, v V) map[Algorithm]V {
	//: size hint = source size + 1 for the new entry; nil source -> 1.
	var size int
	//: nil source is the very-first-publish case; size stays zero.
	if src != nil {
		//: source has entries; pre-size for them plus one.
		size = len(*src)
	}
	//: allocate the new snapshot with the exact required capacity.
	next := make(map[Algorithm]V, size+1)
	//: bulk-copy every existing entry (no-op on a nil source).
	if src != nil {
		//: copy the existing snapshot forward.
		maps.Copy(next, *src)
	}
	//: insert the new entry.
	next[name] = v
	//: caller publishes the snapshot via Value.Update.
	return next
}
