package crypto_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/crypto"
)

// bracketed is rule 4's log-parser header. A refusal must still carry one:
// widening the guard must not cost the dotted-quad code an operator greps for.
var bracketed = regexp.MustCompile(`\[[\d.]+(?: <- [\d.]+)*(?: \(truncated\))? \w+\]`)

// typedNilAEAD's nil pointer satisfies AEAD; uncomparableAEAD holds a slice, so
// `==` on it panics. One pair per port, because a struct embedding several of
// them would promote an ambiguous Algorithm and satisfy none.
type typedNilAEAD struct{ crypto.AEAD }

type uncomparableAEAD struct {
	crypto.AEAD
	tags []string
}

// typedNilHasher's nil pointer satisfies Hasher; uncomparableHasher holds a slice, so
// `==` on it panics. One pair per port, because a struct embedding several of
// them would promote an ambiguous Algorithm and satisfy none.
type typedNilHasher struct{ crypto.Hasher }

type uncomparableHasher struct {
	crypto.Hasher
	tags []string
}

// typedNilSigner's nil pointer satisfies Signer; uncomparableSigner holds a slice, so
// `==` on it panics. One pair per port, because a struct embedding several of
// them would promote an ambiguous Algorithm and satisfy none.
type typedNilSigner struct{ crypto.Signer }

type uncomparableSigner struct {
	crypto.Signer
	tags []string
}

// typedNilMAC's nil pointer satisfies MAC; uncomparableMAC holds a slice, so
// `==` on it panics. One pair per port, because a struct embedding several of
// them would promote an ambiguous Algorithm and satisfy none.
type typedNilMAC struct{ crypto.MAC }

type uncomparableMAC struct {
	crypto.MAC
	tags []string
}

// typedNilDeriver's nil pointer satisfies Deriver; uncomparableDeriver holds a slice, so
// `==` on it panics. One pair per port, because a struct embedding several of
// them would promote an ambiguous Algorithm and satisfy none.
type typedNilDeriver struct{ crypto.Deriver }

type uncomparableDeriver struct {
	crypto.Deriver
	tags []string
}

// typedNilAgreement's nil pointer satisfies Agreement; uncomparableAgreement holds a slice, so
// `==` on it panics. One pair per port, because a struct embedding several of
// them would promote an ambiguous Algorithm and satisfy none.
type typedNilAgreement struct{ crypto.Agreement }

type uncomparableAgreement struct {
	crypto.Agreement
	tags []string
}

// typedNilPasswordHasher's nil pointer satisfies PasswordHasher; uncomparablePasswordHasher holds a slice, so
// `==` on it panics. One pair per port, because a struct embedding several of
// them would promote an ambiguous Algorithm and satisfy none.
type typedNilPasswordHasher struct{ crypto.PasswordHasher }

type uncomparablePasswordHasher struct {
	crypto.PasswordHasher
	tags []string
}

// typedNilStreamSealer's nil pointer satisfies StreamSealer; uncomparableStreamSealer holds a slice, so
// `==` on it panics. One pair per port, because a struct embedding several of
// them would promote an ambiguous Algorithm and satisfy none.
type typedNilStreamSealer struct{ crypto.StreamSealer }

type uncomparableStreamSealer struct {
	crypto.StreamSealer
	tags []string
}

// TestEveryRegistrarRefusesAPlugInTheRegistryCannotStore covers all EIGHT
// capability registrars in one table, because they share one guard and would
// otherwise drift apart one registrar at a time. Two shapes satisfy the port at
// compile time and cannot serve: a typed nil, which the old `== nil` guard let
// through and which fails at the first dispatch instead of at registration; and
// a non-comparable plug-in, whose duplicate check panics with Go's "comparing
// uncomparable type" rather than the domain's DUPLICATE_REGISTRATION.
//
// The ports are embedded rather than implemented: the refusal must happen
// BEFORE any method is called, which is what lets a typed nil be refused at all.
func TestEveryRegistrarRefusesAPlugInTheRegistryCannotStore(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		register func()
		want     string
	}{
		{"a typed nil aead", func() { crypto.Register((*typedNilAEAD)(nil)) }, "nil *crypto_test.typedNilAEAD"},
		{"a non-comparable aead", func() { crypto.Register(uncomparableAEAD{tags: []string{"x"}}) }, "crypto_test.uncomparableAEAD is not comparable"},
		{"a typed nil hasher", func() { crypto.RegisterHasher((*typedNilHasher)(nil)) }, "nil *crypto_test.typedNilHasher"},
		{"a non-comparable hasher", func() { crypto.RegisterHasher(uncomparableHasher{tags: []string{"x"}}) }, "crypto_test.uncomparableHasher is not comparable"},
		{"a typed nil signer", func() { crypto.RegisterSigner((*typedNilSigner)(nil)) }, "nil *crypto_test.typedNilSigner"},
		{"a non-comparable signer", func() { crypto.RegisterSigner(uncomparableSigner{tags: []string{"x"}}) }, "crypto_test.uncomparableSigner is not comparable"},
		{"a typed nil MAC", func() { crypto.RegisterMAC((*typedNilMAC)(nil)) }, "nil *crypto_test.typedNilMAC"},
		{"a non-comparable MAC", func() { crypto.RegisterMAC(uncomparableMAC{tags: []string{"x"}}) }, "crypto_test.uncomparableMAC is not comparable"},
		{"a typed nil deriver", func() { crypto.RegisterDeriver((*typedNilDeriver)(nil)) }, "nil *crypto_test.typedNilDeriver"},
		{"a non-comparable deriver", func() { crypto.RegisterDeriver(uncomparableDeriver{tags: []string{"x"}}) }, "crypto_test.uncomparableDeriver is not comparable"},
		{"a typed nil agreement", func() { crypto.RegisterAgreement((*typedNilAgreement)(nil)) }, "nil *crypto_test.typedNilAgreement"},
		{"a non-comparable agreement", func() { crypto.RegisterAgreement(uncomparableAgreement{tags: []string{"x"}}) }, "crypto_test.uncomparableAgreement is not comparable"},
		{"a typed nil password hasher", func() { crypto.RegisterPasswordHasher((*typedNilPasswordHasher)(nil)) }, "nil *crypto_test.typedNilPasswordHasher"},
		{"a non-comparable password hasher", func() { crypto.RegisterPasswordHasher(uncomparablePasswordHasher{tags: []string{"x"}}) }, "crypto_test.uncomparablePasswordHasher is not comparable"},
		{"a typed nil stream sealer", func() { crypto.RegisterStreamSealer((*typedNilStreamSealer)(nil)) }, "nil *crypto_test.typedNilStreamSealer"},
		{"a non-comparable stream sealer", func() { crypto.RegisterStreamSealer(uncomparableStreamSealer{tags: []string{"x"}}) }, "crypto_test.uncomparableStreamSealer is not comparable"},
	}
	runCase := func(t *testing.T, name string, register func(), want string) {
		t.Helper()
		defer func() {
			//: the registrars refuse by panicking at import time, so recover is
			//: the only place the refusal can be read.
			r := recover()
			if r == nil {
				t.Fatalf("%s: registered without a refusal", name)
			}
			msg, isString := r.(string)
			if !isString {
				t.Fatalf("%s: panicked with %T (%v), want the registrar's message", name, r, r)
			}
			if !strings.Contains(msg, want) {
				t.Errorf("%s: refusal %q does not say %q", name, msg, want)
			}
			if !bracketed.MatchString(msg) {
				t.Errorf("%s: refusal %q carries no dotted-quad header", name, msg)
			}
		}()
		register()
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc.name, tc.register, tc.want)
		})
	}
}
