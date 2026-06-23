// Package id — holds the process-wide Generator registry. Service-level scheme
// packages register themselves via package-level var initialisers when imported
// (no init()), mirroring core/codec, core/crypto, and core/transform.
package id

import (
	"maps"
	"slices"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/snapshot"
)

// registry maps each Scheme to its Generator. snapshot.Value over sync.Map:
// schemes register once at import, every other access is a lock-free Load
// (ADR 0011). Update serialises writers; Lookup stays lock-free.
var registry snapshot.Value[map[Scheme]Generator]

// Register inserts g under g.Scheme() and returns it so callers can bind the
// singleton to a typed package-level variable like
// `var UUIDv4 = id.Register(uuidv4Gen{})`. Panics on a nil generator or when a
// distinct generator already claims the same Scheme.
//
// IFACE-PLUGIN: the registry hands plug-in Generator instances back to callers
// so each scheme keeps its concrete type unexported; the contract is the
// Generator interface itself.
func Register(g Generator) Generator {
	//: nil registration is always a programming error.
	if g == nil {
		//: panic so the offender is visible at boot with the dotted-quad code.
		panic(DuplicateRegistration.Error())
	}
	//: publish under the scheme name; a conflict turns into a boot-time panic.
	if err := publishGenerator(g.Scheme(), g); err != nil {
		//: surface the doc code for grep-friendly panic messages.
		panic(err.Error())
	}
	//: returning the generator lets callers bind it to a typed singleton var.
	return g
}

// publishGenerator inserts (name -> g) under the writer lock. Returns a non-nil
// error when name is already registered to a different generator; re-registering
// the same generator is an idempotent no-op.
func publishGenerator(name Scheme, g Generator) error {
	//: dupErr escapes the Update closure to signal a conflicting registration.
	var dupErr error
	//: Update serialises writers so the check + publish are atomic.
	registry.Update(func(current *map[Scheme]Generator) *map[Scheme]Generator {
		//: duplicate detection runs on the current snapshot before any alloc.
		if current != nil {
			//: an existing entry under name decides idempotent vs conflict.
			if existing, dup := (*current)[name]; dup {
				//: re-registering the SAME generator is a no-op republish.
				if existing == g {
					//: nothing changes; keep the current snapshot.
					return current
				}
				//: a DISTINCT generator under a taken name is the hard conflict;
				//: origin-wins keeps DUPLICATE_REGISTRATION; the scheme rides as a field.
				dupErr = errs.Wrap(DuplicateRegistration, errs.WrapParams{}, errs.String("scheme", string(name)))
				//: no-op publish — republish the current snapshot unchanged.
				return current
			}
		}
		//: clone the snapshot + insert the new entry, then publish atomically.
		return new(cloneGeneratorMap(current, name, g))
	})
	//: surface any conflict to Register, which panics with the doc code.
	return dupErr
}

// cloneGeneratorMap copies src and inserts (name -> g). Register runs once per
// scheme at package import, so this clone is init-time, one-shot work.
func cloneGeneratorMap(src *map[Scheme]Generator, name Scheme, g Generator) map[Scheme]Generator {
	//: size hint = source size + 1 for the new entry; nil source -> 1.
	var size int
	//: nil source is the very-first-Register case; size stays zero.
	if src != nil {
		//: source has entries; pre-size for them plus one.
		size = len(*src)
	}
	//: allocate the new snapshot with the exact required capacity.
	next := make(map[Scheme]Generator, size+1)
	//: bulk-copy every existing entry (no-op on a nil source).
	if src != nil {
		//: copy the existing snapshot forward.
		maps.Copy(next, *src)
	}
	//: insert the new entry.
	next[name] = g
	//: caller publishes the snapshot via Value.Update.
	return next
}

// Lookup returns the Generator registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in scheme instances behind the
// Generator interface — concrete types are intentionally unexported per scheme.
func Lookup(name Scheme) (g Generator, ok bool) {
	//: load the current snapshot pointer; nil before first Register call.
	current := registry.Load()
	//: absence path — no scheme registered yet.
	if current == nil {
		//: clean miss.
		return nil, false
	}
	//: typed map read.
	scheme, found := (*current)[name]
	//: hand back the typed scheme + lookup outcome.
	return scheme, found
}

// Available returns the sorted list of registered Schemes.
func Available() []Scheme {
	//: snapshot the registry pointer; nil before any Register.
	current := registry.Load()
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
