// Package transform — holds the process-wide Compressor registry. Service-level
// scheme packages register themselves via package-level var initialisers when
// imported (no init()), mirroring core/codec and core/crypto.
package transform

import (
	"maps"
	"slices"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/plugin"
	"github.com/kitsunium/sdk/internal/kernel/snapshot"
)

// registry maps each Algorithm to its Compressor.
//
// snapshot.Value[map[K]V] is the deliberate choice over sync.Map: schemes
// register exactly ONCE at import time and every other access is a lock-free
// Load (ADR 0011) — the same read-mostly shape that justifies it for the codec
// and crypto registries. Update serialises writers on a mutex so Register's
// read-modify-write publish is race-free; Lookup stays lock-free.
var registry snapshot.Value[map[Algorithm]Compressor]

// Register inserts c into the registry under c.Algorithm() and returns it so
// callers can bind the singleton to a typed package-level variable like
// `var GzipCompressor = transform.Register(gzipCompressor{})`. Panics on a nil
// scheme or when a distinct scheme already claims the same Algorithm.
//
// IFACE-PLUGIN: the registry hands plug-in Compressor instances back to callers
// so each scheme keeps its concrete type unexported; the stable contract is the
// Compressor interface itself.
//
// "Nil" here means UNUSABLE, not only an untyped nil: a typed nil pointer and a
// plug-in whose type is not comparable both satisfy the port and neither can
// serve one call (see internal/kernel/plugin).
func Register(c Compressor) Compressor {
	//: a typed nil and a non-comparable plug-in both satisfy the port and
	//: neither can serve — refuse at import, where the offender is named.
	if why := plugin.Unusable(c); why != "" {
		//: panic with the DuplicateRegistration sentinel so the bracket header
		//: "[<0.2.5.5> DUPLICATE_REGISTRATION]" is a valid (code,reason) pairing —
		//: the sentinel's Error() already renders the dotted-quad code + reason.
		panic(DuplicateRegistration.Error() + ": " + why)
	}
	//: publish under the scheme name; a conflict turns into a boot-time panic.
	if err := publishCompressor(c.Algorithm(), c); err != nil {
		//: surface the doc code for grep-friendly panic messages.
		panic(err.Error())
	}
	//: returning the scheme lets callers bind it to a typed singleton var.
	return c
}

// publishCompressor inserts (name -> c) into the registry snapshot under the
// writer lock. Returns a non-nil error when name is already registered to a
// different scheme; re-registering the same scheme is an idempotent no-op.
func publishCompressor(name Algorithm, c Compressor) error {
	//: dupErr escapes the Update closure to signal a conflicting registration.
	var dupErr error
	//: Update serialises writers on the snapshot mutex, so the duplicate check
	//: and the publish are atomic against any concurrent Register.
	registry.Update(func(current *map[Algorithm]Compressor) *map[Algorithm]Compressor {
		//: duplicate detection runs on the current snapshot before any alloc.
		if current != nil {
			//: an existing entry under name decides idempotent vs conflict.
			if existing, dup := (*current)[name]; dup {
				//: re-registering the SAME scheme is a no-op republish.
				if existing == c {
					//: nothing changes; keep the current snapshot.
					return current
				}
				//: a DISTINCT scheme under a taken name is the hard conflict;
				//: origin-wins keeps the DuplicateRegistration code+reason so the
				//: bracket header pairs 0.2.5.5 with DUPLICATE_REGISTRATION, while
				//: the algorithm name rides along as a structured field (the empty
				//: WrapParams.Code is dropped from the trail per the poison-pill rule).
				dupErr = errs.Wrap(DuplicateRegistration, errs.WrapParams{}, errs.String("algorithm", string(name)))
				//: no-op publish — republish the current snapshot unchanged.
				return current
			}
		}
		//: clone the snapshot + insert the new entry, then publish atomically.
		return new(cloneCompressorMap(current, name, c))
	})
	//: surface any conflict to Register, which panics with the doc code.
	return dupErr
}

// cloneCompressorMap copies src and inserts (name -> c). Register runs once per
// scheme at package import, so this clone is init-time, one-shot work.
func cloneCompressorMap(src *map[Algorithm]Compressor, name Algorithm, c Compressor) map[Algorithm]Compressor {
	//: size hint = source size + 1 for the new entry; nil source -> 1.
	var size int
	//: nil source is the very-first-Register case; size stays zero.
	if src != nil {
		//: source has entries; pre-size for them plus one.
		size = len(*src)
	}
	//: allocate the new snapshot with the exact required capacity.
	next := make(map[Algorithm]Compressor, size+1)
	//: bulk-copy every existing entry (no-op on a nil source).
	if src != nil {
		maps.Copy(next, *src)
	}
	//: insert the new entry.
	next[name] = c
	//: caller publishes the snapshot via Value.Update.
	return next
}

// Lookup returns the Compressor registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in scheme instances behind the
// Compressor interface — concrete types are intentionally unexported per scheme.
func Lookup(name Algorithm) (c Compressor, ok bool) {
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

// Available returns the sorted list of registered Algorithms.
func Available() []Algorithm {
	//: snapshot the registry pointer; nil before any Register.
	current := registry.Load()
	//: empty result when nothing registered yet.
	if current == nil {
		//: nil slice is the documented zero value.
		return nil
	}
	//: collect the keys, then sort for a stable, reflection-free order.
	names := slices.Collect(maps.Keys(*current))
	//: slices.Sort avoids reflection compared to sort.Slice.
	slices.Sort(names)
	//: hand back the freshly ordered slice.
	return names
}
