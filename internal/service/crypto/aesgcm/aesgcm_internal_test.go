package aesgcm

import (
	"bytes"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func mustKey(t *testing.T, b byte) corecrypto.Key {
	t.Helper()
	k, err := corecrypto.NewKey(bytes.Repeat([]byte{b}, corecrypto.KeyLen))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return k
}

func Test_aesGCM_Algorithm(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want corecrypto.Algorithm
	}
	tests := []tc{{"reports the canonical key", algorithm}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the scheme must report exactly its registry key.
		if got := (aesGCM{}).Algorithm(); got != c.want {
			t.Errorf("%s: Algorithm()=%q want %q", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_aesGCM_ID(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want byte
	}
	tests := []tc{{"reports the frozen wire id", algID}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the wire id must be the frozen 0x01 used for Open dispatch.
		if got := (aesGCM{}).ID(); got != c.want {
			t.Errorf("%s: ID()=%#x want %#x", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_newGCM(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"a 256-bit key builds a usable GCM cipher"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		gcm, err := newGCM(mustKey(t, 0x4).Bytes())
		//: a valid key must yield a non-nil cipher with GCM's 12-byte nonce.
		if err != nil || gcm == nil || gcm.NonceSize() != nonceLen {
			t.Errorf("newGCM=(%v,%v) nonceSize check failed", gcm, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_aesGCM_Seal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		plaintext []byte
		aad       []byte
	}
	tests := []tc{
		{"plaintext, no aad", []byte("hello world"), nil},
		{"plaintext + aad", []byte("hello world"), []byte("ctx:v1")},
		{"empty plaintext", []byte{}, nil},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		key := mustKey(t, 0x5)
		box, err := aesGCM{}.Seal(key, c.plaintext, c.aad)
		//: Seal must succeed and produce the framed box.
		if err != nil {
			t.Fatalf("%s: Seal: %v", c.name, err)
		}
		//: the header must carry the frozen version + this scheme's id, and the
		//: total length must be header + nonce + plaintext + GCM tag.
		if box[0] != corecrypto.Version || box[1] != algID {
			t.Errorf("%s: header=[%#x %#x] want [%#x %#x]", c.name, box[0], box[1], corecrypto.Version, algID)
		}
		if want := headerLen + nonceLen + len(c.plaintext) + gcmTagLen; len(box) != want {
			t.Errorf("%s: len(box)=%d want %d", c.name, len(box), want)
		}
		//: it must round-trip back to the exact plaintext under the same key+aad.
		if got, oerr := (aesGCM{}).Open(key, box, c.aad); oerr != nil || !bytes.Equal(got, c.plaintext) {
			t.Errorf("%s: Open=(%q,%v) want (%q,nil)", c.name, got, oerr, c.plaintext)
		}
		//: a second Seal of the same input must succeed and differ (fresh nonce).
		box2, serr := (aesGCM{}).Seal(key, c.plaintext, c.aad)
		if serr != nil || bytes.Equal(box, box2) {
			t.Errorf("%s: second Seal err=%v or identical box (nonce reused)", c.name, serr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_aesGCM_Open(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		mutate func(box []byte, key, otherKey corecrypto.Key) ([]byte, corecrypto.Key, []byte)
	}
	//: each mutator returns the (possibly altered) box, the key to Open with,
	//: and the aad to Open with — every variant must fail non-oracle.
	tests := []tc{
		{"tampered tag byte", func(box []byte, key, _ corecrypto.Key) ([]byte, corecrypto.Key, []byte) {
			box[len(box)-1] ^= 0xFF
			return box, key, nil
		}},
		{"tampered ciphertext byte", func(box []byte, key, _ corecrypto.Key) ([]byte, corecrypto.Key, []byte) {
			box[headerLen+nonceLen] ^= 0xFF
			return box, key, nil
		}},
		{"wrong key", func(box []byte, _, other corecrypto.Key) ([]byte, corecrypto.Key, []byte) {
			return box, other, nil
		}},
		{"wrong aad", func(box []byte, key, _ corecrypto.Key) ([]byte, corecrypto.Key, []byte) {
			return box, key, []byte("different-aad")
		}},
		{"truncated box", func(box []byte, key, _ corecrypto.Key) ([]byte, corecrypto.Key, []byte) {
			return box[:headerLen], key, nil
		}},
		{"wrong version byte", func(box []byte, key, _ corecrypto.Key) ([]byte, corecrypto.Key, []byte) {
			box[0] ^= 0xFF
			return box, key, nil
		}},
		{"wrong alg id byte", func(box []byte, key, _ corecrypto.Key) ([]byte, corecrypto.Key, []byte) {
			box[1] ^= 0xFF
			return box, key, nil
		}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		key, other := mustKey(t, 0x1), mustKey(t, 0x2)
		box, err := aesGCM{}.Seal(key, []byte("secret payload"), []byte("ctx"))
		if err != nil {
			t.Fatalf("%s: Seal: %v", c.name, err)
		}
		mbox, openKey, openAAD := c.mutate(box, key, other)
		_, oerr := aesGCM{}.Open(openKey, mbox, openAAD)
		//: every failure mode must collapse to the single non-oracle sentinel.
		if !errs.HasCode(oerr, corecrypto.CodeDecryptionFailed) {
			t.Errorf("%s: Open err=%v want DecryptionFailed", c.name, oerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
