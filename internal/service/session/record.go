// Package session — the stored form of a session.
package session

import "time"

// record is what a store keeps. It is deliberately NOT a
// core/session.SessionValue: the value type carries the identifier, and a
// record must not.
//
// # The identifier is never stored
//
// A record holds ID.Digest — the unkeyed SHA-256 of the identifier — and never
// the identifier itself. The digest is what the map is keyed on and what the
// file store names its files after, so the bearer secret is not a map key, not
// a directory entry, and not on disk. A stolen backup, a leaked directory
// listing or a core dump of the file store therefore yields digests, which are
// not cookies. On a successful lookup the store re-attaches the identifier the
// CALLER presented; it never reads one back.
//
// # There is no idle deadline here either
//
// Only lastSeen is stored. The idle deadline is lastSeen + the store's
// IdleTimeout, computed on read, so changing the timeout in configuration
// takes effect on the sessions that already exist instead of leaving a
// generation of records pinned to the old policy.
type record struct {
	// digest is ID.Digest() — the lookup key, kept so a load can re-check it
	// in constant time rather than trusting the map or the filename alone.
	digest string
	// subject is the authenticated principal, "" when anonymous. It is written
	// by minting and by regeneration, and by nothing else.
	subject string
	// createdAt is when the identifier was minted; it anchors absoluteExpiry
	// and never moves for the life of this identifier.
	createdAt time.Time
	// lastSeen is when the idle window last slid.
	lastSeen time.Time
	// absoluteExpiry is the ceiling, stamped once at minting.
	absoluteExpiry time.Time
	// data is the caller's payload.
	data map[string]string
}
