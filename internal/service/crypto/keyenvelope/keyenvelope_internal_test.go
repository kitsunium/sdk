package keyenvelope

import (
	"encoding/base64"
	"strings"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// testIterations is the KDF work factor used by every case whose subject is
// framing, AAD binding, determinism or error mapping — none of which depend on
// the cost. A unit test's passphrase is a literal in this file, so a
// production work factor guards nothing here and only burns CPU: the suite
// used to spend ten 600000-round derivations proving that strings.Split yields
// seven segments (issue #126). Two rounds, not one, so the XOR-folding loop
// PBKDF2 skips at i=1 is still exercised.
const testIterations int = 2

// goldenPassphrase is the passphrase goldenEnvelope was sealed under.
const goldenPassphrase string = "golden passphrase"

// goldenEnvelope is a vector produced ONCE by WrapKey at the pinned 600000
// iterations over goldenDEK. It is the only thing in the suite that fails when
// the production cost changes: every other case derives and verifies with the
// same factor, so it stays green at any value including 1.
const goldenEnvelope string = "$kenv$v=1$kdf=pbkdf2-sha256$" +
	"jbajUIaaBiyK2ejdjOj3GILQ+0uX+VnS+KR7Wq7Z1Pk" +
	"$aead=aes-256-gcm$" +
	"AQHSI5WWa3AlIF4+ghNEu6Uuhs6HF7x12G4isGyq/IpHUA8PVbxmoJ2Pdkw+b1HcmdmguJSQTEtK7DVFeVQ"

// goldenDEK returns the 32-byte DEK sealed inside goldenEnvelope.
func goldenDEK(tb testing.TB) corecrypto.Key {
	tb.Helper()
	raw := make([]byte, corecrypto.KeyLen)
	//: the same recognizable, non-zero pattern the vector was minted from
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

// Test_header asserts the rendered header matches the frozen grammar prefix.
func Test_header(t *testing.T) {
	t.Parallel()
	//: table-driven cases keep arms isolated
	cases := []struct {
		name    string
		b64salt string
	}{
		{name: "simple", b64salt: "AAAA"},
		{name: "empty", b64salt: ""},
	}
	check := func(t *testing.T, b64salt string) {
		t.Helper()
		got := string(header(b64salt))
		want := "$kenv$v=1$kdf=pbkdf2-sha256$" + b64salt + "$aead=aes-256-gcm$"
		//: the header must equal the frozen prefix exactly
		if got != want {
			t.Fatalf("header = %q want %q", got, want)
		}
	}
	//: iterate cases under a parallel parent
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			check(t, tc.b64salt)
		})
	}
}

// Test_deriveKEK asserts derivation is deterministic and KeyLen-sized.
func Test_deriveKEK(t *testing.T) {
	t.Parallel()
	//: table-driven cases keep arms isolated
	cases := []struct {
		name string
		pass []byte
		salt []byte
	}{
		{name: "basic", pass: []byte("pw"), salt: make([]byte, saltLen)},
	}
	check := func(t *testing.T, pass, salt []byte) {
		t.Helper()
		//: determinism and output length are properties of PBKDF2 at ANY
		//: round count, so this case buys nothing by paying the production one
		a, aerr := deriveKEK(pass, salt, testIterations)
		b, berr := deriveKEK(pass, salt, testIterations)
		//: both derivations must succeed with valid params
		if aerr != nil || berr != nil {
			t.Fatalf("deriveKEK errs = %v / %v", aerr, berr)
		}
		//: the same passphrase and salt must derive the same KEK
		if string(a.Bytes()) != string(b.Bytes()) {
			t.Fatalf("deriveKEK not deterministic")
		}
		//: the KEK must be KeyLen bytes for AES-256
		if len(a.Bytes()) != corecrypto.KeyLen {
			t.Fatalf("kek len = %d want %d", len(a.Bytes()), corecrypto.KeyLen)
		}
	}
	//: iterate cases under a parallel parent
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			check(t, tc.pass, tc.salt)
		})
	}
}

