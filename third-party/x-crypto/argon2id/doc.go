// Package argon2id registers the "argon2id" password-hashing scheme (ADR 0013).
// It is the memory-hard, OWASP-first-choice password hash — but it depends on
// golang.org/x/crypto, so it lives under third-party/x-crypto as an OPT-IN
// scheme (mirroring xchacha vs the stdlib aesgcm default). Blank-import this
// package to register it, then crypto.HashPassword("argon2id", …) resolves;
// crypto.VerifyPassword already verifies any registered scheme's stored hashes.
//
// Stored hashes use the standard argon2 PHC string:
//
//	$argon2id$v=19$m=19456,t=2,p=1$<b64-salt>$<b64-digest>
package argon2id
