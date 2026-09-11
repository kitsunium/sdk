// Package crypto — holds the process-wide AEAD registry. Service- and
// third-party-level scheme packages register themselves via package-level var
// initialisers when imported (no init()), mirroring core/codec.
package crypto

import (
	"errors"
	"fmt"
	"maps"

	"github.com/kitsunium/sdk/internal/kernel/plugin"
	"github.com/kitsunium/sdk/internal/kernel/snapshot"
)

// aeads maps each Algorithm to its AEAD (the shared read-mostly schemeRegistry);
// idIndex maps the 1-byte wire id to the same AEAD so Open can dispatch from the
// box header alone. The id index stays bespoke — the generic registry is keyed
// by Algorithm, whereas Open resolves by the byte id in the frame.
var (
	aeads   = schemeRegistry[AEAD]{verb: "Register"}
	idIndex snapshot.Value[map[byte]AEAD]

	//: errDuplicateRegistration is the wrappable sentinel for boot-time
	//: duplicate-scheme panics, shared by every registry (the generic
	//: schemeRegistry + the AEAD wire-id index). Wrapping via %w keeps the
	//: chain inspectable while the message retains the dotted-quad code for
	//: grep-friendly logs. Lowercase per Go style guide (KTN-FUNC-ERRFMT).
	errDuplicateRegistration = errors.New("duplicate registration")
)

// Register inserts a into the registry under a.Algorithm() + a.ID() and returns
// it so callers can bind the singleton to a typed package-level variable like
// `var AEAD = crypto.Register(aesGCM{})`. Panics on a nil scheme or when a
// distinct scheme already claims the same Algorithm or wire id.
//
// IFACE-PLUGIN: the registry hands plug-in AEAD instances back to callers so
// each scheme keeps its concrete type unexported; the stable contract is the
// AEAD interface itself.
//
// "Nil" here means UNUSABLE, not only an untyped nil: a typed nil pointer and a
// plug-in whose type is not comparable both satisfy the port and neither can
// serve one call (see internal/kernel/plugin).
func Register(a AEAD) AEAD {
	//: a typed nil and a non-comparable plug-in both satisfy the port and
	//: neither can serve — refuse at import, where the offender is named.
	if why := plugin.Unusable(a); why != "" {
		//: panic so the offender is visible at boot.
		panic(fmt.Sprintf("crypto.Register [%s DUPLICATE_REGISTRATION]: %s", CodeDuplicateRegistration, why))
	}
	//: publish under the scheme name first via the shared registry; either
	//: conflict turns into a boot-time panic with the doc code.
	if err := aeads.publish(a.Algorithm(), a); err != nil {
		//: surface the doc code for grep-friendly panic messages.
		panic(err.Error())
	}
	//: index the wire id second so Open can dispatch from the box header.
	if err := indexID(a.ID(), a); err != nil {
		//: surface the doc code for grep-friendly panic messages.
		panic(err.Error())
	}
	//: returning the scheme lets callers bind it to a typed singleton var.
	return a
}

// indexID inserts (id -> a) into the wire-id index under the writer lock. Same
// idempotent-vs-conflict semantics as the shared registry's publish.
func indexID(id byte, a AEAD) error {
	//: dupErr escapes the Update closure to signal a conflicting wire id.
	var dupErr error
	idIndex.Update(func(current *map[byte]AEAD) *map[byte]AEAD {
		//: duplicate detection runs on the current snapshot before any alloc.
		if current != nil {
			//: an existing entry under id decides idempotent vs conflict.
			if existing, dup := (*current)[id]; dup {
				//: re-registering the SAME scheme is a no-op republish.
				if existing == a {
					//: nothing changes; keep the current snapshot.
					return current
				}
				//: a DISTINCT scheme under a taken id is the hard conflict.
				dupErr = fmt.Errorf("crypto.Register [%s %w]: duplicate wire id 0x%02x", CodeDuplicateRegistration, errDuplicateRegistration, id)
				//: no-op publish — republish the current snapshot unchanged.
				return current
			}
		}
		//: clone the snapshot + insert the new entry, then publish atomically.
		return new(cloneIDMap(current, id, a))
	})
	//: surface any conflict to Register, which panics with the doc code.
	return dupErr
}

// cloneIDMap copies src and inserts (id -> a). Register runs once per scheme at
// package import, so this clone is init-time, one-shot work.
func cloneIDMap(src *map[byte]AEAD, id byte, a AEAD) map[byte]AEAD {
	//: size hint = source size + 1 for the new entry; nil source -> 1.
	var size int
	//: nil source is the very-first-Register case; size stays zero.
	if src != nil {
		//: source has entries; pre-size for them plus one.
		size = len(*src)
	}
	//: allocate the new snapshot with the exact required capacity.
	next := make(map[byte]AEAD, size+1)
	//: bulk-copy every existing entry (no-op on a nil source).
	if src != nil {
		//: copy the existing id index forward.
		maps.Copy(next, *src)
	}
	//: insert the new entry.
	next[id] = a
	//: caller publishes the snapshot via Value.Update.
	return next
}

// Lookup returns the AEAD registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in scheme instances behind the AEAD
// interface — concrete types are intentionally unexported per scheme.
func Lookup(name Algorithm) (a AEAD, ok bool) {
	//: delegate to the shared registry's typed lookup.
	return aeads.lookup(name)
}

// lookupByID resolves the 1-byte wire id to its AEAD; used by Open to dispatch
// from the box header alone.
//
// IFACE-PLUGIN: returns the AEAD interface so the box id resolves to whichever
// scheme registered it; concrete scheme types stay unexported per package.
func lookupByID(id byte) (a AEAD, ok bool) {
	//: load the current snapshot pointer; nil before first Register call.
	current := idIndex.Load()
	//: absence path — no scheme registered yet.
	if current == nil {
		//: clean miss.
		return nil, false
	}
	//: typed map read.
	scheme, found := (*current)[id]
	//: hand back the typed scheme + lookup outcome.
	return scheme, found
}

// Available returns the sorted list of registered Algorithms.
func Available() []Algorithm {
	//: delegate to the shared registry's sorted key list.
	return aeads.available()
}
