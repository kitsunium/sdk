// Package pbkdf2pw registers the "pbkdf2-sha256" password-hashing scheme
// (ADR 0013). Importing the package (typically a blank import via
// pkg/v1/crypto/password) self-registers the scheme so crypto.HashPassword /
// crypto.VerifyPassword resolve. It is stdlib-only (crypto/pbkdf2 + crypto/sha256
// + crypto/rand + crypto/subtle, all Go 1.26), so it pulls zero non-stdlib deps
// and is the default that keeps pkg/v1/crypto/password consumers dep-light.
//
// PBKDF2 is the stdlib password stretcher. argon2id (memory-hard, the OWASP
// first choice) lives under third-party/x-crypto as an opt-in scheme — import
// it explicitly when you can afford the x/crypto dependency. PBKDF2-SHA256 at
// 600 000 iterations is the OWASP-recommended fallback where argon2 is absent.
//
// Stored hashes use the PHC string format:
//
//	$pbkdf2-sha256$i=600000$<b64-salt>$<b64-digest>
package pbkdf2pw
