// Package plugin — Registry: the read-mostly, name-keyed table a process-wide
// registry stores its plug-ins in, once Unusable has said they can be stored.
package plugin

import (
	"cmp"
	"maps"
	"slices"

	"github.com/kitsunium/sdk/internal/kernel/snapshot"
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
// two paths without turning a diamond dependency into a boot panic.
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
	// Publish.
	table snapshot.Value[map[K]V]
}

// Publish inserts (name -> value) and reports whether name was already held by
// a DIFFERENT value. On a conflict nothing changes: the first registration
// stays, and the caller refuses the second with its own code. Re-publishing the
// identical value changes nothing either, and is not a conflict.
//
// The duplicate check and the publish are one atomic step.
func (r *Registry[K, V]) Publish(name K, value V) bool {
	//: set inside the writer's critical section, read after it.
	var conflict bool
	//: Update serialises writers, so the check below and the publish after it
	//: cannot interleave with another writer's.
	r.table.Update(func(current *map[K]V) *map[K]V {
		//: an existing entry decides between idempotent and conflicting.
		if current != nil {
			//: the name is taken: compare before allocating anything.
			if existing, taken := (*current)[name]; taken {
				//: a DIFFERENT value under a taken name is the conflict.
				conflict = existing != value
				//: either way the snapshot stays as it is.
				return current
			}
		}
		//: copy forward and insert, then publish the copy atomically.
		return new(cloneWith(current, name, value))
	})
	//: the caller refuses a conflict with its own code.
	return conflict
}

// Lookup returns the value registered under name, and whether there is one. A
// miss returns V's zero value, so a caller checking only the value of an
// interface-typed V would hold a nil — which is why found exists.
func (r *Registry[K, V]) Lookup(name K) (value V, found bool) {
	//: the current snapshot; nil before the first Publish.
	current := r.table.Load()
	//: nothing has been published yet.
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
// first Publish. The slice is the caller's; it shares nothing with the table.
func (r *Registry[K, V]) Names() []K {
	//: the current snapshot; nil before the first Publish.
	current := r.table.Load()
	//: the documented nil for an empty registry.
	if current == nil {
		//: nothing registered.
		return nil
	}
	//: collect, then sort — map order is not an order.
	names := slices.Collect(maps.Keys(*current))
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
