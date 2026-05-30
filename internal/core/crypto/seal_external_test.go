package crypto_test

import (
	"bytes"
	"testing"

	"github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func testKey(t *testing.T) crypto.Key {
	t.Helper()
	k, err := crypto.NewKey(bytes.Repeat([]byte{0x5}, crypto.KeyLen))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return k
}

func TestSeal(t *testing.T) {
	//: sequential — seeds + reads the process-wide registry.
	crypto.ResetForTest()
	crypto.Register(fakeAEAD{name: "seal-fake", id: 0x50})
	type tc struct {
		name string
		alg  crypto.Algorithm
		want []byte
	}
	tests := []tc{
		{"round-trips through the resolved scheme", "seal-fake", []byte("payload")},
		{"empty plaintext round-trips", "seal-fake", []byte{}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		key := testKey(t)
		box, err := crypto.Seal(c.alg, key, c.want, nil)
		//: Seal must succeed for a registered algorithm.
		if err != nil {
			t.Fatalf("%s: Seal: %v", c.name, err)
		}
		got, oerr := crypto.Open(key, box, nil)
		//: Open must recover the exact plaintext.
		if oerr != nil || !bytes.Equal(got, c.want) {
			t.Errorf("%s: Open=(%q,%v) want (%q,nil)", c.name, got, oerr, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestSeal_UnknownAlgorithm(t *testing.T) {
	//: sequential — reads the process-wide registry.
	crypto.ResetForTest()
	type tc struct {
		name string
		alg  crypto.Algorithm
	}
	tests := []tc{{"unregistered algorithm is rejected", "ghost-scheme"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, err := crypto.Seal(c.alg, testKey(t), []byte("x"), nil)
		//: a missing scheme surfaces the typed UnknownAlgorithm sentinel.
		if !errs.HasCode(err, crypto.CodeUnknownAlgorithm) {
			t.Errorf("%s: err=%v want UnknownAlgorithm", c.name, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestOpen(t *testing.T) {
	//: sequential — seeds + reads the process-wide registry.
	crypto.ResetForTest()
	crypto.Register(fakeAEAD{name: "open-fake", id: 0x60})
	type tc struct {
		name string
		box  []byte
	}
	tests := []tc{
		{"box too short to dispatch", []byte{crypto.Version}},
		{"unknown wire id", []byte{crypto.Version, 0xEE, 0x01, 0x02}},
		{"wrong version byte", []byte{0xFF, 0x60, 0x01}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, err := crypto.Open(testKey(t), c.box, nil)
		//: every malformed/foreign box yields the single non-oracle sentinel.
		if !errs.HasCode(err, crypto.CodeDecryptionFailed) {
			t.Errorf("%s: err=%v want DecryptionFailed", c.name, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