// Test_validFraming asserts the framing predicate accepts only the frozen shape.
func Test_validFraming(t *testing.T) {
	t.Parallel()
	good := strings.Split("$kenv$v=1$kdf=pbkdf2-sha256$AAAA$aead=aes-256-gcm$Ym94", "$")
	//: table-driven cases keep arms isolated; want is the single discriminator
	cases := []struct {
		name  string
		parts []string
		want  bool
	}{
		{name: "well-formed", parts: good, want: true},
		{name: "few-fields", parts: []string{"", "kenv", "v=1"}, want: false},
		{name: "bad-magic", parts: strings.Split("$xenv$v=1$kdf=pbkdf2-sha256$AAAA$aead=aes-256-gcm$Ym94", "$"), want: false},
		{name: "bad-version", parts: strings.Split("$kenv$v=2$kdf=pbkdf2-sha256$AAAA$aead=aes-256-gcm$Ym94", "$"), want: false},
		{name: "bad-kdf", parts: strings.Split("$kenv$v=1$kdf=scrypt$AAAA$aead=aes-256-gcm$Ym94", "$"), want: false},
		{name: "bad-aead", parts: strings.Split("$kenv$v=1$kdf=pbkdf2-sha256$AAAA$aead=chacha$Ym94", "$"), want: false},
	}
	check := func(t *testing.T, parts []string, want bool) {
		t.Helper()
		//: only the frozen framing shape is accepted
		if got := validFraming(parts); got != want {
			t.Fatalf("validFraming = %v want %v", got, want)
		}
	}
	//: iterate cases under a parallel parent
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			check(t, tc.parts, tc.want)
		})
	}
}

// Test_parseEnvelope asserts structural faults map to InvalidKeyEnvelope.
func Test_parseEnvelope(t *testing.T) {
	t.Parallel()
	good := "$kenv$v=1$kdf=pbkdf2-sha256$" +
		base64.RawStdEncoding.EncodeToString(make([]byte, saltLen)) +
		"$aead=aes-256-gcm$" + base64.RawStdEncoding.EncodeToString([]byte("box"))
	//: table-driven cases keep arms isolated; wantErr is the single discriminator
	cases := []struct {
		name    string
		env     string
		wantErr bool
	}{
		{name: "well-formed", env: good, wantErr: false},
		{name: "few-fields", env: "$kenv$v=1", wantErr: true},
		{name: "bad-kdf", env: strings.Replace(good, "pbkdf2-sha256", "scrypt", 1), wantErr: true},
		{name: "short-salt", env: "$kenv$v=1$kdf=pbkdf2-sha256$AAAA$aead=aes-256-gcm$Ym94", wantErr: true},
	}
	check := func(t *testing.T, env string, wantErr bool) {
		t.Helper()
		_, _, err := parseEnvelope(env)
		//: a structural fault must surface InvalidKeyEnvelope
		if wantErr {
			//: the error must carry the core sentinel code
			if !errs.HasCode(err, corecrypto.CodeInvalidKeyEnvelope) {
				t.Fatalf("err = %v want InvalidKeyEnvelope", err)
			}
			return
		}
		//: a well-formed envelope must parse cleanly
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
	}
	//: iterate cases under a parallel parent
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			check(t, tc.env, tc.wantErr)
		})
	}
}

