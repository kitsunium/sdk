// Package ecdsasig registers the "ecdsa-p256" signature scheme (ADR 0013).
// Importing the package (typically a blank import via pkg/v1/crypto/sign) self-registers
// the scheme so crypto.Sign / crypto.Verify / crypto.GenerateKey resolve. It is
// stdlib-only (crypto/ecdsa + crypto/elliptic + crypto/sha256 + crypto/x509 +
// crypto/rand), so it pulls zero non-stdlib deps and keeps pkg/v1/crypto/sign dep-light.
//
// ECDSA over NIST P-256 with SHA-256 digests and ASN.1/DER signatures — the
// interoperable choice for JWTs, X.509, and other ecosystems that expect ECDSA.
// Ed25519 (ed25519sig) is the modern default when interop is not required.
//
// Keys are DER-marshalled: the public key is PKIX (SubjectPublicKeyInfo) and the
// private key is SEC1 (the x509.MarshalECPrivateKey form).
package ecdsasig
