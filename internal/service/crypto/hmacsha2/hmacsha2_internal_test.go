package hmacsha2

import (
	"bytes"
	"encoding/hex"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

// runTagKATCase asserts Tag over a KeyLen-padded key + msg reproduces the exact
// frozen hex tag, so any drift in the construction breaks the build.
func runTagKATCase(t *testing.T, rawKey, msg []byte, wantHex string) {
	t.Helper()
	//: pad the vector key into a KeyLen buffer (the redacting Key pins length).
	buf := make([]byte, corecrypto.KeyLen)
	//: leading bytes are the vector key; the remainder stays zero.
	copy(buf, rawKey)
	//: a KeyLen buffer always constructs a Key.
	key, err := corecrypto.NewKey(buf)
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	//: compare the hex tag against the frozen known answer.
	if got := hex.EncodeToString((hmacSHA256{}).Tag(key, msg)); got != wantHex {
		t.Fatalf("Tag hex = %s, want %s", got, wantHex)
	}
}

// Test_hmacSHA256_TagKAT pins the frozen HMAC-SHA256 output so a silent change to
// the wire-visible construction (e.g. a hash swap) fails the build rather than
// breaking external interop. Keys are zero-padded to KeyLen (the SDK Key pins
// length), so these vectors freeze THAT documented behaviour.
func Test_hmacSHA256_TagKAT(t *testing.T) {
	t.Parallel()
	//: known-answer vectors over KeyLen-padded keys.
	tests := []struct {
		name    string
		key     []byte
		msg     []byte
		wantHex string
	}{
		{"padded-0b-hi-there", bytes.Repeat([]byte{0x0b}, 20), []byte("Hi There"), "b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7"},
		{"jefe", []byte("Jefe"), []byte("what do ya want for nothing?"), "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843"},
	}
	//: drive every vector through the shared KAT runner.
	for _, c := range tests {
		//: each vector is independent and parallel-safe.
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: delegate to the shared KAT runner.
			runTagKATCase(t, c.key, c.msg, c.wantHex)
		})
	}
}

func newKey(t *testing.T, b byte) corecrypto.Key {
	t.Helper()
	//: a fixed-byte 32-byte key keeps the test deterministic.
	key, err := corecrypto.NewKey(bytes.Repeat([]byte{b}, corecrypto.KeyLen))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return key
}

func Test_hmacSHA256_Algorithm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want corecrypto.Algorithm
	}{
		{"reports the frozen hmac-sha256 key", "hmac-sha256"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: the scheme must report its frozen canonical key.
			if got := (hmacSHA256{}).Algorithm(); got != c.want {
				t.Errorf("Algorithm()=%q want %q", got, c.want)
			}
		})
	}
}

func Test_hmacSHA256_Tag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		message string
	}{
		{"empty message yields a 32-byte tag", ""},
		{"short message yields a 32-byte tag", "hello"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: a SHA-256 tag is always 32 bytes wide.
			if tag := (hmacSHA256{}).Tag(newKey(t, 0x7), []byte(c.message)); len(tag) != 32 {
				t.Errorf("Tag len=%d want 32", len(tag))
			}
		})
	}
}

func Test_hmacSHA256_Verify(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		tamper bool
		want   bool
	}{
		{"a genuine tag verifies", false, true},
		{"a tampered tag fails", true, false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := hmacSHA256{}
			key := newKey(t, 0x7)
			msg := []byte("authenticate me")
			tag := h.Tag(key, msg)
			//: optionally corrupt a tag byte to drive the negative path.
			if c.tamper {
				tag[0] ^= 0xff
			}
			//: Verify is constant-time and reports the genuine/tampered verdict.
			if got := h.Verify(key, msg, tag); got != c.want {
				t.Errorf("Verify=%v want %v", got, c.want)
			}
		})
	}
}

func Test_hmacSHA256_New(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		message string
	}{
		{"streaming New equals one-shot Tag", "incremental payload"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := hmacSHA256{}
			key := newKey(t, 0x3)
			//: drive the streaming hash and the one-shot Tag over the same input.
			mac := h.New(key)
			mac.Write([]byte(c.message))
			//: both paths must yield the identical HMAC tag.
			if !bytes.Equal(mac.Sum(nil), h.Tag(key, []byte(c.message))) {
				t.Errorf("streaming New tag != one-shot Tag")
			}
		})
	}
}
