package keyenvelope

import (
	"encoding/base64"
	"strings"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

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
		a, aerr := deriveKEK(pass, salt)
		b, berr := deriveKEK(pass, salt)
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
