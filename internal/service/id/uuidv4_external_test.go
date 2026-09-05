// Package id_test — the UUIDv4 generator as a consumer reaches it.
package id_test

import (
	"testing"

	coreid "github.com/kitsunium/sdk/internal/core/id"
	svcid "github.com/kitsunium/sdk/internal/service/id"
)

// TestUUIDv4 pins that blank-importing the package registers the scheme and
// that what comes out is a canonical, unguessable v4. The registration is the
// part a consumer cannot verify any other way: nothing in their code names
// uuidv4Gen.
func TestUUIDv4(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		mint func() (string, error)
	}
	tests := []tc{
		{"through the exported generator", svcid.UUIDv4.New},
		{"through the registry", func() (string, error) { return coreid.New("uuidv4") }},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := c.mint()
		if err != nil {
			t.Fatalf("New = %v, want nil", err)
		}
		assertDashedUUID(t, got, '4')
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: the scheme must resolve by name, which is what the blank import buys.
	if !coreid.Scheme("uuidv4").Known() {
		t.Error("the uuidv4 scheme did not self-register on import")
	}
}

// assertDashedUUID checks the canonical rendering and the version nibble.
func assertDashedUUID(t *testing.T, got string, version byte) {
	t.Helper()
	if len(got) != 36 {
		t.Fatalf("New() = %q (%d chars), want 36", got, len(got))
	}
	for _, idx := range []int{8, 13, 18, 23} {
		if got[idx] != '-' {
			t.Fatalf("New() = %q has %q at index %d, want a dash", got, got[idx], idx)
		}
	}
	if got[14] != version {
		t.Errorf("New() = %q has version %q, want %q", got, got[14], version)
	}
}
