package agree_test

import (
	"bytes"
	"reflect"
	"testing"

	agree "github.com/kitsunium/sdk/pkg/v1/agree"
	"github.com/kitsunium/sdk/pkg/v1/crypto"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

func TestAgree_twoPartySharedKey(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		info string
	}
	tests := []tc{
		{"both parties derive the same usable AEAD key", "app-v1"},
		{"a different info still agrees between the pair", "app-v2"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: each party draws an ephemeral keypair.
		pubA, privA, err := agree.GenerateKey(agree.X25519)
		if err != nil {
			t.Fatalf("GenerateKey A: %v", err)
		}
		pubB, privB, err := agree.GenerateKey(agree.X25519)
		if err != nil {
			t.Fatalf("GenerateKey B: %v", err)
		}
		//: A uses its private + B's public; B mirrors with the swap + same info.
		keyA, aerr := agree.SharedKey(agree.X25519, privA, pubB, c.info)
		if aerr != nil {
			t.Fatalf("SharedKey A: %v", aerr)
		}
		keyB, berr := agree.SharedKey(agree.X25519, privB, pubA, c.info)
		if berr != nil {
			t.Fatalf("SharedKey B: %v", berr)
		}
		//: the derived keys must be usable AEAD keys that interoperate: A seals,
		//: B opens — only equal keys can do this round-trip.
		box, serr := crypto.Seal(keyA, []byte("hello peer"), nil)
		if serr != nil {
			t.Fatalf("Seal under keyA: %v", serr)
		}
		got, oerr := crypto.Open(keyB, box, nil)
		if oerr != nil || !bytes.Equal(got, []byte("hello peer")) {
			t.Errorf("Open under keyB=(%q,%v) want (hello peer,nil) — keys disagree", got, oerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestAgree_badPeerKey(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		peerPub []byte
	}
	tests := []tc{
		{"all-zero low-order peer point fails", make([]byte, 32)},
		{"garbage-length peer key fails", []byte{0x1, 0x2}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, priv, err := agree.GenerateKey(agree.X25519)
		if err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}
		//: a low-order/garbage peer key must fail rather than yield a key.
		key, aerr := agree.SharedKey(agree.X25519, priv, c.peerPub, "ctx")
		if aerr == nil {
			t.Errorf("SharedKey accepted a bad peer key (got key %v)", key)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestAgree_unknownAlgorithm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"an unregistered scheme fails GenerateKey"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: a scheme no package registered must not resolve.
			if _, _, err := agree.GenerateKey("p384-not-registered"); err == nil {
				t.Errorf("GenerateKey resolved an unregistered scheme")
			}
		})
	}
}

// TestAlgorithmIsDomainDefinedType_V104 pins the V104 fix: agree.Algorithm is a
// DEFINED type owned by this facade, not a bare alias of the shared
// internal/core/crypto.Algorithm. While Algorithm was an alias, all seven
// crypto-family facades shared one identical Go type, so a hash or signature
// constant fed into an agreement call type-checked and only misrouted at
// runtime. A defined type makes that cross-domain mix a compile error;
// reflection witnesses the change because a defined type reports its own package
// path, whereas an alias reports internal/core/crypto. This test FAILS before
// the alias→defined-type change and PASSES after.
func TestAlgorithmIsDomainDefinedType_V104(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		got  string
		want string
	}
	rt := reflect.TypeFor[agree.Algorithm]()
	tests := []tc{
		//: a defined type reports its declaring package; an alias reports corecrypto's.
		{"package path is this facade", rt.PkgPath(), "github.com/kitsunium/sdk/pkg/v1/agree"},
		//: the defined type names itself Algorithm in this package.
		{"type name is Algorithm", rt.Name(), "Algorithm"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: a mismatch means Algorithm is still an alias of corecrypto.Algorithm.
			if c.got != c.want {
				t.Errorf("agree.Algorithm %s=%q want %q (still an alias?)", c.name, c.got, c.want)
			}
		})
	}
}

// TestKeyLenAndNewKeyExposed_V105 pins the V105 fix: every Key-aliasing facade
// re-exports BOTH NewKey and KeyLen uniformly. Before the fix agree returned a
// Key from SharedKey but exposed no NewKey to round-trip raw bytes and no KeyLen,
// forcing a cross-facade import. This test references agree.KeyLen and round-
// trips agree.NewKey, so it does not compile before the additions and PASSES
// after. The wrong-length rejection routes through the errs API, never .Error()
// string matching.
func TestKeyLenAndNewKeyExposed_V105(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		raw     []byte
		wantErr bool
	}
	tests := []tc{
		{"exactly KeyLen bytes round-trips", bytes.Repeat([]byte{0x1}, agree.KeyLen), false},
		{"a short key is rejected with INVALID_KEY", bytes.Repeat([]byte{0x1}, agree.KeyLen-1), true},
	}
	//: KeyLen is the frozen 256-bit length of the Key SharedKey returns.
	if agree.KeyLen != 32 {
		t.Fatalf("agree.KeyLen=%d want 32", agree.KeyLen)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, err := agree.NewKey(c.raw)
			//: a wrong-length key surfaces the typed INVALID_KEY reason, nothing else.
			if c.wantErr {
				if !errs.HasReason(err, "INVALID_KEY") {
					t.Errorf("%s: NewKey reason=%v want INVALID_KEY", c.name, err)
				}
				return
			}
			//: an exactly-KeyLen slice builds a redacting Key without error.
			if err != nil {
				t.Errorf("%s: NewKey: %v", c.name, err)
			}
		})
	}
}
