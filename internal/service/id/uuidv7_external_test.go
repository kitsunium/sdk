// Package id_test — the UUIDv7 generator as a consumer reaches it.
package id_test

import (
	"testing"

	coreid "github.com/kitsunium/sdk/internal/core/id"
	svcid "github.com/kitsunium/sdk/internal/service/id"
)

// TestUUIDv7 pins the registration and the ordering property a consumer picks
// v7 for: two identifiers minted in sequence must not have regressing
// millisecond prefixes, or the format offers nothing a v4 does not.
func TestUUIDv7(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		mint func() (string, error)
	}
	tests := []tc{
		{"through the exported generator", svcid.UUIDv7.New},
		{"through the registry", func() (string, error) { return coreid.New("uuidv7") }},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		first, err := c.mint()
		if err != nil {
			t.Fatalf("New = %v, want nil", err)
		}
		second, err := c.mint()
		if err != nil {
			t.Fatalf("New = %v, want nil", err)
		}
		assertDashedUUID(t, first, '7')
		assertDashedUUID(t, second, '7')
		//: the 13-character millisecond prefix must never regress.
		if second[:13] < first[:13] {
			t.Errorf("the time prefix regressed: %q then %q", first, second)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: the scheme must resolve by name, which is what the blank import buys.
	if !coreid.Scheme("uuidv7").Known() {
		t.Error("the uuidv7 scheme did not self-register on import")
	}
}
