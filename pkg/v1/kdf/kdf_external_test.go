package kdf_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
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

// TestAlgorithmIsDomainDefinedType_V104 pins the V104 fix: kdf.Algorithm is a
// DEFINED type owned by this facade, not a bare alias of the shared
// internal/core/crypto.Algorithm. While Algorithm was an alias, all seven
// crypto-family facades shared one identical Go type, so a hash or MAC constant
// fed into a KDF call type-checked and only misrouted at runtime. A defined type
// makes that cross-domain mix a compile error; reflection witnesses the change
// because a defined type reports its own package path, whereas an alias reports
// internal/core/crypto. This test FAILS before the alias→defined-type change and
// PASSES after.
func TestAlgorithmIsDomainDefinedType_V104(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		got  string
		want string
	}
	rt := reflect.TypeFor[kdf.Algorithm]()
	tests := []tc{
		//: a defined type reports its declaring package; an alias reports corecrypto's.
		{"package path is this facade", rt.PkgPath(), "github.com/kitsunium/sdk/pkg/v1/kdf"},
		//: the defined type names itself Algorithm in this package.
		{"type name is Algorithm", rt.Name(), "Algorithm"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: a mismatch means Algorithm is still an alias of corecrypto.Algorithm.
			if c.got != c.want {
				t.Errorf("kdf.Algorithm %s=%q want %q (still an alias?)", c.name, c.got, c.want)
			}
		})
	}
}

// TestKeyLenAndNewKeyExposed_V105 pins the V105 fix: every Key-aliasing facade
// re-exports BOTH NewKey and KeyLen uniformly. Before the fix kdf exposed neither
// NewKey nor KeyLen, so a kdf consumer building the master a KeyTree derives from
// had to import the unrelated crypto facade. This test references kdf.KeyLen,
// round-trips kdf.NewKey, and feeds the result to NewKeyTree, so it does not
// compile before the additions and PASSES after. The wrong-length rejection
// routes through the errs API, never .Error() string matching.
func TestKeyLenAndNewKeyExposed_V105(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		raw     []byte
		wantErr bool
	}
	tests := []tc{
		{"exactly KeyLen bytes builds a usable master", bytes.Repeat([]byte{0x1}, kdf.KeyLen), false},
		{"a short key is rejected with INVALID_KEY", bytes.Repeat([]byte{0x1}, kdf.KeyLen-1), true},
	}
	//: KeyLen is the frozen 256-bit master-key length the KeyTree derives from.
	if kdf.KeyLen != 32 {
		t.Fatalf("kdf.KeyLen=%d want 32", kdf.KeyLen)
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		master, err := kdf.NewKey(c.raw)
		//: a wrong-length key surfaces the typed INVALID_KEY reason, nothing else.
		if c.wantErr {
			if !errs.HasReason(err, "INVALID_KEY") {
				t.Errorf("%s: NewKey reason=%v want INVALID_KEY", c.name, err)
			}
			return
		}
		//: the locally-built master must drive a KeyTree without an unrelated import.
		if err != nil {
			t.Fatalf("%s: NewKey: %v", c.name, err)
		}
		if _, derr := kdf.NewKeyTree(kdf.HKDFSHA256, master).Child("svc").DeriveKey(); derr != nil {
			t.Errorf("%s: DeriveKey from locally-built master: %v", c.name, derr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