// Test_iterations pins the production KDF policy. It is the guard the suite
// did not have: with every other case deriving AND verifying at the same
// factor, setting iterations to 1 — destroying the whole work factor — left
// the package green and merely three times faster. The golden vector is
// verified with the policy the code declares, so it fails the moment the two
// diverge, and a cheaper factor must be refused by the AEAD rather than
// silently accepted.
func Test_iterations(t *testing.T) {
	t.Parallel()
	//: the declared policy IS the wire contract — the envelope has no cost field,
	//: so lowering it silently invalidates every envelope already at rest
	if iterations != 600000 {
		t.Fatalf("iterations = %d want 600000", iterations)
	}
	want := goldenDEK(t)
	//: table-driven cases keep arms isolated; wantOK is the single discriminator
	cases := []struct {
		name   string
		iters  int
		wantOK bool
	}{
		{name: "pinned-policy-opens-the-vector", iters: iterations, wantOK: true},
		{name: "cheaper-policy-is-refused", iters: testIterations, wantOK: false},
	}
	check := func(t *testing.T, iters int, wantOK bool) {
		t.Helper()
		got, err := unwrapKey([]byte(goldenPassphrase), goldenEnvelope, iters)
		//: any factor but the minted one derives a different KEK, so the open fails
		if !wantOK {
			//: the failure must be the non-oracle decryption sentinel
			if !errs.HasCode(err, corecrypto.CodeDecryptionFailed) {
				t.Fatalf("err = %v want DecryptionFailed", err)
			}
			return
		}
		//: the vector must open under the policy it was minted at
		if err != nil {
			t.Fatalf("unwrap golden: %v", err)
		}
		//: and yield back the exact DEK it was minted from
		if string(got.Bytes()) != string(want.Bytes()) {
			t.Fatalf("golden dek mismatch")
		}
	}
	//: iterate cases under a parallel parent
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			check(t, tc.iters, tc.wantOK)
		})
	}
}

// Test_wrapKey exercises wrap-then-unwrap round-trips at a cheap work factor.
// The round trip proves the salt, the AAD header and the box agree; none of
// those depend on the number of KDF rounds, which Test_iterations pins on its
// own against a vector minted at the production policy.
func Test_wrapKey(t *testing.T) {
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
		dek := goldenDEK(t)
		env, err := wrapKey(pass, dek, testIterations)
		//: wrapping a valid DEK must succeed
		if err != nil {
			t.Fatalf("wrap: %v", err)
		}
		//: the envelope must carry the frozen header prefix
		if !strings.HasPrefix(env, "$kenv$v=1$kdf=pbkdf2-sha256$") {
			t.Fatalf("bad prefix: %q", env)
		}
		out, uerr := unwrapKey(pass, env, testIterations)
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

// Test_unwrapKey asserts the failure shapes that only surface AFTER the KEK is
// derived, so they are exercised at a cheap work factor. A wrong passphrase,
// the AAD-bound salt swap and a structurally short box all fail inside the
// AEAD open as DecryptionFailed. The salt swap is the load-bearing case: it
// keeps the framing valid yet changes the only per-envelope AAD field, proving
// the salt is genuinely bound into the AEAD rather than merely framed. The
// faults rejected BEFORE any derivation are black-box tested in
// Test_UnwrapKey, where they cost nothing.
func Test_unwrapKey(t *testing.T) {
	t.Parallel()
	base, err := wrapKey([]byte("right"), goldenDEK(t), testIterations)
	//: a valid envelope is the fixture for the failure cases
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	//: a different but structurally-valid 32-byte salt keeps framing valid yet
	//: changes the per-envelope AAD field, so the open fails inside the AEAD.
	saltSwap := strings.Split(base, "$")
	saltSwap[fieldSalt] = base64.RawStdEncoding.EncodeToString([]byte("fedcba9876543210fedcba9876543210"))
	//: a valid-framing envelope whose box ("Ym94" -> "box", 3 bytes) is shorter
	//: than the AES-GCM nonce+tag must surface the fault from the AEAD open.
	shortBox := strings.Split(base, "$")
	shortBox[fieldBox] = "Ym94"
	//: table-driven cases keep arms isolated; every arm wants DecryptionFailed
	cases := []struct {
		name     string
		pass     []byte
		envelope string
	}{
		{name: "wrong-pass", pass: []byte("wrong"), envelope: base},
		{name: "salt-swap", pass: []byte("right"), envelope: strings.Join(saltSwap, "$")},
		{name: "short-box", pass: []byte("right"), envelope: strings.Join(shortBox, "$")},
	}
	check := func(t *testing.T, pass []byte, envelope string) {
		t.Helper()
		_, uerr := unwrapKey(pass, envelope, testIterations)
		//: the failure must carry the non-oracle decryption sentinel
		if !errs.HasCode(uerr, corecrypto.CodeDecryptionFailed) {
			t.Fatalf("err = %v want DecryptionFailed", uerr)
		}
	}
	//: iterate cases under a parallel parent
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			check(t, tc.pass, tc.envelope)
		})
	}
}
