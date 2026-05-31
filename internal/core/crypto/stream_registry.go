// Package crypto — the StreamSealer registry + SealStream / OpenStream dispatch.
package crypto

import (
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/kitsunium/sdk/internal/kernel/snapshot"
)

// streamSealers maps each Algorithm to its StreamSealer. Streaming AEAD extends
// the whole-buffer AEAD domain, so a lookup miss reuses the AEAD UnknownAlgorithm
// sentinel rather than a stream-only one. Same read-mostly snapshot.Value shape —
// register once at import, dispatch is lock-free.
var streamSealers snapshot.Value[map[Algorithm]StreamSealer]

// RegisterStreamSealer inserts s under s.Algorithm() and returns it so callers
// can bind the singleton to a typed package-level variable like
// `var StreamSealer = crypto.RegisterStreamSealer(streamAES{})`. Panics on a nil
// sealer or when a distinct sealer already claims the same Algorithm.
//
// IFACE-PLUGIN: the registry hands plug-in StreamSealer instances back to callers
// so each scheme keeps its concrete type unexported; the stable contract is the
// StreamSealer interface itself.
func RegisterStreamSealer(s StreamSealer) StreamSealer {
	//: nil registration is always a programming error.
	if s == nil {
		//: panic so the offender is visible at boot.
		panic(fmt.Sprintf("crypto.RegisterStreamSealer [%s DUPLICATE_REGISTRATION]: nil StreamSealer", CodeDuplicateRegistration))
	}
	//: publish under the sealer lock; a distinct duplicate Name is a conflict.
	if err := publishStreamSealer(s.Algorithm(), s); err != nil {
		//: surface the doc code for grep-friendly panic messages.
		panic(err.Error())
	}
	//: returning the sealer lets callers bind it to a typed singleton var.
	return s
}

// publishStreamSealer inserts (name -> s) into the sealer snapshot under the
// writer lock. Returns a non-nil error when name is already registered to a
// different sealer; re-registering the same sealer is an idempotent no-op.
func publishStreamSealer(name Algorithm, s StreamSealer) error {
	//: dupErr escapes the Update closure to signal a conflicting registration.
	var dupErr error
	//: Update serialises writers on the snapshot mutex, so the duplicate check
	//: and the publish are atomic against any concurrent RegisterStreamSealer.
	streamSealers.Update(func(current *map[Algorithm]StreamSealer) *map[Algorithm]StreamSealer {
		//: duplicate detection runs on the current snapshot before any alloc.
		if current != nil {
			//: an existing entry under name decides idempotent vs conflict.
			if existing, dup := (*current)[name]; dup {
				//: re-registering the SAME sealer is a no-op republish.
				if existing == s {
					//: nothing changes; keep the current snapshot.
					return current
				}
				//: a DISTINCT sealer under a taken name is the hard conflict.
				dupErr = fmt.Errorf("crypto.RegisterStreamSealer [%s %w]: duplicate Algorithm %q", CodeDuplicateRegistration, errDuplicateRegistration, name)
				//: no-op publish — republish the current snapshot unchanged.
				return current
			}
		}
		//: clone the snapshot + insert the new entry, then publish atomically.
		return new(cloneStreamSealerMap(current, name, s))
	})
	//: surface any conflict to RegisterStreamSealer, which panics with the code.
	return dupErr
}

// cloneStreamSealerMap copies src and inserts (name -> s). RegisterStreamSealer
// runs once per scheme at package import, so this clone is init-time work.
func cloneStreamSealerMap(src *map[Algorithm]StreamSealer, name Algorithm, s StreamSealer) map[Algorithm]StreamSealer {
	//: size hint = source size + 1 for the new entry; nil source -> 1.
	var size int
	//: nil source is the very-first-Register case; size stays zero.
	if src != nil {
		//: source has entries; pre-size for them plus one.
		size = len(*src)
	}
	//: allocate the new snapshot with the exact required capacity.
	next := make(map[Algorithm]StreamSealer, size+1)
	//: bulk-copy every existing entry (no-op on a nil source).
	if src != nil {
		maps.Copy(next, *src)
	}
	//: insert the new entry.
	next[name] = s
	//: caller publishes the snapshot via Value.Update.
	return next
}

// LookupStreamSealer returns the StreamSealer registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in StreamSealer instances behind the
// StreamSealer interface — concrete types are intentionally unexported per scheme.
func LookupStreamSealer(name Algorithm) (s StreamSealer, ok bool) {
	//: load the current snapshot pointer; nil before first RegisterStreamSealer.
	current := streamSealers.Load()
	//: absence path — no sealer registered yet.
	if current == nil {
		//: clean miss.
		return nil, false
	}
	//: typed map read.
	sealer, found := (*current)[name]
	//: hand back the typed sealer + lookup outcome.
	return sealer, found
}

// AvailableStreamSealers returns the sorted list of registered streaming
// Algorithms.
func AvailableStreamSealers() []Algorithm {
	//: snapshot the registry pointer; nil before any RegisterStreamSealer.
	current := streamSealers.Load()
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

// SealStream wraps dst so writes are sealed under key with aad by the
// StreamSealer registered as name. Streaming extends the AEAD domain, so a name
// with no registered sealer returns the shared UnknownAlgorithm sentinel; a
// scheme construction fault propagates unchanged.
func SealStream(name Algorithm, key Key, dst io.Writer, aad []byte) (sealed io.WriteCloser, err error) {
	//: resolve the sealer; a missing import surfaces the AEAD-domain sentinel.
	sealer, ok := LookupStreamSealer(name)
	//: absence path — the sealer package was never blank-imported.
	if !ok {
		//: streaming reuses the AEAD UnknownAlgorithm, not a stream-only miss.
		return nil, UnknownAlgorithm
	}
	//: delegate construction; the scheme owns its salt + header framing.
	return sealer.Writer(key, dst, aad)
}

// OpenStream wraps src so reads are opened under key with aad by the
// StreamSealer registered as name. A name with no registered sealer returns the
// shared UnknownAlgorithm sentinel; a truncated stream surfaces later as
// StreamTruncated from the returned reader.
func OpenStream(name Algorithm, key Key, src io.Reader, aad []byte) (opened io.Reader, err error) {
	//: resolve the sealer; a missing import surfaces the AEAD-domain sentinel.
	sealer, ok := LookupStreamSealer(name)
	//: absence path — the sealer package was never blank-imported.
	if !ok {
		//: streaming reuses the AEAD UnknownAlgorithm, not a stream-only miss.
		return nil, UnknownAlgorithm
	}
	//: delegate construction; the reader enforces the hold-back contract.
	return sealer.Reader(key, src, aad)
}
