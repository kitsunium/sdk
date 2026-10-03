package stdhash

import (
	"bytes"
	"errors"
	"io"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

func Test_verifyingReader_verify(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      []byte
		want    []byte
		wantErr error
	}
	tests := []tc{
		{"matching digest yields io.EOF", []byte("payload"), []byte("payload"), io.EOF},
		{"matching empty digest yields io.EOF", []byte{}, []byte{}, io.EOF},
		{"diverging digest yields DigestMismatch", []byte("payload"), []byte("tampered"), corecrypto.DigestMismatch},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		want, err := corecrypto.SumHex(corecrypto.Algorithm("sha256"), c.want)
		if err != nil {
			t.Fatalf("%s: SumHex setup: %v", c.name, err)
		}
		vr, err := NewVerifyingReader(corecrypto.Algorithm("sha256"), bytes.NewReader(c.in), want)
		if err != nil {
			t.Fatalf("%s: NewVerifyingReader: %v", c.name, err)
		}
		//: drain the running hash exactly as Read does before the EOF check.
		if _, hErr := io.Copy(io.Discard, vr.src); hErr != nil {
			t.Fatalf("%s: drain src: %v", c.name, hErr)
		}
		if _, hErr := vr.hsh.Write(c.in); hErr != nil {
			t.Fatalf("%s: hash write: %v", c.name, hErr)
		}
		//: verify() is the private terminal check exercised directly here.
		if got := vr.verify(); !errors.Is(got, c.wantErr) {
			t.Errorf("%s: verify()=%v want %v", c.name, got, c.wantErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
