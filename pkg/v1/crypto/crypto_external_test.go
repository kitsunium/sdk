package crypto_test

import (
	"bytes"
	"fmt"
	"io"
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

func TestSealStream_RoundTrip(t *testing.T) {
	t.Parallel()
	const chunk int = 64 * 1024
	type tc struct {
		name string
		size int
		aad  []byte
	}
	tests := []tc{
		{"empty stream", 0, nil},
		{"sub-chunk payload", 500, []byte("ctx")},
		{"multi-chunk payload", chunk*2 + 13, []byte("ctx")},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		key := mustKey(t, 0xB)
		//: a deterministic pattern keeps the equality check exact.
		pt := make([]byte, c.size)
		for i := range pt {
			pt[i] = byte(i % 97)
		}
		var dst bytes.Buffer
		//: SealStream activates the stdlib streaming scheme via the blank import.
		w, err := crypto.SealStream(&dst, key, c.aad)
		if err != nil {
			t.Fatalf("%s: SealStream: %v", c.name, err)
		}
		if _, werr := w.Write(pt); werr != nil {
			t.Fatalf("%s: Write: %v", c.name, werr)
		}
		//: Close seals the final chunk; the stream is invalid without it.
		if cerr := w.Close(); cerr != nil {
			t.Fatalf("%s: Close: %v", c.name, cerr)
		}
		r, oerr := crypto.OpenStream(bytes.NewReader(dst.Bytes()), key, c.aad)
		if oerr != nil {
			t.Fatalf("%s: OpenStream: %v", c.name, oerr)
		}
		//: the opened stream must reproduce the plaintext exactly.
		got, rerr := io.ReadAll(r)
		if rerr != nil || !bytes.Equal(got, pt) {
			t.Errorf("%s: round-trip err=%v equal=%v", c.name, rerr, bytes.Equal(got, pt))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestOpenStream_RejectsBox(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"a whole-buffer box (0x01) is rejected by the streaming reader"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		key := mustKey(t, 0xC)
		//: a whole-buffer box has lead byte 0x01; the streaming reader must reject it.
		box, err := crypto.Seal(key, []byte("not a stream"), nil)
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		r, oerr := crypto.OpenStream(bytes.NewReader(box), key, nil)
		if oerr != nil {
			t.Fatalf("OpenStream: %v", oerr)
		}
		//: reading a 0x01 box through the streaming reader must error, not decode.
		if _, rerr := io.ReadAll(r); rerr == nil {
			t.Errorf("OpenStream accepted a whole-buffer box")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestOpen_RejectsStream(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"a streaming box (0x02) is rejected by whole-buffer Open"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		key := mustKey(t, 0xD)
		//: produce a streaming frame (lead byte 0x02).
		var dst bytes.Buffer
		w, err := crypto.SealStream(&dst, key, nil)
		if err != nil {
			t.Fatalf("SealStream: %v", err)
		}
		if _, werr := w.Write([]byte("streamed")); werr != nil {
			t.Fatalf("Write: %v", werr)
		}
		if cerr := w.Close(); cerr != nil {
			t.Fatalf("Close: %v", cerr)
		}
		//: whole-buffer Open must reject the 0x02 stream lead byte.
		if _, oerr := crypto.Open(key, dst.Bytes(), nil); oerr == nil {
			t.Errorf("Open accepted a streaming frame")
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
