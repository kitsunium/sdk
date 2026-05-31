package stdhash_test

import (
	"bytes"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/service/crypto/stdhash"
)

// dwSumHex computes the canonical SHA-256 hex of in, failing the test on error.
func dwSumHex(t *testing.T, in []byte) string {
	t.Helper()
	got, err := corecrypto.SumHex(corecrypto.Algorithm("sha256"), in)
	if err != nil {
		t.Fatalf("SumHex setup: %v", err)
	}
	return got
}

func TestNewDigestWriter(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		alg     corecrypto.Algorithm
		wantErr bool
	}
	tests := []tc{
		{"registered algorithm constructs a writer", "sha256", false},
		{"unregistered algorithm errors", "no-such-hash", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, err := stdhash.NewDigestWriter(c.alg, &bytes.Buffer{})
		//: the failure arm must surface UnknownHashAlgorithm and no writer.
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: NewDigestWriter accepted an unregistered algorithm", c.name)
			}
			return
		}
		//: the success arm must construct without error.
		if err != nil {
			t.Errorf("%s: NewDigestWriter: %v", c.name, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_DigestWriter_Write exercises Write across input sizes: every byte must
// reach dst unaltered and the reported count must equal the input length.
func Test_DigestWriter_Write(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   []byte
	}
	tests := []tc{
		{"empty input digests the empty hash", []byte{}},
		{"single chunk tees and digests", []byte("payload")},
		{"large content addresses", bytes.Repeat([]byte("kitsunium"), 64)},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var dst bytes.Buffer
		w, err := stdhash.NewDigestWriter(corecrypto.Algorithm("sha256"), &dst)
		if err != nil {
			t.Fatalf("%s: NewDigestWriter: %v", c.name, err)
		}
		n, werr := w.Write(c.in)
		//: every byte must reach dst and the count must match the input.
		if werr != nil || n != len(c.in) {
			t.Fatalf("%s: Write=(%d,%v) want (%d,nil)", c.name, n, werr, len(c.in))
		}
		//: dst must have received exactly the bytes written, unaltered.
		if !bytes.Equal(dst.Bytes(), c.in) {
			t.Errorf("%s: dst=%q want %q", c.name, dst.Bytes(), c.in)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_DigestWriter_Sum checks Sum equals the one-shot raw digest of the bytes
// written, across input sizes.
func Test_DigestWriter_Sum(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   []byte
	}
	tests := []tc{
		{"empty input", []byte{}},
		{"content", []byte("payload")},
		{"large", bytes.Repeat([]byte("kitsunium"), 64)},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var dst bytes.Buffer
		w, err := stdhash.NewDigestWriter(corecrypto.Algorithm("sha256"), &dst)
		if err != nil {
			t.Fatalf("%s: NewDigestWriter: %v", c.name, err)
		}
		if _, werr := w.Write(c.in); werr != nil {
			t.Fatalf("%s: Write: %v", c.name, werr)
		}
		raw, sumErr := corecrypto.Sum(corecrypto.Algorithm("sha256"), c.in)
		//: the running Sum must equal the one-shot Sum of the same bytes.
		if sumErr != nil || !bytes.Equal(w.Sum(), raw) {
			t.Errorf("%s: Sum=%x want %x (err=%v)", c.name, w.Sum(), raw, sumErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_DigestWriter_SumHex checks SumHex equals the canonical one-shot SumHex of
// the bytes written, across input sizes.
func Test_DigestWriter_SumHex(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   []byte
	}
	tests := []tc{
		{"empty input", []byte{}},
		{"content", []byte("payload")},
		{"large", bytes.Repeat([]byte("kitsunium"), 64)},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var dst bytes.Buffer
		w, err := stdhash.NewDigestWriter(corecrypto.Algorithm("sha256"), &dst)
		if err != nil {
			t.Fatalf("%s: NewDigestWriter: %v", c.name, err)
		}
		if _, werr := w.Write(c.in); werr != nil {
			t.Fatalf("%s: Write: %v", c.name, werr)
		}
		//: SumHex must equal the canonical one-shot SumHex of the same bytes.
		if got := w.SumHex(); got != dwSumHex(t, c.in) {
			t.Errorf("%s: SumHex=%q want %q", c.name, got, dwSumHex(t, c.in))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
