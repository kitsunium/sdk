package stdhash

import (
	"encoding/hex"
	"io"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

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

// sum is DigestWriter.Sum's body: decl_gen.go writes DigestWriter.Sum, from the
// design, as one call of it.
func (w *DigestWriter) sum() []byte {
	//: Sum(nil) appends the current digest to a fresh slice.
	return w.hsh.Sum(nil)
}

// SumHex returns Sum rendered as canonical lowercase hex — the frozen string
// form for content IDs and cache keys.
func (w *DigestWriter) SumHex() string {
	//: lowercase hex is the canonical, frozen rendering of the digest.
	return hex.EncodeToString(w.Sum())
}
