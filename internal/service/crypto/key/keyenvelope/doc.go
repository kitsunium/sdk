// Package keyenvelope wraps a symmetric DEK at rest under a passphrase.
//
// A wrapped key is rendered in the frozen PHC-style grammar
//
//	$kenv$v=1$kdf=<id>$<b64salt>$aead=<id>$<b64box>
//
// where <b64salt> and <b64box> use base64.RawStdEncoding (PHC convention).
// The KEK is derived from the passphrase with PBKDF2-SHA256 (600000
// iterations, pinned by the kdf id — there is no iterations field) and the
// DEK is sealed under AES-256-GCM with the envelope header as AAD, binding
// the framing so a tampered header fails the AEAD open. The package mints no
// error codes of its own: structural faults map to the core
// InvalidKeyEnvelope sentinel and a wrong passphrase forwards the core
// DecryptionFailed sentinel verbatim (no key/password oracle beyond
// structural validity).
package keyenvelope
