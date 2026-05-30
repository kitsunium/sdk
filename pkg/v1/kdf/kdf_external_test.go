package kdf_test

import (
	"bytes"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/kdf"
)

func TestSubkey(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		alg     kdf.Algorithm
		length  int
		wantErr bool
	}
	tests := []tc{
		{"hkdf-sha256 derives the requested length", kdf.HKDFSHA256, 32, false},
		{"unknown algorithm errors", "no-such-kdf", 32, true},
		//: HKDF-SHA256 cannot exceed 255*32 = 8160 bytes.
		{"over-long length errors", kdf.HKDFSHA256, 255*32 + 1, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := kdf.Subkey(c.alg, []byte("master-secret"), []byte("salt"), "purpose", c.length)
		//: the failure arms must error and yield no subkey.
		if c.wantErr {
			if err == nil || got != nil {
				t.Errorf("%s: Subkey accepted a bad request (got %d bytes, err %v)", c.name, len(got), err)
			}
			return
		}
		//: the success arm returns exactly length bytes, deterministically.
		if err != nil || len(got) != c.length {
			t.Fatalf("%s: Subkey=(%d bytes,%v) want (%d,nil)", c.name, len(got), err, c.length)
		}
		again, againErr := kdf.Subkey(c.alg, []byte("master-secret"), []byte("salt"), "purpose", c.length)
		//: the second derivation must also succeed and match — HKDF is deterministic.
		if againErr != nil || !bytes.Equal(got, again) {
			t.Errorf("%s: Subkey is not deterministic (err=%v)", c.name, againErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestSubkeySeparation(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		info string
	}
	tests := []tc{
		{"the aead-key purpose", "aead-key"},
		{"the mac-key purpose", "mac-key"},
	}
	//: a shared baseline derived under a fixed reference label.
	ref, err := kdf.Subkey(kdf.HKDFSHA256, []byte("master"), []byte("salt"), "reference", 32)
	if err != nil {
		t.Fatalf("reference Subkey: %v", err)
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := kdf.Subkey(kdf.HKDFSHA256, []byte("master"), []byte("salt"), c.info, 32)
		//: a distinct info label must produce a key independent of the baseline.
		if err != nil || bytes.Equal(got, ref) {
			t.Errorf("%s: subkey collided with the reference label (err %v)", c.name, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
