package crypto_test

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/crypto"
)

func mustKey(t *testing.T, b byte) crypto.Key {
	t.Helper()
	k, err := crypto.NewKey(bytes.Repeat([]byte{b}, crypto.KeyLen))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return k
}

func TestSealOpen_RoundTrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		plaintext []byte
		aad       []byte
	}
	tests := []tc{
		{"payload, no aad", []byte("order placed"), nil},
		{"payload + aad binds context", []byte("order placed"), []byte("orders:42")},
		{"empty plaintext", []byte{}, nil},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		key := mustKey(t, 0xA)
		//: the default AES-256-GCM scheme is active via the package blank import.
		box, err := crypto.Seal(key, c.plaintext, c.aad)
		if err != nil {
			t.Fatalf("%s: Seal: %v", c.name, err)
		}
		got, oerr := crypto.Open(key, box, c.aad)
		//: Open recovers the exact plaintext under the same key + aad.
		if oerr != nil || !bytes.Equal(got, c.plaintext) {
			t.Errorf("%s: Open=(%q,%v) want (%q,nil)", c.name, got, oerr, c.plaintext)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestOpen_Rejects(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		mutate func(box []byte, key, other crypto.Key) ([]byte, crypto.Key, []byte)
	}
	tests := []tc{
		{"tampered box", func(box []byte, key, _ crypto.Key) ([]byte, crypto.Key, []byte) {
			box[len(box)-1] ^= 0xFF
			return box, key, nil
		}},
		{"wrong key", func(box []byte, _, other crypto.Key) ([]byte, crypto.Key, []byte) {
			return box, other, nil
		}},
		{"wrong aad", func(box []byte, key, _ crypto.Key) ([]byte, crypto.Key, []byte) {
			return box, key, []byte("tampered-ctx")
		}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		key, other := mustKey(t, 0x1), mustKey(t, 0x2)
		box, err := crypto.Seal(key, []byte("secret"), []byte("ctx"))
		if err != nil {
			t.Fatalf("%s: Seal: %v", c.name, err)
		}
		mbox, openKey, openAAD := c.mutate(box, key, other)
		//: any tamper/mismatch must fail (the error is non-oracle by contract).
		if _, oerr := crypto.Open(openKey, mbox, openAAD); oerr == nil {
			t.Errorf("%s: Open succeeded on a tampered/mismatched box", c.name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestNewKey_RejectsBadLength(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		raw  []byte
	}
	tests := []tc{
		{"too short", bytes.Repeat([]byte{0x1}, crypto.KeyLen-1)},
		{"too long", bytes.Repeat([]byte{0x1}, crypto.KeyLen+1)},
		{"nil", nil},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: a wrong-length key must be rejected, never truncated or padded.
		if _, err := crypto.NewKey(c.raw); err == nil {
			t.Errorf("%s: NewKey accepted a %d-byte key", c.name, len(c.raw))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestSealAs_UnknownAlgorithm(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		alg  crypto.Algorithm
	}
	tests := []tc{{"unimported scheme is rejected", "xchacha20poly1305"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: a scheme whose package was never imported must not resolve.
		if _, err := crypto.SealAs(c.alg, mustKey(t, 0x3), []byte("x"), nil); err == nil {
			t.Errorf("%s: SealAs resolved an unregistered algorithm", c.name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestKey_Redacts(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		render func(crypto.Key) string
	}
	tests := []tc{
		{"%v", func(k crypto.Key) string { return fmt.Sprintf("%v", k) }},
		{"%#v", func(k crypto.Key) string { return fmt.Sprintf("%#v", k) }},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the public Key alias must redact through the facade too.
		if got := c.render(mustKey(t, 0xF)); got != "<redacted>" {
			t.Errorf("%s: got %q want <redacted>", c.name, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
