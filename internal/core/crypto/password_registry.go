// Package crypto — the process-wide PasswordHasher registry + HashPassword / VerifyPassword / NeedsRehash dispatch.
package crypto

import (
	"fmt"
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/plugin"
)

// phcMinSegments is the minimum field count of a `$id$…` PHC string once split
// on `$`: the empty prefix, the id, and at least one more field.
const phcMinSegments int = 3

// passwordHashers maps each Algorithm to its PasswordHasher. Backed by the
// shared read-mostly schemeRegistry — register once at import, dispatch is
// lock-free.
var passwordHashers = schemeRegistry[PasswordHasher]{verb: "RegisterPasswordHasher"}

// RegisterPasswordHasher inserts p under p.Algorithm() and returns it so callers
// can bind the singleton to a typed package-level variable. Panics on a nil
// hasher or when a distinct hasher already claims the same Algorithm.
//
// IFACE-PLUGIN: the registry hands plug-in PasswordHasher instances back to
// callers so each scheme keeps its concrete type unexported; the contract is the
// PasswordHasher interface itself.
//
// "Nil" here means UNUSABLE, not only an untyped nil: a typed nil pointer and a
// plug-in whose type is not comparable both satisfy the port and neither can
// serve one call (see internal/kernel/plugin).
func RegisterPasswordHasher(p PasswordHasher) PasswordHasher {
	//: a typed nil and a non-comparable plug-in both satisfy the port and
	//: neither can serve — refuse at import, where the offender is named.
	if why := plugin.Unusable(p); why != "" {
		//: panic so the offender is visible at boot.
		panic(fmt.Sprintf("crypto.RegisterPasswordHasher [%s DUPLICATE_REGISTRATION]: %s", CodeDuplicateRegistration, why))
	}
	//: publish via the shared registry; a distinct duplicate Name is a hard conflict.
	if err := passwordHashers.publish(p.Algorithm(), p); err != nil {
		//: surface the doc code for grep-friendly panic messages.
		panic(err.Error())
	}
	//: returning the hasher lets callers bind it to a typed singleton var.
	return p
}

// LookupPasswordHasher returns the PasswordHasher registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in PasswordHasher instances behind the
// PasswordHasher interface — concrete types are intentionally unexported per scheme.
func LookupPasswordHasher(name Algorithm) (p PasswordHasher, ok bool) {
	//: delegate to the shared registry's typed lookup.
	return passwordHashers.lookup(name)
}

// AvailablePasswordHashers returns the sorted list of registered password
// Algorithms.
func AvailablePasswordHashers() []Algorithm {
	//: delegate to the shared registry's sorted key list.
	return passwordHashers.available()
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
