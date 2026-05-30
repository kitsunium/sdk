// Package crypto — the process-wide PasswordHasher registry + PHC dispatch.
package crypto

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/snapshot"
)

// phcMinSegments is the minimum field count of a `$id$…` PHC string once split
// on "$": the empty prefix, the id, and at least one trailing field.
const phcMinSegments int = 3

// passwordHashers maps each Algorithm to its PasswordHasher. A fifth registry
// beside AEAD, Hasher, Signer, and Deriver. Same read-mostly snapshot.Value
// shape — register once at import, dispatch is lock-free.
var passwordHashers snapshot.Value[map[Algorithm]PasswordHasher]

// RegisterPasswordHasher inserts p under p.Algorithm() and returns it so callers
// can bind the singleton to a typed package-level variable. Panics on a nil
// hasher or when a distinct hasher already claims the same Algorithm.
//
// IFACE-PLUGIN: the registry hands plug-in PasswordHasher instances back to
// callers so each scheme keeps its concrete type unexported.
func RegisterPasswordHasher(p PasswordHasher) PasswordHasher {
	//: nil registration is always a programming error.
	if p == nil {
		//: panic so the offender is visible at boot.
		panic(fmt.Sprintf("crypto.RegisterPasswordHasher [%s DUPLICATE_REGISTRATION]: nil PasswordHasher", CodeDuplicateRegistration))
	}
	//: publish under the lock; a distinct duplicate Name is a hard conflict.
	if err := publishPasswordHasher(p.Algorithm(), p); err != nil {
		//: surface the doc code for grep-friendly panic messages.
		panic(err.Error())
	}
	//: returning the hasher lets callers bind it to a typed singleton var.
	return p
}

// publishPasswordHasher inserts (name -> p) into the snapshot under the writer
// lock. Returns a non-nil error when name is already registered to a different
// hasher; re-registering the same hasher is an idempotent no-op.
func publishPasswordHasher(name Algorithm, p PasswordHasher) error {
	//: dupErr escapes the Update closure to signal a conflicting registration.
	var dupErr error
	//: Update serialises writers on the snapshot mutex, so the duplicate check
	//: and the publish are atomic against any concurrent registration.
	passwordHashers.Update(func(current *map[Algorithm]PasswordHasher) *map[Algorithm]PasswordHasher {
		//: duplicate detection runs on the current snapshot before any alloc.
		if current != nil {
			//: an existing entry under name decides idempotent vs conflict.
			if existing, dup := (*current)[name]; dup {
				//: re-registering the SAME hasher is a no-op republish.
				if existing == p {
					//: nothing changes; keep the current snapshot.
					return current
				}
				//: a DISTINCT hasher under a taken name is the hard conflict.
				dupErr = fmt.Errorf("crypto.RegisterPasswordHasher [%s %w]: duplicate Algorithm %q", CodeDuplicateRegistration, errDuplicateRegistration, name)
				//: no-op publish — republish the current snapshot unchanged.
				return current
			}
		}
		//: clone the snapshot + insert the new entry, then publish atomically.
		return new(clonePasswordHasherMap(current, name, p))
	})
	//: surface any conflict to RegisterPasswordHasher, which panics with the code.
	return dupErr
}

// clonePasswordHasherMap copies src and inserts (name -> p). Registration runs
// once per scheme at package import, so this clone is init-time, one-shot work.
func clonePasswordHasherMap(src *map[Algorithm]PasswordHasher, name Algorithm, p PasswordHasher) map[Algorithm]PasswordHasher {
	//: size hint = source size + 1 for the new entry; nil source -> 1.
	var size int
	//: nil source is the very-first-Register case; size stays zero.
	if src != nil {
		//: source has entries; pre-size for them plus one.
		size = len(*src)
	}
	//: allocate the new snapshot with the exact required capacity.
	next := make(map[Algorithm]PasswordHasher, size+1)
	//: bulk-copy every existing entry (no-op on a nil source).
	if src != nil {
		maps.Copy(next, *src)
	}
	//: insert the new entry.
	next[name] = p
	//: caller publishes the snapshot via Value.Update.
	return next
}

