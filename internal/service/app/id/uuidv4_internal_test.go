// Package id — white-box tests for the UUIDv4 generator.
package id

import "testing"

// Test_uuidv4Gen_Scheme pins the registry key. It is what a caller passes to
// id.New, so a drift here unregisters the generator from every consumer that
// asks for it by name.
func Test_uuidv4Gen_Scheme(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"the canonical key", "uuidv4"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := string(uuidv4Gen{}.Scheme()); got != c.want {
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

// Test_uuidv4Gen_New pins the shape and the uniqueness a v4 identifier is
// chosen for. There is no timestamp here on purpose: v4 is 122 bits of
// entropy, which is exactly what makes it unguessable and exactly what makes
// it unordered.
func Test_uuidv4Gen_New(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		count int
	}
	tests := []tc{
		{"a single identifier", 1},
		{"a batch", 4096},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		seen := make(map[string]struct{}, c.count)
		for range c.count {
			got, err := uuidv4Gen{}.New()
			if err != nil {
				t.Fatalf("New = %v, want nil", err)
			}
			assertCanonicalUUID(t, got, '4')
			if _, dup := seen[got]; dup {
				t.Fatalf("New() repeated %q", got)
			}
			seen[got] = struct{}{}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// assertCanonicalUUID checks the dashed rendering, the version nibble and the
// RFC variant bits — the three properties every parser in the ecosystem reads.
func assertCanonicalUUID(t *testing.T, got string, version byte) {
	t.Helper()
	//: the canonical dashed form is exactly 36 characters.
	if len(got) != 36 {
		t.Fatalf("New() = %q (%d chars), want 36", got, len(got))
	}
	for _, idx := range []int{8, 13, 18, 23} {
		if got[idx] != '-' {
			t.Fatalf("New() = %q has %q at index %d, want a dash", got, got[idx], idx)
		}
	}
	//: the version nibble is the first character of the third group.
	if got[14] != version {
		t.Errorf("New() = %q has version %q, want %q", got, got[14], version)
	}
	//: the variant is 10xx, so the first character of the fourth group is one
	//: of 8, 9, a or b.
	switch got[19] {
	case '8', '9', 'a', 'b':
	default:
		t.Errorf("New() = %q has variant character %q, want one of 89ab", got, got[19])
	}
}
