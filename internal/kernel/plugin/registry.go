// Package plugin — Registry: the read-mostly, name-keyed table a process-wide
// registry stores its plug-ins in, once Unusable has said they can be stored.
package plugin

import (
	"cmp"
	"maps"
	"slices"

	"github.com/kitsunium/sdk/internal/kernel/concur/snapshot"
)

// Registry is a process-wide table of plug-ins keyed by name: written at import
// time, read on every dispatch. A reader loads an immutable snapshot and takes
// no lock; a writer copies the table, inserts, and publishes the copy under the
// snapshot's writer lock, so a reader never observes a half-built map and two
// writers never lose each other's entry.
//
// It decides nothing a domain names. Publish REPORTS whether the name was
// already held by a different value — the conflict — and the registrar refuses
// it with its own dotted-quad code and reason, exactly as it does with
// Unusable's answer. Re-publishing the SAME value is an idempotent no-op that
// reports no conflict, which is what lets a plug-in package be imported through
// two paths without turning a diamond dependency into a boot panic. Claim
// reports the holder instead of a verdict, for a registrar that refuses every
// second registration or names the holder in its refusal.
//
// V must be comparable, and that is a runtime contract as much as a compile
// one: when V is an interface, == on a dynamic value that is not comparable
// panics inside Publish's duplicate check with Go's message rather than the
// registrar's. A registrar asks Unusable first, which refuses exactly that
// value, and a typed nil with it.
//
// The zero value is ready to use. K is ordered so Names can answer in a stable
// order rather than in map order.
type Registry[K cmp.Ordered, V comparable] struct {
	// table is the copy-on-write snapshot; it loads nil before the first
	// Publish or Claim inserts.
	table snapshot.Value[map[K]V]
}

// Publish inserts (name -> value) and reports whether name was already held by
// a DIFFERENT value. On a conflict nothing changes: the first registration
// stays, and the caller refuses the second with its own code. Re-publishing the
// identical value changes nothing either, and is not a conflict.
//
// The duplicate check and the publish are one atomic step: Publish is Claim
// with the verdict a registry that tolerates a diamond import wants.
func (r *Registry[K, V]) Publish(name K, value V) bool {
	//: the holder, if any, decides — after the atomic step, outside the lock.
	holder, taken := r.Claim(name, value)
	//: a DIFFERENT value under a taken name is the conflict; the identical one
	//: is the idempotent re-registration.
	return taken && holder != value
}

// Claim inserts (name -> value) when name is free. When name is taken, nothing
// changes and Claim reports the value holding it — value itself or another.
//
// Publish is Claim plus the verdict most registrars want. Claim serves the two
// that want another one: a registrar for which ANY second registration is a
// conflict, the identical value included, refuses on taken alone; and a
// registrar whose refusal names the holder reads it here, from the same atomic
// step that refused, rather than from a second Lookup.
//
// The check and the insert are one atomic step.
func (r *Registry[K, V]) Claim(name K, value V) (holder V, taken bool) {
	//: Update serialises writers, so the check below and the insert after it
	//: cannot interleave with another writer's.
	r.table.Update(func(current *map[K]V) *map[K]V {
		//: an existing entry is reported, never replaced.
		if current != nil {
			//: the name is taken: report what holds it, allocating nothing.
			if existing, found := (*current)[name]; found {
				//: read by the caller once Update returns.
				holder, taken = existing, true
				//: the snapshot stays as it is.
				return current
			}
		}
		//: copy forward and insert, then publish the copy atomically.
		return new(cloneWith(current, name, value))
	})
	//: the zero holder and false when the name was free and is now value's.
	return holder, taken
}

// Lookup returns the value registered under name, and whether there is one. A
// miss returns V's zero value, so a caller checking only the value of an
// interface-typed V would hold a nil — which is why found exists.
func (r *Registry[K, V]) Lookup(name K) (value V, found bool) {
	//: the current snapshot; nil before the first insert.
	current := r.table.Load()
	//: nothing has been inserted yet.
	if current == nil {
		//: a clean miss, with the zero value.
		return value, false
	}
	//: a plain map read on an immutable snapshot.
	value, found = (*current)[name]
	//: hand back the outcome.
	return value, found
}

// Names returns every registered name in ascending order, or nil before the
// first Publish or Claim. The slice is the caller's; it shares nothing with the
// table.
func (r *Registry[K, V]) Names() []K {
	//: the current snapshot; nil before the first insert.
	current := r.table.Load()
	//: the documented nil for an empty registry.
	if current == nil {
		//: nothing registered.
		return nil
	}
	//: collect into a slice sized once — the table's size is known, so the
	//: append never regrows — then sort: map order is not an order.
	names := slices.AppendSeq(make([]K, 0, len(*current)), maps.Keys(*current))
	slices.Sort(names)
	//: hand back the caller's own slice.
	return names
}

// cloneWith copies src and inserts (name -> value) into the copy. A publish runs
// once per plug-in at import time, so the copy is one-shot init work and never
// on a dispatch path.
func cloneWith[K cmp.Ordered, V comparable](src *map[K]V, name K, value V) map[K]V {
	//: size the copy for the existing entries plus the new one.
	var size int
	//: a nil source is the very first publish.
	if src != nil {
		//: the existing entries.
		size = len(*src)
	}
	next := make(map[K]V, size+1)
	//: copy the existing entries forward.
	if src != nil {
		//: every entry, unchanged.
		maps.Copy(next, *src)
	}
	//: the new entry.
	next[name] = value
	//: the caller publishes it.
	return next
}
