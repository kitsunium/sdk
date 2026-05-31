package stdhash_test

import (
	"bytes"
	"io"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/crypto/stdhash"
)

// vrSumHex computes the canonical SHA-256 hex of in, failing the test on error.
func vrSumHex(t *testing.T, in []byte) string {
	t.Helper()
	got, err := corecrypto.SumHex(corecrypto.Algorithm("sha256"), in)
	if err != nil {
		t.Fatalf("SumHex setup: %v", err)
	}
	return got
}

func TestNewVerifyingReader(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		alg     corecrypto.Algorithm
		wantErr bool
	}
	tests := []tc{
		{"registered algorithm constructs a reader", "sha256", false},
		{"unregistered algorithm errors", "no-such-hash", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, err := stdhash.NewVerifyingReader(c.alg, bytes.NewReader(nil), "")
		//: the failure arm must surface UnknownHashAlgorithm and no reader.
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: NewVerifyingReader accepted an unregistered algorithm", c.name)
			}
			return
		}
		//: the success arm must construct without error.
		if err != nil {
			t.Errorf("%s: NewVerifyingReader: %v", c.name, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_VerifyingReader_Read tests Read over matching and tampered streams: a
// match drains clean (the terminal EOF passes through io.Copy as nil), a
// tampered stream surfaces DigestMismatch at EOF. The mid-stream-only guarantee
// is covered by TestVerifyingReaderMismatchOnlyAtEOF.
func Test_VerifyingReader_Read(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      []byte
		tamper  bool
		wantErr bool
	}
	tests := []tc{
		{"matching digest over empty input is a clean EOF", []byte{}, false, false},
		{"matching digest over content is a clean EOF", []byte("payload"), false, false},
		{"matching digest over a large stream is a clean EOF", bytes.Repeat([]byte("k"), 4096), false, false},
		{"tampered stream fails at EOF with DigestMismatch", []byte("payload"), true, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		want := vrSumHex(t, c.in)
		//: a tampered case feeds a digest that cannot match the actual stream.
		if c.tamper {
			want = vrSumHex(t, append([]byte("x"), c.in...))
		}
		vr, err := stdhash.NewVerifyingReader(corecrypto.Algorithm("sha256"), bytes.NewReader(c.in), want)
		if err != nil {
			t.Fatalf("%s: NewVerifyingReader: %v", c.name, err)
		}
		var dst bytes.Buffer
		_, copyErr := io.Copy(&dst, vr)
		//: the mismatch arm must surface DigestMismatch (io.Copy hides a clean EOF).
		if c.wantErr {
			if !errs.HasCode(copyErr, corecrypto.CodeDigestMismatch) {
				t.Errorf("%s: io.Copy err=%v want DigestMismatch", c.name, copyErr)
			}
			return
		}
		//: the match arm must complete with no error and the bytes intact.
		if copyErr != nil || !bytes.Equal(dst.Bytes(), c.in) {
			t.Errorf("%s: io.Copy err=%v dst=%q want nil/%q", c.name, copyErr, dst.Bytes(), c.in)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestVerifyingReaderMismatchOnlyAtEOF(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   []byte
	}
	tests := []tc{{"a short mid-stream read returns no verification error", []byte("payload-data")}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: a deliberately wrong digest so verification WOULD fail at EOF.
		vr, err := stdhash.NewVerifyingReader(corecrypto.Algorithm("sha256"), bytes.NewReader(c.in), vrSumHex(t, []byte("other")))
		if err != nil {
			t.Fatalf("%s: NewVerifyingReader: %v", c.name, err)
		}
		//: a small buffer forces a read that returns data WITHOUT reaching EOF.
		buf := make([]byte, 4)
		n, readErr := vr.Read(buf)
		//: the mid-stream read must yield data and NOT surface the mismatch.
		if readErr != nil || n != len(buf) {
			t.Fatalf("%s: mid-stream Read=(%d,%v) want (%d,nil)", c.name, n, readErr, len(buf))
		}
		//: draining the rest must now surface the mismatch at the terminal read.
		_, drainErr := io.Copy(io.Discard, vr)
		if !errs.HasCode(drainErr, corecrypto.CodeDigestMismatch) {
			t.Errorf("%s: drain err=%v want DigestMismatch", c.name, drainErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
