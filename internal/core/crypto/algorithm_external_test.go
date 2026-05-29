package crypto_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/crypto"
)

func TestAlgorithm_String(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   crypto.Algorithm
		want string
	}
	tests := []tc{
		{"named algorithm", "aes-256-gcm", "aes-256-gcm"},
		{"empty zero value", "", ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: String is a plain cast back to the raw identifier.
		if got := c.in.String(); got != c.want {
			t.Errorf("%s: String()=%q want %q", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestAlgorithm_Known(t *testing.T) {
	//: sequential — seeds + reads the process-wide registry.
	crypto.ResetForTest()
	crypto.Register(fakeAEAD{name: "known-fake", id: 0x70})
	type tc struct {
		name   string
		in     crypto.Algorithm
		wantOK bool
	}
	tests := []tc{
		{"registered algorithm is known", "known-fake", true},
		{"unregistered algorithm is unknown", "absent-zzz", false},
		{"empty zero value is never known", "", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: Known short-circuits the empty zero value and delegates otherwise.
		if got := c.in.Known(); got != c.wantOK {
			t.Errorf("%s: Known()=%v want %v", c.name, got, c.wantOK)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
