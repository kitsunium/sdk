// Package id — white-box tests for the UUIDv7 generator.
package id

import "testing"

// Test_uuidv7Gen_Scheme pins the registry key. It is what a caller passes to
// id.New, so a drift here unregisters the generator from every consumer that
// asks for it by name.
func Test_uuidv7Gen_Scheme(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"the canonical key", "uuidv7"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := string(uuidv7Gen{}.Scheme()); got != c.want {
			t.Errorf("Scheme() = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_uuidv7Gen_New pins the shape and the ordering property. A v7 identifier
// is chosen over a v4 precisely because its 48-bit millisecond prefix makes
// successive ids k-sortable, so a regression in that prefix would silently
// remove the only reason to use the format.
func Test_uuidv7Gen_New(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		count int
	}
	tests := []tc{
		{"a single identifier", 1},
		{"a pair", 2},
		{"a batch", 2048},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		seen := make(map[string]struct{}, c.count)
		var prev string
		for range c.count {
			got, err := uuidv7Gen{}.New()
			if err != nil {
				t.Fatalf("New = %v, want nil", err)
			}
			assertCanonicalUUID(t, got, '7')
			if _, dup := seen[got]; dup {
				t.Fatalf("New() repeated %q", got)
			}
			seen[got] = struct{}{}

			//: the first 13 characters are the millisecond prefix (8 + dash +
			//: 4); within one millisecond the tail is unordered by design.
			if prev != "" && got[:13] < prev[:13] {
				t.Errorf("the time prefix regressed: %q then %q", prev, got)
			}
			prev = got
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
