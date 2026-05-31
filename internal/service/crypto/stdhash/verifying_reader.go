// Package stdhash — VerifyingReader streaming content-address ergonomics (see
// the package doc in stdhash.go).
package stdhash

import (
	"encoding/hex"
	"hash"
	"io"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

// VerifyingReader wraps a source reader and folds every byte read into a running
// hash. The digest is compared against the expected hex ONLY on the terminal
// read (the one returning io.EOF): on a match it surfaces io.EOF unchanged; on a
// mismatch it returns DigestMismatch instead. The check never fires mid-stream,
// so a partial read can never leak the verification outcome, and the digest is
// public so the comparison is non-oracle.
type VerifyingReader struct {
	// src is the wrapped source; its EOF is the trigger for verification.
	src io.Reader
	// hsh is the running hash advanced in lockstep with every Read.
	hsh hash.Hash
	// wantHex is the expected canonical lowercase hex digest.
	wantHex string
}

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
