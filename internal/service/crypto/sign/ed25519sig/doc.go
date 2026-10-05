// Package ed25519sig registers the "ed25519" signature scheme (ADR 0013).
// Importing the package (typically a blank import via pkg/v1/crypto/sign) self-registers
// the scheme so crypto.Sign / crypto.Verify / crypto.GenerateKey resolve. It is
// stdlib-only (crypto/ed25519 + crypto/rand), so it pulls zero non-stdlib deps
// and keeps pkg/v1/crypto/sign consumers dep-light.
//
// Ed25519 is the modern default: small fixed-size keys and signatures, fast
// verification, and no parameter choices to misconfigure. ECDSA lands later
// under its own scheme package.
package ed25519sig
