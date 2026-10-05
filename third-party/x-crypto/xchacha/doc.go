// Package xchacha registers the "xchacha20poly1305" AEAD scheme (ADR 0013).
// Blank-importing the package self-registers the scheme so
// crypto.SealAs("xchacha20poly1305", …) / crypto.Open resolve. It is the ONLY
// place golang.org/x/crypto enters a build for this scheme — declared in the
// go.mod of the third-party/x-crypto module (ADR 0157), which nothing in the
// SDK requires, so pkg/v1 consumers stay dep-light (zero x/crypto) unless they
// opt in with this blank import.
//
// XChaCha20-Poly1305 uses a 192-bit (24-byte) random nonce, eliminating the
// birthday-bound message-count limit that AES-256-GCM's 96-bit random nonce
// imposes — preferred for high-volume random-nonce workloads.
package xchacha
