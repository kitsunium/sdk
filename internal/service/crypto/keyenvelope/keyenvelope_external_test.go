package keyenvelope_test

import (
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

// Test_UnwrapKey asserts wrong passphrase and corrupt envelope error shapes,
// including an AAD-bound header tamper that surfaces as a structural fault.
func Test_UnwrapKey(t *testing.T) {
	t.Parallel()
	dek := dek32(t)
	base, err := keyenvelope.WrapKey([]byte("right"), dek)
	//: a valid envelope is the fixture for the failure cases
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
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