// LookupPasswordHasher returns the PasswordHasher registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in instances behind the interface —
// concrete types are intentionally unexported per scheme.
func LookupPasswordHasher(name Algorithm) (p PasswordHasher, ok bool) {
	//: load the current snapshot pointer; nil before first registration.
	current := passwordHashers.Load()
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

// AvailablePasswordHashers returns the sorted list of registered Algorithms.
func AvailablePasswordHashers() []Algorithm {
	//: snapshot the registry pointer; nil before any registration.
	current := passwordHashers.Load()
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

// HashPassword returns a PHC-string hash of password using the PasswordHasher
// registered as name. A name with no registered hasher returns
// UnknownPasswordAlgorithm (blank-import the scheme's package to register it).
func HashPassword(name Algorithm, password []byte) (phc string, err error) {
	//: resolve the hasher first so a missing import surfaces a clear sentinel.
	hasher, ok := LookupPasswordHasher(name)
	//: absence path — the hasher package was never blank-imported.
	if !ok {
		//: surface the documented sentinel naming the missing algorithm.
		return "", UnknownPasswordAlgorithm
	}
	//: delegate hashing; the scheme owns its salt + cost parameters.
	return hasher.Hash(password)
}

// VerifyPassword reports whether password matches the stored PHC hash. The
// scheme is read from phc's id segment — no algorithm argument is needed. A
// malformed phc or unregistered scheme returns an error; a genuine mismatch is
// (false, nil).
func VerifyPassword(password []byte, phc string) (ok bool, err error) {
	//: the scheme id is the first PHC field; a missing id is a malformed hash.
	id, idOK := phcID(phc)
	//: reject an unparseable PHC string before any scheme lookup.
	if !idOK {
		//: a stored hash we cannot parse is server data corruption.
		return false, InvalidPasswordHash
	}
	//: resolve the scheme named by the PHC id.
	hasher, found := LookupPasswordHasher(Algorithm(id))
	//: a PHC produced by a scheme we never imported cannot be checked.
	if !found {
		//: surface the missing-scheme sentinel, distinct from a mismatch.
		return false, UnknownPasswordAlgorithm
	}
	//: delegate the constant-time comparison to the scheme.
	return hasher.Verify(password, phc)
}

// NeedsRehash reports whether the stored PHC hash was produced with cost
// parameters weaker than its scheme's current policy, so a caller can re-hash on
// a successful login. A malformed or unknown-scheme phc reports false.
func NeedsRehash(phc string) bool {
	//: a hash we cannot attribute to a scheme is left untouched.
	id, idOK := phcID(phc)
	//: unparseable PHC — nothing to upgrade here.
	if !idOK {
		//: report not-stale; VerifyPassword surfaces the real error.
		return false
	}
	//: resolve the scheme; an unimported scheme cannot judge staleness.
	hasher, found := LookupPasswordHasher(Algorithm(id))
	//: unknown scheme — leave the stored hash as-is.
	if !found {
		//: report not-stale.
		return false
	}
	//: the scheme compares the embedded params against its current policy.
	return hasher.NeedsRehash(phc)
}

// phcID extracts the scheme id from a PHC string of the form `$id$...`. It
// returns ok=false for any string not starting with `$id$`.
func phcID(phc string) (id string, ok bool) {
	//: a PHC string must begin with the `$` field separator.
	if !strings.HasPrefix(phc, "$") {
		//: not a PHC string at all.
		return "", false
	}
	//: split into fields; index 0 is the empty prefix before the first `$`.
	fields := strings.Split(phc, "$")
	//: require at least `$id$` — an empty prefix, the id, and one more field.
	if len(fields) < phcMinSegments || fields[1] == "" {
		//: missing or empty id segment.
		return "", false
	}
	//: the id is the first non-empty field.
	return fields[1], true
}
