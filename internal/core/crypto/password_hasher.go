// Package crypto — the password-hashing port implemented by each scheme.
package crypto

// PasswordHasher stretches a human password into a slow, salted, self-describing
// PHC-string hash for storage. UNLIKE the Deriver (key separation) and Hasher
// (fingerprints) ports, this one is DELIBERATELY SLOW and salted: it is the only
// crypto surface meant to consume a low-entropy human secret.
//
// The returned hash is a PHC string (`$id$params$salt$digest`) that embeds the
// scheme id and its cost parameters, so Verify needs no side-channel: the stored
// string fully describes how to check it. Implementations MUST compare digests
// in constant time and MUST be safe for concurrent use.
//
// IFACE-PLUGIN: the registry hands plug-in PasswordHasher instances back to
// callers behind this interface; concrete scheme types stay unexported.
type PasswordHasher interface {
	// Algorithm reports the canonical key under which this scheme registers; it
	// is also the PHC id segment (the first `$`-delimited field).
	Algorithm() Algorithm
	// Hash returns a PHC-string hash of password with a fresh random salt and
	// the scheme's current cost parameters, or PasswordHashFailed on an entropy
	// fault.
	Hash(password []byte) (phc string, err error)
	// Verify reports whether password matches the stored PHC hash, comparing in
	// constant time. A malformed phc returns InvalidPasswordHash; a genuine
	// mismatch is (false, nil) so Verify is not an oracle on WHY it failed.
	Verify(password []byte, phc string) (ok bool, err error)
	// NeedsRehash reports whether phc was produced with cost parameters weaker
	// than this scheme's current policy, so callers can transparently re-hash on
	// a successful login. A malformed phc reports false (Verify surfaces the error).
	NeedsRehash(phc string) (stale bool)
}
