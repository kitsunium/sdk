package stdhash

import (
	"encoding/hex"
	"io"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

// NewVerifyingReader returns a VerifyingReader over src that hashes the stream
// under the Hasher registered as a and compares the result to wantHex at EOF. An
// unregistered algorithm returns UnknownHashAlgorithm (blank-import the scheme's
// package to register it).
func NewVerifyingReader(a corecrypto.Algorithm, src io.Reader, wantHex string) (reader *VerifyingReader, err error) {
	//: resolve the streaming hash first so a missing import surfaces the sentinel.
	hsh, lookupErr := corecrypto.NewHash(a)
	//: absence path — the hasher package was never blank-imported.
	if lookupErr != nil {
		//: propagate the typed unknown-algorithm sentinel unchanged.
		return nil, lookupErr
	}
	//: bind src + the fresh hash + the expected digest for the EOF comparison.
	return &VerifyingReader{src: src, hsh: hsh, wantHex: wantHex}, nil
}

// Read proxies the wrapped reader, hashing the bytes it yields. Only when the
// underlying read reports io.EOF does it verify the accumulated digest: a
// mismatch becomes DigestMismatch, a match passes io.EOF through unchanged.
func (r *VerifyingReader) Read(p []byte) (n int, err error) {
	//: pull from the source; n bytes are valid regardless of err.
	n, err = r.src.Read(p)
	//: hash exactly the bytes produced so the digest tracks the real stream.
	if n > 0 {
		//: hash.Hash.Write is contractually error-free; surface it anyway so a
		//: non-conforming hash can never silently drop a fault.
		if _, hErr := r.hsh.Write(p[:n]); hErr != nil {
			//: unreachable for every stdlib hash; propagated, never swallowed.
			return n, hErr
		}
	}
	//: any non-EOF outcome (data or a real fault) passes through untouched.
	if err != io.EOF {
		//: mid-stream reads never surface the verification result.
		return n, err
	}
	//: terminal read — the digest is now complete and can be checked.
	return n, r.verify()
}

// verify compares the computed digest to the expected hex, returning
// DigestMismatch on divergence and io.EOF on a match.
func (r *VerifyingReader) verify() error {
	//: render the completed digest in the same canonical lowercase hex form.
	got := hex.EncodeToString(r.hsh.Sum(nil))
	//: a divergence at the public digest is the typed, non-oracle mismatch.
	if got != r.wantHex {
		//: surface the sentinel at EOF; callers route on it via errs accessors.
		return corecrypto.DigestMismatch
	}
	//: a match ends the stream as a clean EOF, indistinguishable from any reader.
	return io.EOF
}
