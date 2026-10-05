// Package crypto is the authenticated-encryption facade: Seal and Open with a
// hidden nonce.
//
// One key, two verbs. [Seal] encrypts a byte slice into a self-describing box;
// [Open] turns the box back into plaintext. The nonce is generated, embedded,
// and stripped for you — there is no nonce parameter to misuse, and the secure
// path is the only path.
//
//	k, _   := crypto.NewKey(key32)            // exactly 32 bytes, copied + redacting
//	box, _ := crypto.Seal(k, []byte("hi"), nil) // nonce generated + hidden inside box
//	pt, _  := crypto.Open(k, box, nil)          // []byte("hi")
//
// # Hard to misuse, by design
//
//   - The nonce never appears in the API. [Seal] draws it from crypto/rand and
//     embeds it; [Open] strips it. Nonce reuse is structurally impossible.
//   - [NewKey] rejects any input that is not exactly [KeyLen] (32) bytes with
//     InvalidKey — never silent truncation — and copies the bytes defensively.
//   - [Key] redacts: its %v / %s / %#v output is always "<redacted>", and key
//     material never reaches an error's public or private message.
//   - [Open] returns one undifferentiated error for every failure (bad tag,
//     tampered framing, wrong key), so it cannot be used as an oracle.
//
// # Associated data (aad)
//
// The optional aad argument is authenticated but NOT encrypted: it is bound into
// the tag so Open fails if it differs, but it is not stored in the box. Use it
// to tie a ciphertext to its context (a record id, a table name, a format tag)
// so a box cannot be replayed into a different context. Pass nil when unused.
//
// # Algorithms
//
// The default is AES-256-GCM (stdlib, hardware-accelerated, FIPS-track) and is
// active out of the box — importing this package activates it with zero
// non-stdlib dependencies. [SealAs] selects a specific [Algorithm]; additional
// schemes (e.g. XChaCha20-Poly1305) activate via their own blank import.
//
// # Wire format
//
// A box is [1-byte version][1-byte algorithm-id][nonce][ciphertext || tag]. The
// version and per-algorithm id are frozen post-v1.0.0, so a box sealed today
// opens tomorrow. Open reads the id to pick the algorithm — the caller never
// names it.
//
// # Streaming large payloads
//
// [Seal] is whole-buffer: it holds the full plaintext and box in memory. For
// payloads too large to buffer, [SealStream] / [OpenStream] wrap an io.Writer /
// io.Reader and process the data in fixed 64 KiB authenticated chunks under a
// disjoint, frozen wire format (stream version 0x02). The chunked construction
// (a random per-stream salt + a counter nonce + a final-chunk flag) is
// truncation-resistant and never surfaces a chunk's plaintext before it
// authenticates. The streaming frame is bespoke and SDK-owned (not age/libsodium
// interop) and stays stdlib-only, so it preserves the dep-light invariant.
package crypto
