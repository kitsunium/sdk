// Package session — constant-time comparison of the values that name a session.
package session

import "crypto/subtle"

// digestsEqual compares two ID.Digest values in time that does not depend on
// how many leading characters they share.
//
// A digest is not itself a secret — it is the unkeyed SHA-256 of the
// identifier, and anyone holding the identifier can compute it. The
// constant-time compare is here anyway, for one reason: it makes "every path
// from a presented identifier to a stored record ends in a crypto/subtle
// comparison" a property that is TRUE rather than nearly true. A map lookup and
// a filename match are both variable-time, and both are followed by this call,
// so no amount of reasoning about which of them was the real check is needed.
func digestsEqual(stored, presented string) bool {
	//: ConstantTimeCompare returns 0 on a length mismatch without an early
	//: return, so a truncated digest never short-circuits.
	return subtle.ConstantTimeCompare([]byte(stored), []byte(presented)) == 1
}
