// Package hkdfsha256 registers the "hkdf-sha256" key-derivation scheme
// (ADR 0013). Importing the package (typically a blank import via pkg/v1/crypto/kdf)
// self-registers the scheme so crypto.Subkey resolves. It is stdlib-only
// (crypto/hkdf + crypto/sha256), so it pulls zero non-stdlib deps and keeps
// pkg/v1/crypto/kdf consumers dep-light.
//
// HKDF (RFC 5869) is for KEY SEPARATION — expanding one strong secret into
// independent, purpose-bound subkeys — NOT for stretching passwords. Feed it a
// high-entropy secret; argon2id handles human passwords under its own port.
package hkdfsha256
