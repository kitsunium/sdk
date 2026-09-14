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

// Test_WrapKey is the black-box conformance pass over the exported API at the
// PINNED work factor: the envelope is sealed and reopened by the real
// WrapKey/UnwrapKey, so the 600000-round policy is exercised end to end rather
// than asserted about. The wrong-passphrase refusal shares that one envelope
// instead of minting its own, because every derivation here costs the
// production work factor — the whole subject of issue #126. The shapes that do
// NOT depend on the cost (an empty passphrase, the AAD-bound salt swap, a
// short box) are exercised at two rounds in Test_wrapKey / Test_unwrapKey.
func Test_WrapKey(t *testing.T) {
	t.Parallel()
	dek := dek32(t)
	pass := []byte("correct horse battery staple")
	env, err := keyenvelope.WrapKey(pass, dek)
	//: wrapping a valid DEK must succeed
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	//: the envelope must carry the frozen header prefix
	if !strings.HasPrefix(env, "$kenv$v=1$kdf=pbkdf2-sha256$") {
		t.Fatalf("bad prefix: %q", env)
	}
	//: both opens run as PARALLEL subtests over the one envelope. Chaining them
	//: would put three 600000-round derivations on a single critical path, which
	//: costs real wall time on a box that has cores to spare.
	t.Run("right-passphrase-recovers-the-dek", func(t *testing.T) {
		t.Parallel()
		out, uerr := keyenvelope.UnwrapKey(pass, env)
		//: unwrapping with the same passphrase must succeed
		if uerr != nil {
			t.Fatalf("unwrap: %v", uerr)
		}
		//: the recovered DEK must equal the original
		if string(out.Bytes()) != string(dek.Bytes()) {
			t.Fatalf("dek mismatch")
		}
	})
	t.Run("wrong-passphrase-is-refused", func(t *testing.T) {
		t.Parallel()
		_, werr := keyenvelope.UnwrapKey([]byte("wrong"), env)
		//: a wrong passphrase forwards DecryptionFailed — no key/password oracle
		if !errs.HasCode(werr, corecrypto.CodeDecryptionFailed) {
			t.Fatalf("err = %v want DecryptionFailed", werr)
		}
	})
}

// Test_UnwrapKey asserts the failure shapes the exported API rejects on
// FRAMING, before a KEK is ever derived. That is what makes them free: each
// case is a literal, so none of them pays the 600000-round work factor and
// none needs a WrapKey fixture to build on.
func Test_UnwrapKey(t *testing.T) {
	t.Parallel()
	//: a structurally well-formed envelope to mutate one field at a time
	const good string = "$kenv$v=1$kdf=pbkdf2-sha256$" +
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" +
		"$aead=aes-256-gcm$Ym94"
	//: table-driven cases keep arms isolated; every arm wants InvalidKeyEnvelope
	cases := []struct {
		name     string
		envelope string
	}{
		{name: "corrupt", envelope: "$kenv$bad"},
		{name: "tampered-kdf", envelope: strings.Replace(good, "pbkdf2-sha256", "scrypt", 1)},
		{name: "tampered-aead", envelope: strings.Replace(good, "aes-256-gcm", "chacha", 1)},
		{name: "bad-version", envelope: strings.Replace(good, "v=1", "v=2", 1)},
		{name: "short-salt", envelope: "$kenv$v=1$kdf=pbkdf2-sha256$AAAA$aead=aes-256-gcm$Ym94"},
	}
	check := func(t *testing.T, envelope string) {
		t.Helper()
		_, uerr := keyenvelope.UnwrapKey([]byte("right"), envelope)
		//: a structural fault must surface the core InvalidKeyEnvelope sentinel
		if !errs.HasCode(uerr, corecrypto.CodeInvalidKeyEnvelope) {
			t.Fatalf("err = %v want InvalidKeyEnvelope", uerr)
		}
	}
	//: iterate cases under a parallel parent
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			check(t, tc.envelope)
		})
	}
}
