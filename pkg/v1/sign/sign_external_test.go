package sign_test

import (
	"reflect"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/sign"
)

func TestGenerateKey(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		alg     sign.Algorithm
		wantErr bool
	}
	tests := []tc{
		{"ed25519 yields a keypair", sign.Ed25519, false},
		{"unknown algorithm errors", "no-such-scheme", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		pub, priv, err := sign.GenerateKey(c.alg)
		//: the failure arm must error and yield no keys.
		if c.wantErr {
			if err == nil || pub != nil || priv != nil {
				t.Errorf("%s: GenerateKey accepted an unregistered scheme", c.name)
			}
			return
		}
		//: a registered scheme returns both key halves.
		if err != nil || len(pub) == 0 || len(priv) == 0 {
			t.Fatalf("%s: GenerateKey=(%d,%d,%v)", c.name, len(pub), len(priv), err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestSign(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		alg     sign.Algorithm
		wantErr bool
	}
	tests := []tc{
		{"ed25519 signs", sign.Ed25519, false},
		{"unknown algorithm errors", "no-such-scheme", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: a real ed25519 key backs the success arm; the failure arm never signs.
		_, priv, genErr := sign.GenerateKey(sign.Ed25519)
		if genErr != nil {
			t.Fatalf("%s: GenerateKey: %v", c.name, genErr)
		}
		sig, err := sign.Sign(c.alg, priv, []byte("message"))
		//: the failure arm must error and yield no signature.
		if c.wantErr {
			if err == nil || sig != nil {
				t.Errorf("%s: Sign accepted an unregistered scheme", c.name)
			}
			return
		}
		//: a registered scheme returns a non-empty signature.
		if err != nil || len(sig) == 0 {
			t.Errorf("%s: Sign=(%d bytes,%v)", c.name, len(sig), err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestVerify(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		tamper bool
		wantOK bool
	}
	tests := []tc{
		{"a genuine signature verifies", false, true},
		{"a tampered message is rejected without error", true, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		pub, priv, err := sign.GenerateKey(sign.Ed25519)
		if err != nil {
			t.Fatalf("%s: GenerateKey: %v", c.name, err)
		}
		sig, err := sign.Sign(sign.Ed25519, priv, []byte("message"))
		if err != nil {
			t.Fatalf("%s: Sign: %v", c.name, err)
		}
		//: the tamper arm verifies a different message than the one signed.
		message := []byte("message")
		if c.tamper {
			message = []byte("tampered")
		}
		ok, vErr := sign.Verify(sign.Ed25519, pub, message, sig)
		//: a rejected signature is (false, nil) — never an error oracle.
		if vErr != nil || ok != c.wantOK {
			t.Errorf("%s: Verify=(%v,%v) want (%v,nil)", c.name, ok, vErr, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestVerifyUnknownAlgorithm(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		alg  sign.Algorithm
	}
	tests := []tc{
		{"unknown scheme surfaces an error, not a bare false", "no-such-scheme"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: an unregistered scheme is a configuration error, distinct from a bad sig.
		ok, err := sign.Verify(c.alg, []byte("pub"), []byte("m"), []byte("sig"))
		if ok || err == nil {
			t.Errorf("%s: Verify=(%v,%v) want (false, error)", c.name, ok, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestAlgorithmIsDomainDefinedType_V104 pins the V104 fix: sign.Algorithm is a
// DEFINED type owned by this facade, not a bare alias of the shared
// internal/core/crypto.Algorithm. While Algorithm was an alias, all seven
// crypto-family facades shared one identical Go type, so a hash or MAC constant
// fed into a signature call type-checked and only misrouted at runtime. A
// defined type makes that cross-domain mix a compile error; reflection witnesses
// the change because a defined type reports its own package path, whereas an
// alias reports internal/core/crypto. This test FAILS before the alias→defined-
// type change and PASSES after.
func TestAlgorithmIsDomainDefinedType_V104(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		got  string
		want string
	}
	rt := reflect.TypeFor[sign.Algorithm]()
	tests := []tc{
		//: a defined type reports its declaring package; an alias reports corecrypto's.
		{"package path is this facade", rt.PkgPath(), "github.com/kitsunium/sdk/pkg/v1/sign"},
		//: the defined type names itself Algorithm in this package.
		{"type name is Algorithm", rt.Name(), "Algorithm"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: a mismatch means Algorithm is still an alias of corecrypto.Algorithm.
			if c.got != c.want {
				t.Errorf("sign.Algorithm %s=%q want %q (still an alias?)", c.name, c.got, c.want)
			}
		})
	}
}
