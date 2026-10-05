// Package aesgcm registers the "aes-256-gcm" AEAD scheme (ADR 0013). Importing
// the package (typically a blank import via pkg/v1/crypto) self-registers the
// scheme so crypto.Seal / crypto.Open resolve. It is stdlib-only
// (crypto/aes + crypto/cipher + crypto/rand), so it pulls zero non-stdlib deps
// and keeps pkg/v1/crypto consumers dep-light.
package aesgcm
