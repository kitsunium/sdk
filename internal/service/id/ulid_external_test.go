// Package id_test — the ULID generator as a consumer reaches it.
package id_test

import (
	"strings"
	"testing"

	coreid "github.com/kitsunium/sdk/internal/core/id"
	svcid "github.com/kitsunium/sdk/internal/service/id"
)

// crockfordAlphabet is Crockford base32 — it deliberately excludes I, L, O and
// U, which are the characters a human transcribing an identifier confuses.
const crockfordAlphabet string = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// TestULID pins that blank-importing the package registers the scheme and that
// what comes out is a usable ULID. The registration is the part a consumer
// cannot verify any other way: nothing in their code names ulidGen.
func TestULID(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		mint func() (string, error)
	}
	tests := []tc{
		{"through the exported generator", svcid.ULID.New},
		{"through the registry", func() (string, error) { return coreid.New("ulid") }},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := c.mint()
		if err != nil {
			t.Fatalf("New = %v, want nil", err)
		}
		//: 26 Crockford characters is the canonical rendering.
		if len(got) != 26 {
			t.Fatalf("New() = %q (%d chars), want 26", got, len(got))
		}
		for _, r := range got {
			if !strings.ContainsRune(crockfordAlphabet, r) {
				t.Errorf("New() = %q contains %q, outside the Crockford alphabet", got, r)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: the scheme must resolve by name, which is what the blank import buys.
	if !coreid.Scheme("ulid").Known() {
		t.Error("the ulid scheme did not self-register on import")
	}
}
