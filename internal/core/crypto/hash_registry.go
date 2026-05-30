// Package crypto — the process-wide Hasher registry + Sum / SumHex dispatch.
package crypto

import (
	"encoding/hex"
	"fmt"
	"hash"
	"maps"
	"slices"

	"github.com/kitsunium/sdk/internal/kernel/snapshot"
)

// hashers maps each Algorithm to its Hasher. Separate from the AEAD registry:
// fingerprint hashing is a distinct, non-authenticated capability. Same
// read-mostly snapshot.Value shape — register once at import, Sum is lock-free.
var hashers snapshot.Value[map[Algorithm]Hasher]

// RegisterHasher inserts h under h.Algorithm() and returns it so callers can
// bind the singleton to a typed package-level variable like
// `var Hasher = crypto.RegisterHasher(sha256Hasher{})`. Panics on a nil hasher
// or when a distinct hasher already claims the same Algorithm.
//
// IFACE-PLUGIN: the registry hands plug-in Hasher instances back to callers so
// each scheme keeps its concrete type unexported; the stable contract is the
// Hasher interface itself.
func RegisterHasher(h Hasher) Hasher {
	//: nil registration is always a programming error.
	if h == nil {
		//: panic so the offender is visible at boot.
		panic(fmt.Sprintf("crypto.RegisterHasher [%s DUPLICATE_REGISTRATION]: nil Hasher", CodeDuplicateRegistration))
	}
	//: publish under the hasher lock; a distinct duplicate Name is a hard conflict.
	if err := publishHasher(h.Algorithm(), h); err != nil {
		//: surface the doc code for grep-friendly panic messages.
		panic(err.Error())
	}
	//: returning the hasher lets callers bind it to a typed singleton var.
	return h
}

// publishHasher inserts (name -> h) into the hasher snapshot under the writer
// lock. Returns a non-nil error when name is already registered to a different
// hasher; re-registering the same hasher is an idempotent no-op.
func publishHasher(name Algorithm, h Hasher) error {
	//: dupErr escapes the Update closure to signal a conflicting registration.
	var dupErr error
	//: Update serialises writers on the snapshot mutex, so the duplicate check
	//: and the publish are atomic against any concurrent RegisterHasher.
	hashers.Update(func(current *map[Algorithm]Hasher) *map[Algorithm]Hasher {
		//: duplicate detection runs on the current snapshot before any alloc.
		if current != nil {
			//: an existing entry under name decides idempotent vs conflict.
			if existing, dup := (*current)[name]; dup {
				//: re-registering the SAME hasher is a no-op republish.
				if existing == h {
					//: nothing changes; keep the current snapshot.
					return current
				}
				//: a DISTINCT hasher under a taken name is the hard conflict.
				dupErr = fmt.Errorf("crypto.RegisterHasher [%s %w]: duplicate Algorithm %q", CodeDuplicateRegistration, errDuplicateRegistration, name)
				//: no-op publish — republish the current snapshot unchanged.
				return current
			}
		}
		//: clone the snapshot + insert the new entry, then publish atomically.
		return new(cloneHasherMap(current, name, h))
	})
	//: surface any conflict to RegisterHasher, which panics with the doc code.
	return dupErr
}

// cloneHasherMap copies src and inserts (name -> h). RegisterHasher runs once
// per scheme at package import, so this clone is init-time, one-shot work.
func cloneHasherMap(src *map[Algorithm]Hasher, name Algorithm, h Hasher) map[Algorithm]Hasher {
	//: size hint = source size + 1 for the new entry; nil source -> 1.
	var size int
	//: nil source is the very-first-Register case; size stays zero.
	if src != nil {
		//: source has entries; pre-size for them plus one.
		size = len(*src)
	}
	//: allocate the new snapshot with the exact required capacity.
	next := make(map[Algorithm]Hasher, size+1)
	//: bulk-copy every existing entry (no-op on a nil source).
	if src != nil {
		maps.Copy(next, *src)
	}
	//: insert the new entry.
	next[name] = h
	//: caller publishes the snapshot via Value.Update.
	return next
}

// LookupHasher returns the Hasher registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in Hasher instances behind the Hasher
// interface — concrete types are intentionally unexported per scheme.
func LookupHasher(name Algorithm) (h Hasher, ok bool) {
	//: load the current snapshot pointer; nil before first RegisterHasher call.
	current := hashers.Load()
	//: absence path — no hasher registered yet.
	if current == nil {
		//: clean miss.
		return nil, false
	}
	//: typed map read.
	hasher, found := (*current)[name]
	//: hand back the typed hasher + lookup outcome.
	return hasher, found
}

// AvailableHashers returns the sorted list of registered hash Algorithms.
func AvailableHashers() []Algorithm {
	//: snapshot the registry pointer; nil before any RegisterHasher.
	current := hashers.Load()
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

// Sum returns the digest of data under the Hasher registered as name. A name
// with no registered hasher returns UnknownHashAlgorithm (blank-import the
// scheme's package to register it).
func Sum(name Algorithm, data []byte) (digest []byte, err error) {
	//: resolve the hasher first so a missing import surfaces a clear sentinel.
	hasher, ok := LookupHasher(name)
	//: absence path — the hasher package was never blank-imported.
	if !ok {
		//: surface the documented sentinel naming the missing algorithm.
		return nil, UnknownHashAlgorithm
	}
	//: one-shot: a fresh hash, write the data, read the digest.
	hsh := hasher.New()
	//: hash.Hash.Write is contractually error-free, but capture + propagate so a
	//: non-conforming custom Hasher can never silently drop a fault.
	if _, werr := hsh.Write(data); werr != nil {
		//: unreachable for every stdlib hasher; surfaced, never swallowed.
		return nil, werr
	}
	//: Sum(nil) appends the digest to a fresh slice.
	return hsh.Sum(nil), nil
}

// NewHash returns a fresh streaming hash.Hash for the Hasher registered as
// name (for io.Copy over large inputs). A name with no registered hasher
// returns UnknownHashAlgorithm.
func NewHash(name Algorithm) (h hash.Hash, err error) {
	//: resolve the hasher; a missing import surfaces the typed sentinel.
	hasher, ok := LookupHasher(name)
	//: absence path — the hasher package was never blank-imported.
	if !ok {
		//: surface the documented sentinel naming the missing algorithm.
		return nil, UnknownHashAlgorithm
	}
	//: hand back a fresh streaming hash for the caller to Write/Sum.
	return hasher.New(), nil
}

// SumHex is Sum rendered as canonical lowercase hex — the frozen string form
// for content IDs and cache keys.
func SumHex(name Algorithm, data []byte) (digest string, err error) {
	//: delegate to Sum, then hex-encode on success.
	raw, sErr := Sum(name, data)
	//: propagate an unknown-algorithm error unchanged.
	if sErr != nil {
		//: no digest to encode.
		return "", sErr
	}
	//: lowercase hex is the canonical, frozen rendering.
	return hex.EncodeToString(raw), nil
}
