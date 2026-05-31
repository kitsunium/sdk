package keyenvelope_test

import (
	"encoding/base64"
	"strings"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/crypto/keyenvelope"
)

// dek32 returns a deterministic 32-byte DEK for round-trip tests.
func dek32(tb testing.TB) corecrypto.Key {
	tb.Helper()
	raw := make([]byte, corecrypto.KeyLen)
	//: fill with a recognizable, non-zero pattern
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	key, err := corecrypto.NewKey(raw)
	//: the fixture key must construct cleanly
	if err != nil {
		tb.Fatalf("NewKey: %v", err)
	}
	return key
}

// Test_WrapKey exercises wrap then unwrap round-trips under the right passphrase.
func Test_WrapKey(t *testing.T) {
	t.Parallel()
	//: table-driven cases keep arms isolated
	cases := []struct {
		name string
		pass []byte
	}{
		{name: "ascii", pass: []byte("correct horse battery staple")},
		{name: "empty", pass: nil},
	}
	check := func(t *testing.T, pass []byte) {
		t.Helper()
		dek := dek32(t)
		env, err := keyenvelope.WrapKey(pass, dek)
		//: wrapping a valid DEK must succeed
		if err != nil {
			t.Fatalf("wrap: %v", err)
		}
		//: the envelope must carry the frozen header prefix
		if !strings.HasPrefix(env, "$kenv$v=1$kdf=pbkdf2-sha256$") {
			t.Fatalf("bad prefix: %q", env)
		}
		out, uerr := keyenvelope.UnwrapKey(pass, env)
		//: unwrapping with the same passphrase must succeed
		if uerr != nil {
			t.Fatalf("unwrap: %v", uerr)
		}
		//: the recovered DEK must equal the original
		if string(out.Bytes()) != string(dek.Bytes()) {
			t.Fatalf("dek mismatch")
		}
	}
	//: iterate cases under a parallel parent
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			check(t, tc.pass)
		})
	}
}

// Test_UnwrapKey asserts the failure shapes of UnwrapKey. A wrong passphrase
// and the AAD-bound salt swap both fail inside the AEAD open as
// DecryptionFailed; a structurally short box also surfaces DecryptionFailed
// (parseEnvelope defers the length check to the AEAD). The corrupt and
// swapped-kdf cases are rejected earlier by framing validation as
// InvalidKeyEnvelope. The salt swap is the load-bearing case: it keeps the
// framing valid yet changes the only per-envelope AAD field, proving the salt
// is genuinely bound into the AEAD rather than merely framed.
func Test_UnwrapKey(t *testing.T) {
	t.Parallel()
	dek := dek32(t)
	base, err := keyenvelope.WrapKey([]byte("right"), dek)
	//: a valid envelope is the fixture for the failure cases
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	parts := strings.Split(base, "$")
	//: the fixture envelope must carry the frozen seven-segment framing
	if len(parts) != 7 {
		t.Fatalf("framing: got %d segments want 7", len(parts))
	}
	//: a different but structurally-valid 32-byte salt keeps framing valid yet
	//: changes the per-envelope AAD field, so the open fails inside the AEAD.
	saltSwap := strings.Split(base, "$")
	saltSwap[4] = base64.RawStdEncoding.EncodeToString([]byte("fedcba9876543210fedcba9876543210"))
	//: a valid-framing envelope whose box ("Ym94" -> "box", 3 bytes) is shorter
	//: than the AES-GCM nonce+tag must surface the fault from the AEAD open.
	shortBox := strings.Split(base, "$")
	shortBox[6] = "Ym94"
	//: table-driven cases keep arms isolated; wantCode is the single discriminator
	cases := []struct {
		name     string
		pass     []byte
		envelope string
		wantCode errs.Code
	}{
		{name: "wrong-pass", pass: []byte("wrong"), envelope: base, wantCode: corecrypto.CodeDecryptionFailed},
		{name: "corrupt", pass: []byte("right"), envelope: "$kenv$bad", wantCode: corecrypto.CodeInvalidKeyEnvelope},
		{name: "tampered-kdf", pass: []byte("right"), envelope: strings.Replace(base, "pbkdf2-sha256", "scrypt", 1), wantCode: corecrypto.CodeInvalidKeyEnvelope},
		{name: "salt-swap", pass: []byte("right"), envelope: strings.Join(saltSwap, "$"), wantCode: corecrypto.CodeDecryptionFailed},
		{name: "short-box", pass: []byte("right"), envelope: strings.Join(shortBox, "$"), wantCode: corecrypto.CodeDecryptionFailed},
	}
	check := func(t *testing.T, pass []byte, envelope string, wantCode errs.Code) {
		t.Helper()
		_, uerr := keyenvelope.UnwrapKey(pass, envelope)
		//: the failure must carry the expected core sentinel code
		if !errs.HasCode(uerr, wantCode) {
			t.Fatalf("err = %v want code %v", uerr, wantCode)
		}
	}
	//: iterate cases under a parallel parent
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			check(t, tc.pass, tc.envelope, tc.wantCode)
		})
	}
}
