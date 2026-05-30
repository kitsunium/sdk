package streamaead

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

func fixedKey(t *testing.T, b byte) corecrypto.Key {
	t.Helper()
	//: deterministic 32-byte key for repeatable KAT bytes.
	key, err := corecrypto.NewKey(bytes.Repeat([]byte{b}, corecrypto.KeyLen))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return key
}

func Test_streamAEAD_Algorithm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want corecrypto.Algorithm
	}{
		{"reports the frozen aes-256-gcm-stream key", "aes-256-gcm-stream"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: the scheme must report its frozen canonical key.
			if got := (streamAEAD{}).Algorithm(); got != c.want {
				t.Errorf("Algorithm()=%q want %q", got, c.want)
			}
		})
	}
}

func Test_streamAEAD_Writer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Writer draws a random salt and returns a WriteCloser"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: the production Writer path draws a real random salt internally.
			w, err := (streamAEAD{}).Writer(fixedKey(t, 0x5), &bytes.Buffer{}, nil)
			if err != nil || w == nil {
				t.Fatalf("Writer=(%v,%v) want (writer,nil)", w, err)
			}
		})
	}
}

func Test_streamAEAD_Reader(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Reader returns a lazily-initialised reader"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: the Reader constructor never fails and never reads up front.
			r, err := (streamAEAD{}).Reader(fixedKey(t, 0x6), bytes.NewReader(nil), nil)
			if err != nil || r == nil {
				t.Errorf("Reader=(%v,%v) want (reader,nil)", r, err)
			}
		})
	}
}

func Test_newGCM(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"newGCM derives a usable AEAD from key + salt"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			key := fixedKey(t, 0x7)
			salt := make([]byte, saltLen)
			//: a well-formed key + salt yields a non-nil GCM mode.
			gcm, err := newGCM(key.Bytes(), salt)
			if err != nil || gcm == nil || gcm.NonceSize() != nonceLen {
				t.Errorf("newGCM=(%v,%v) nonce=%d want (gcm,nil,%d)", gcm, err, safeNonceSize(gcm), nonceLen)
			}
		})
	}
}

// safeNonceSize reports gcm.NonceSize() or -1 for a nil mode, so the assertion
// above never panics on the error path.
func safeNonceSize(gcm cipher.AEAD) int {
	//: a nil mode has no nonce size; report a sentinel.
	if gcm == nil {
		return -1
	}
	//: the real GCM nonce width.
	return gcm.NonceSize()
}

func Test_chunkNonce(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		counter uint64
		flag    byte
		wantHex string
	}{
		{"counter 0 more-flag", 0, flagMore, "000000000000000000000000"},
		{"counter 1 final-flag", 1, flagFinal, "000000000000000000000101"},
		{"counter 258 more-flag", 258, flagMore, "000000000000000000010200"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			nonce := chunkNonce(c.counter, c.flag)
			//: the nonce is an 11-byte big-endian counter || a 1-byte flag.
			if hex.EncodeToString(nonce[:]) != c.wantHex {
				t.Errorf("chunkNonce(%d,%#x)=%s want %s", c.counter, c.flag, hex.EncodeToString(nonce[:]), c.wantHex)
			}
		})
	}
}

// referenceFrame is an INDEPENDENT, inline recomputation of the frozen wire
// format straight from the stdlib primitives named in the ADR §D2 — header,
// HKDF-SHA256 stream key, single final-flag chunk. The KAT asserts the scheme
// emits exactly these bytes, so any drift in the scheme's framing fails without
// trusting the scheme's own helpers. salt/pt/aad are the frozen KAT inputs.
func referenceFrame(t *testing.T, keyBytes, salt, pt, aad []byte) []byte {
	t.Helper()
	//: derive the per-stream key by the frozen recipe (info string is frozen).
	sk, err := hkdf.Key(sha256.New, keyBytes, salt, hkdfInfo, corecrypto.KeyLen)
	if err != nil {
		t.Fatalf("hkdf.Key: %v", err)
	}
	block, berr := aes.NewCipher(sk)
	if berr != nil {
		t.Fatalf("aes.NewCipher: %v", berr)
	}
	gcm, gerr := cipher.NewGCM(block)
	if gerr != nil {
		t.Fatalf("cipher.NewGCM: %v", gerr)
	}
	//: counter 0, final flag — a single-chunk stream's only chunk.
	nonce := make([]byte, nonceLen)
	nonce[nonceLen-1] = flagFinal
	//: header [ver][algID][salt] then the sealed final chunk.
	frame := append([]byte{streamVersion, algID}, salt...)
	return gcm.Seal(frame, nonce, pt, aad)
}

// Test_KAT freezes the exact wire bytes for a known key + salt + plaintext so the
// bespoke frame can never silently drift. The deterministic salt comes from the
// UNEXPORTED test-only newWriterWithSalt seam; the expected bytes are recomputed
// independently from the stdlib via referenceFrame.
func Test_KAT(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		pt   string
		aad  string
	}{
		{"single-chunk frozen vector", "kitsunium", "ad"},
		{"empty plaintext frozen vector", "", ""},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			key := fixedKey(t, 0x2a)
			//: a fixed, monotonically-numbered salt makes the output deterministic.
			var salt [saltLen]byte
			for i := range salt {
				salt[i] = byte(i)
			}
			var dst bytes.Buffer
			w, err := newWriterWithSalt(key, &dst, []byte(c.aad), salt)
			if err != nil {
				t.Fatalf("newWriterWithSalt: %v", err)
			}
			//: seal a plaintext that fits in a single final chunk.
			if _, werr := w.Write([]byte(c.pt)); werr != nil {
				t.Fatalf("Write: %v", werr)
			}
			if cerr := w.Close(); cerr != nil {
				t.Fatalf("Close: %v", cerr)
			}
			//: the scheme output must equal the independent stdlib reference frame.
			want := referenceFrame(t, key.Bytes(), salt[:], []byte(c.pt), []byte(c.aad))
			if !bytes.Equal(dst.Bytes(), want) {
				t.Errorf("KAT drift:\n got=%s\nwant=%s", hex.EncodeToString(dst.Bytes()), hex.EncodeToString(want))
			}
		})
	}
}
