// Package x25519 registers the "x25519" key-agreement scheme (ADR 0014).
// Importing the package (typically a blank import via pkg/v1/crypto/agree) self-registers
// the scheme so crypto.GenerateAgreementKey / crypto.AgreementShared resolve. It
// is stdlib-only (crypto/ecdh + crypto/rand), so it pulls zero non-stdlib deps
// and keeps pkg/v1/crypto/agree consumers dep-light.
//
// X25519 (RFC 7748) is the modern Diffie-Hellman default: a 32-byte public key,
// a 32-byte private key, and a 32-byte shared secret. crypto/ecdh rejects
// low-order peer points, so a malformed or attacker-chosen peer key surfaces as
// a typed error rather than a degenerate secret.
//
// Key-hygiene contract: the priv returned by GenerateKey and the secret returned
// by Shared are RAW key material. The caller (the pkg/v1/crypto/agree facade) MUST run
// the secret through a KDF before use and Zeroize both when done — this scheme
// never logs or retains either.
package x25519
