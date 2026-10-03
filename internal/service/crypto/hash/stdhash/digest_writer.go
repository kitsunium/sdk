// Package stdhash — DigestWriter streaming content-address ergonomics (see the
// package doc in stdhash.go).
package stdhash

import (
	"encoding/hex"
	"hash"
	"io"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

// DigestWriter tees every Write into both an underlying io.Writer and a running
// hash, so a caller can stream bytes to a destination and read the digest of
// everything written so far without a second pass. The digest is public (a
// content ID / cache key) — it is NOT authentication.
type DigestWriter struct {
	// dst receives every byte unchanged; the digest never alters the stream.
	dst io.Writer
	// hsh is the running hash updated in lockstep with dst.
	hsh hash.Hash
}

// NewDigestWriter returns a DigestWriter that tees writes into dst while hashing
// them under the Hasher registered as a. An unregistered algorithm returns
// UnknownHashAlgorithm (blank-import the scheme's package to register it).
func NewDigestWriter(a corecrypto.Algorithm, dst io.Writer) (writer *DigestWriter, err error) {
	//: resolve the streaming hash first so a missing import surfaces the sentinel.
	hsh, lookupErr := corecrypto.NewHash(a)
	//: absence path — the hasher package was never blank-imported.
	if lookupErr != nil {
		//: propagate the typed unknown-algorithm sentinel unchanged.
		return nil, lookupErr
	}
	//: bind dst + the fresh hash; both advance together on every Write.
	return &DigestWriter{dst: dst, hsh: hsh}, nil
}

// Write forwards p to the underlying writer and folds the same bytes into the
// running hash, returning the count the destination accepted.
func (w *DigestWriter) Write(p []byte) (n int, err error) {
	//: the destination is the source of truth for the byte count + error.
	n, err = w.dst.Write(p)
	//: hash exactly the bytes dst accepted so Sum tracks what was emitted.
	if n > 0 {
		//: hash.Hash.Write is contractually error-free; surface it anyway so a
		//: non-conforming hash can never silently drop a fault.
		if _, hErr := w.hsh.Write(p[:n]); hErr != nil {
			//: unreachable for every stdlib hash; propagated, never swallowed.
			return n, hErr
		}
	}
	//: hand back the destination's outcome verbatim.
	return n, err
}

// Sum returns the digest of every byte written so far. Calling it does not reset
// the hash, so subsequent Writes keep extending the same digest.
func (w *DigestWriter) Sum() []byte {
	//: Sum(nil) appends the current digest to a fresh slice.
	return w.hsh.Sum(nil)
}

// SumHex returns Sum rendered as canonical lowercase hex — the frozen string
// form for content IDs and cache keys.
func (w *DigestWriter) SumHex() string {
	//: lowercase hex is the canonical, frozen rendering of the digest.
	return hex.EncodeToString(w.Sum())
}
